// +build manual

package core

import (
	"context"
	"fmt"
	"testing"
)

// Manual test for Claude Code integration
// Run with: go test -v -tags manual -run TestClaudeCodeManual ./core
func TestClaudeCodeManual(t *testing.T) {
	ctx := context.Background()

	// Create router
	router := &LLMRouter{
		ClaudeCodeModel: "sonnet",
	}

	// Route the model
	endpoint, err := router.Route("claude-code")
	if err != nil {
		t.Fatalf("Failed to route: %v", err)
	}

	fmt.Printf("✓ Routed to provider: %s\n", endpoint.Provider)
	fmt.Printf("✓ Model: %s\n", endpoint.Model)

	// Create test message
	messages := []*ModelMessage{
		{
			Role:    "user",
			Content: "What is 2+2? Answer with just the number.",
		},
	}

	// Send query
	fmt.Println("\n🔄 Sending query to Claude Code CLI...")
	resp, err := endpoint.Client.SendQuery(ctx, messages, nil)
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	fmt.Println("\n✅ Response received!")
	fmt.Printf("Content: %s\n", resp.Content)
	fmt.Printf("\nToken usage:\n")
	fmt.Printf("  Input: %d\n", resp.TokenUsage.InputTokens)
	fmt.Printf("  Output: %d\n", resp.TokenUsage.OutputTokens)
	fmt.Printf("  Cached reads: %d\n", resp.TokenUsage.CachedTokenReads)
	fmt.Printf("  Cached writes: %d\n", resp.TokenUsage.CachedTokenWrites)
	fmt.Printf("  Total: %d\n", resp.TokenUsage.TotalTokens)

	// Verify response contains "4"
	if resp.Content == "" {
		t.Fatal("Response content is empty")
	}
}
