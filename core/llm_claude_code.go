package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"dagger.io/dagger/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ClaudeCodeClient implements LLMClient using Claude Code CLI with Max subscription
type ClaudeCodeClient struct {
	endpoint  *LLMEndpoint
	sessionID string // Track session ID for multi-turn conversations
}

// Ensure ClaudeCodeClient implements LLMClient interface
var _ LLMClient = (*ClaudeCodeClient)(nil)

// NO_TOOLS_PROMPT is a system prompt that instructs Claude to not use any tools
const NO_TOOLS_PROMPT = `You must respond using only plain text without invoking any tools, functions, or external capabilities. Do not use web search, file reading, file writing, code execution, or any other tools even if they are available to you. Do not create artifacts, documents, or any special formatted content blocks. Simply provide your answer directly as regular conversational text. If asked to perform actions that would require tools (like searching the web, analyzing files, or running code), explain what you would do conceptually rather than actually executing these actions. Your entire response should be standard text without any function calls or special formatting. NO USAGE OF SUBAGENTS.`

// newClaudeCodeClient creates a new Claude Code client
func newClaudeCodeClient(endpoint *LLMEndpoint) *ClaudeCodeClient {
	return &ClaudeCodeClient{
		endpoint: endpoint,
	}
}

// Claude Code CLI JSON response structure
type claudeCodeResponse struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int64   `json:"duration_ms"`
	SessionID    string  `json:"session_id"`
	Model        string  `json:"model"`
	NumTurns     int     `json:"num_turns"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
	} `json:"usage"`
}

// IsRetryable checks if an error from Claude Code CLI is retryable
func (c *ClaudeCodeClient) IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	retryableErrors := []string{
		"rate_limit",
		"timeout",
		"overloaded",
		"Internal server error",
		"503",
		"429",
		"connection refused",  // CLI can't connect to API
		"deadline exceeded",   // Context timeout
		"temporary failure",   // Temporary network issues
	}

	for _, retryable := range retryableErrors {
		if strings.Contains(msg, retryable) {
			return true
		}
	}

	return false
}

// SendQuery sends a query to Claude Code CLI and returns the response
func (c *ClaudeCodeClient) SendQuery(ctx context.Context, history []*ModelMessage, tools []LLMTool) (res *LLMResponse, rerr error) {
	stdio := telemetry.SpanStdio(ctx, InstrumentationLibrary)
	defer stdio.Close()

	markdownW := telemetry.NewWriter(ctx, InstrumentationLibrary,
		log.String(telemetry.ContentTypeAttr, "text/markdown"))

	m := telemetry.Meter(ctx, InstrumentationLibrary)
	spanCtx := trace.SpanContextFromContext(ctx)
	attrs := []attribute.KeyValue{
		attribute.String(telemetry.MetricsTraceIDAttr, spanCtx.TraceID().String()),
		attribute.String(telemetry.MetricsSpanIDAttr, spanCtx.SpanID().String()),
		attribute.String("model", c.endpoint.Model),
		attribute.String("provider", "claude-code"),
	}

	// Build the prompt from message history
	prompt, err := c.buildPrompt(history)
	if err != nil {
		return nil, fmt.Errorf("failed to build prompt: %w", err)
	}

	// Execute Claude Code CLI
	output, err := c.executeCLI(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to execute Claude Code CLI: %w", err)
	}

	// Parse the JSON response
	var resp claudeCodeResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse Claude Code response: %w\nOutput: %s", err, output)
	}

	// Check for error responses
	if resp.IsError {
		return nil, fmt.Errorf("claude CLI returned error: %s", resp.Result)
	}

	// Check for empty result
	if resp.Result == "" {
		return nil, fmt.Errorf("claude CLI returned empty result")
	}

	// Store session ID for future calls
	if resp.SessionID != "" {
		c.sessionID = resp.SessionID
	}

	// Write response to telemetry
	fmt.Fprint(markdownW, resp.Result)

	// Record metrics
	durationGauge, err := m.Int64Gauge("llm.duration_ms")
	if err == nil {
		durationGauge.Record(ctx, resp.DurationMS, metric.WithAttributes(attrs...))
	}

	// Note: Claude Code Max subscription should have $0 cost
	// but we track it anyway for monitoring
	costGauge, err := m.Float64Gauge("llm.cost_usd")
	if err == nil {
		costGauge.Record(ctx, resp.TotalCostUSD, metric.WithAttributes(attrs...))
	}

	// Return the response with token usage from CLI output
	return &LLMResponse{
		Content:   resp.Result,
		ToolCalls: []LLMToolCall{}, // No tool calls in no-tools mode
		TokenUsage: LLMTokenUsage{
			InputTokens:       resp.Usage.InputTokens,
			OutputTokens:      resp.Usage.OutputTokens,
			CachedTokenReads:  resp.Usage.CacheReadInputTokens,
			CachedTokenWrites: resp.Usage.CacheCreationInputTokens,
			TotalTokens:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}, nil
}

// buildPrompt constructs a prompt string from message history
func (c *ClaudeCodeClient) buildPrompt(history []*ModelMessage) (string, error) {
	if len(history) == 0 {
		return "", fmt.Errorf("no messages in history")
	}

	// If we have a session ID, we can use --resume for multi-turn
	// For now, we'll concatenate the history into a single prompt
	var parts []string

	for _, msg := range history {
		switch msg.Role {
		case "system":
			parts = append(parts, fmt.Sprintf("[System]: %s", msg.Content))
		case "user":
			parts = append(parts, fmt.Sprintf("[User]: %s", msg.Content))
		case "assistant":
			if msg.Content != "" {
				parts = append(parts, fmt.Sprintf("[Assistant]: %s", msg.Content))
			}
			// Handle tool calls
			for _, toolCall := range msg.ToolCalls {
				argsJSON, _ := json.Marshal(toolCall.Function.Arguments)
				parts = append(parts, fmt.Sprintf("[Tool Call]: %s(%s)", toolCall.Function.Name, string(argsJSON)))
			}
		}

		// Add tool results
		if msg.ToolCallID != "" {
			status := "success"
			if msg.ToolErrored {
				status = "error"
			}
			parts = append(parts, fmt.Sprintf("[Tool Result %s]: %s", status, msg.Content))
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// resolveModel resolves the model name to use with Claude Code CLI
func (c *ClaudeCodeClient) resolveModel() string {
	// If endpoint has a specific model, use it
	if c.endpoint.Model != "" && c.endpoint.Model != "claude-code" {
		// Extract model from "claude-code-sonnet" -> "sonnet"
		model := strings.TrimPrefix(c.endpoint.Model, "claude-code-")
		if model != c.endpoint.Model {
			return model
		}
	}

	// Default to sonnet for best balance
	return "sonnet"
}

// executeCLI executes the Claude Code CLI command and returns the output
func (c *ClaudeCodeClient) executeCLI(ctx context.Context, prompt string) (string, error) {
	// Resolve model name (sonnet, opus, haiku, or full name)
	model := c.resolveModel()

	// Build command arguments
	args := []string{
		"-p",                          // Print mode (non-interactive)
		"--model", model,              // Model selection
		"--append-system-prompt", NO_TOOLS_PROMPT, // Disable tools
		"--max-turns", "1",            // Single turn only
		"--output-format", "json",     // JSON output
		prompt,                        // User prompt
	}

	// Execute command
	cmd := exec.CommandContext(ctx, "claude", args...)

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if claude CLI is not found
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("claude CLI not found in PATH. Please install: npm install -g @anthropic-ai/claude-code")
		}
		return "", fmt.Errorf("claude CLI execution failed: %w\nOutput: %s", err, output)
	}

	return string(output), nil
}
