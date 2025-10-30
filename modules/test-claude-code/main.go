// A module for testing Claude Code LLM integration
//
// This module provides simple functions to test the Claude Code LLM
// integration in Dagger. It allows you to quickly verify that the
// claude-code model is working correctly.

package main

import (
	"context"
	"dagger/test-claude-code/internal/dagger"
)

type TestClaudeCode struct{}

// Test a simple prompt with the claude-code model
func (m *TestClaudeCode) Simple(ctx context.Context,
	// The prompt to send to Claude Code
	// +default="What is 2+2? Answer with just the number."
	prompt string,
) (string, error) {
	return dag.LLM(dagger.LLMOpts{
		Model: "claude-code",
	}).
		WithPrompt(prompt).
		LastReply(ctx)
}

// Test with a custom model variant (sonnet, opus, haiku)
func (m *TestClaudeCode) WithModel(ctx context.Context,
	// The Claude Code model variant to use
	// +default="sonnet"
	model string,
	// The prompt to send
	// +default="What is 2+2? Answer with just the number."
	prompt string,
) (string, error) {
	return dag.LLM(dagger.LLMOpts{
		Model: "claude-code-" + model,
	}).
		WithPrompt(prompt).
		LastReply(ctx)
}

// Test with a system prompt
func (m *TestClaudeCode) WithSystemPrompt(ctx context.Context,
	// The system prompt
	// +default="You are a helpful math tutor."
	systemPrompt string,
	// The user prompt
	// +default="What is 2+2?"
	prompt string,
) (string, error) {
	return dag.LLM(dagger.LLMOpts{
		Model: "claude-code",
	}).
		WithSystemPrompt(systemPrompt).
		WithPrompt(prompt).
		LastReply(ctx)
}

// Test a more complex prompt
func (m *TestClaudeCode) Complex(ctx context.Context,
	// The prompt to send
	// +default="Write a haiku about programming in Dagger"
	prompt string,
) (string, error) {
	return dag.LLM(dagger.LLMOpts{
		Model: "claude-code",
	}).
		WithPrompt(prompt).
		LastReply(ctx)
}

// Test basic math computation
func (m *TestClaudeCode) Math(ctx context.Context,
	// The math question to ask
	// +default="What is 17 * 23?"
	question string,
) (string, error) {
	return dag.LLM(dagger.LLMOpts{
		Model: "claude-code",
	}).
		WithSystemPrompt("You are a calculator. Respond only with the numeric answer, no explanation.").
		WithPrompt(question).
		LastReply(ctx)
}
