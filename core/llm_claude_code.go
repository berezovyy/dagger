package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dagger.io/dagger/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ClaudeCodeClient implements LLMClient using Claude Code CLI with Max subscription
type ClaudeCodeClient struct {
	endpoint     *LLMEndpoint
	workdir      string
	allowedTools []string
	sessionID    string // Track session ID for multi-turn conversations
}

// Ensure ClaudeCodeClient implements LLMClient interface
var _ LLMClient = (*ClaudeCodeClient)(nil)

// newClaudeCodeClient creates a new Claude Code client
func newClaudeCodeClient(endpoint *LLMEndpoint, workdir string, allowedTools []string) *ClaudeCodeClient {
	return &ClaudeCodeClient{
		endpoint:     endpoint,
		workdir:      workdir,
		allowedTools: allowedTools,
	}
}

// Claude Code CLI JSON response structure
type claudeCodeResponse struct {
	Type         string  `json:"type"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int64   `json:"duration_ms"`
	SessionID    string  `json:"session_id"`
	Model        string  `json:"model"`
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
		return nil, fmt.Errorf("failed to parse Claude Code response: %w", err)
	}

	// Store session ID for future calls
	if resp.SessionID != "" {
		c.sessionID = resp.SessionID
	}

	// Write response to telemetry
	fmt.Fprint(markdownW, resp.Result)

	// Record metrics (Claude Code returns cost instead of tokens)
	// We'll track duration as a metric
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

	// Return the response
	return &LLMResponse{
		Content:   resp.Result,
		ToolCalls: []LLMToolCall{}, // TODO: Parse tool calls from response
		TokenUsage: LLMTokenUsage{
			// Claude Code doesn't return token counts, only cost
			// We'll leave these as 0 for now
			InputTokens:  0,
			OutputTokens: 0,
			TotalTokens:  0,
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

// executeCLI executes the Claude Code CLI command and returns the output
// NOTE: This implementation assumes Claude Code CLI is installed and available in the PATH
// For now, this is a placeholder that returns an error indicating the feature needs external setup
func (c *ClaudeCodeClient) executeCLI(ctx context.Context, prompt string) (string, error) {
	// TODO: Implement actual CLI execution
	// This requires one of the following approaches:
	// 1. Expect claude CLI to be installed on the host and use exec.CommandContext
	// 2. Use Dagger's Container API (requires refactoring to access from service context)
	// 3. Connect to an external Claude Code service/daemon

	return "", fmt.Errorf("Claude Code CLI integration is not yet fully implemented. " +
		"This feature requires Claude Code CLI to be installed and configured with OAuth token. " +
		"Please use model='claude-sonnet-*' with ANTHROPIC_API_KEY for now")
}
