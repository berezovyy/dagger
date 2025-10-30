package exec

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestExecAttachableBasic tests basic functionality of ExecAttachable
func TestExecAttachableBasic(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Test stream writer creation
	stdoutWriter := exec.NewStdoutWriter()
	if stdoutWriter == nil {
		t.Fatal("NewStdoutWriter returned nil")
	}

	stderrWriter := exec.NewStderrWriter()
	if stderrWriter == nil {
		t.Fatal("NewStderrWriter returned nil")
	}

	// Test writing to stream
	testData := []byte("test output\n")
	n, err := stdoutWriter.Write(testData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Write returned %d, expected %d", n, len(testData))
	}

	// Test exit code handling
	if err := exec.SendExitCode(0); err != nil {
		t.Fatalf("SendExitCode failed: %v", err)
	}

	// Test cleanup
	if err := exec.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestExecAttachableStreamWriter tests the streamWriter implementation
func TestExecAttachableStreamWriter(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Get stdout writer
	writer := exec.NewStdoutWriter()

	// Write some data
	data := []byte("hello world")
	n, err := writer.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Write returned %d, expected %d", n, len(data))
	}

	// Verify data was queued (non-blocking)
	select {
	case received := <-exec.stdoutChan:
		if string(received) != string(data) {
			t.Fatalf("Received %q, expected %q", received, data)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for data in channel")
	}
}

// TestExecAttachableMultipleWrites tests multiple writes
func TestExecAttachableMultipleWrites(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	stdoutWriter := exec.NewStdoutWriter()
	stderrWriter := exec.NewStderrWriter()

	// Write to stdout
	stdoutData := []byte("stdout data\n")
	if _, err := stdoutWriter.Write(stdoutData); err != nil {
		t.Fatalf("stdout Write failed: %v", err)
	}

	// Write to stderr
	stderrData := []byte("stderr data\n")
	if _, err := stderrWriter.Write(stderrData); err != nil {
		t.Fatalf("stderr Write failed: %v", err)
	}

	// Verify both channels received data
	select {
	case received := <-exec.stdoutChan:
		if string(received) != string(stdoutData) {
			t.Fatalf("stdout: received %q, expected %q", received, stdoutData)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for stdout data")
	}

	select {
	case received := <-exec.stderrChan:
		if string(received) != string(stderrData) {
			t.Fatalf("stderr: received %q, expected %q", received, stderrData)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for stderr data")
	}
}

// TestExecAttachableEmptyWrite tests writing empty data
func TestExecAttachableEmptyWrite(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	writer := exec.NewStdoutWriter()

	// Write empty data
	n, err := writer.Write([]byte{})
	if err != nil {
		t.Fatalf("Empty write failed: %v", err)
	}
	if n != 0 {
		t.Fatalf("Empty write returned %d, expected 0", n)
	}

	// Verify no data was sent to channel
	select {
	case data := <-exec.stdoutChan:
		t.Fatalf("Unexpected data in channel: %q", data)
	case <-time.After(50 * time.Millisecond):
		// Expected - no data should be in channel
	}
}

// TestExecAttachableSendExitCodeMultipleTimes tests that sending exit code multiple times is safe
func TestExecAttachableSendExitCodeMultipleTimes(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Send exit code first time
	if err := exec.SendExitCode(42); err != nil {
		t.Fatalf("First SendExitCode failed: %v", err)
	}

	// Send exit code second time (should be safe)
	if err := exec.SendExitCode(43); err != nil {
		t.Fatalf("Second SendExitCode failed: %v", err)
	}

	// Verify first exit code was sent
	select {
	case exitCode := <-exec.exitChan:
		if exitCode != 42 {
			t.Fatalf("Received exit code %d, expected 42", exitCode)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for exit code")
	}
}

// TestStreamWriterInterface verifies that streamWriter implements io.Writer
func TestStreamWriterInterface(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)
	defer exec.Close()

	// Verify io.Writer interface
	var _ io.Writer = exec.NewStdoutWriter()
	var _ io.Writer = exec.NewStderrWriter()
}

// TestExecAttachableDone tests the Done channel
func TestExecAttachableDone(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Verify done channel is open initially
	select {
	case <-exec.Done():
		t.Fatal("Done channel closed prematurely")
	default:
		// Expected
	}

	// Close exec
	if err := exec.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify done channel is now closed
	select {
	case <-exec.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Done channel not closed after Close()")
	}
}

// TestContainerStatus tests the ContainerStatus RPC method with valid state
func TestContainerStatus(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Create mock registry with test data
	exitCode := int32(0)
	finishedAt := time.Now()
	mockRegistry := &mockStateRegistry{
		states: map[string]*ContainerState{
			"test-container": {
				ContainerID: "test-container",
				Status:      "running",
				ExitCode:    nil,
				StartedAt:   time.Now().Add(-1 * time.Minute),
				FinishedAt:  nil,
				ResourceUsage: &ContainerResourceUsage{
					CPUPercent:   25.5,
					MemoryBytes:  1048576,
					MemoryLimit:  2097152,
					IOReadBytes:  1024,
					IOWriteBytes: 2048,
				},
			},
			"exited-container": {
				ContainerID: "exited-container",
				Status:      "exited",
				ExitCode:    &exitCode,
				StartedAt:   time.Now().Add(-5 * time.Minute),
				FinishedAt:  &finishedAt,
			},
		},
	}

	exec.SetStateRegistry(mockRegistry)

	// Test query for running container
	resp, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId:          "test-container",
		IncludeResourceUsage: true,
	})
	if err != nil {
		t.Fatalf("ContainerStatus failed: %v", err)
	}

	// Verify response
	if resp.ContainerId != "test-container" {
		t.Errorf("ContainerId = %q, want %q", resp.ContainerId, "test-container")
	}
	if resp.Status != "running" {
		t.Errorf("Status = %q, want %q", resp.Status, "running")
	}
	if resp.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", resp.ExitCode)
	}
	if resp.StartedAt == 0 {
		t.Error("StartedAt should not be zero")
	}
	if resp.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil", resp.FinishedAt)
	}
	if resp.ResourceUsage == nil {
		t.Fatal("ResourceUsage should not be nil")
	}
	if resp.ResourceUsage.CpuPercent != 25.5 {
		t.Errorf("CPUPercent = %f, want 25.5", resp.ResourceUsage.CpuPercent)
	}

	// Test query for exited container
	resp, err = exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId:          "exited-container",
		IncludeResourceUsage: false,
	})
	if err != nil {
		t.Fatalf("ContainerStatus failed: %v", err)
	}

	if resp.Status != "exited" {
		t.Errorf("Status = %q, want %q", resp.Status, "exited")
	}
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", resp.ExitCode)
	}
	if resp.FinishedAt == nil {
		t.Error("FinishedAt should not be nil")
	}
	if resp.ResourceUsage != nil {
		t.Error("ResourceUsage should be nil when not requested")
	}
}

// TestContainerStatusNotFound tests querying a non-existent container
func TestContainerStatusNotFound(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Create empty registry
	mockRegistry := &mockStateRegistry{
		states: map[string]*ContainerState{},
	}

	exec.SetStateRegistry(mockRegistry)

	// Query non-existent container
	_, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId: "non-existent",
	})

	// Should return NotFound error
	if err == nil {
		t.Fatal("Expected error for non-existent container")
	}

	// Check error code
	// The error should be a gRPC status error with NotFound code
	if !contains(err.Error(), "not found") {
		t.Errorf("Error message should contain 'not found', got: %v", err)
	}
}

// TestContainerStatusWithoutRegistry tests querying without a registry
func TestContainerStatusWithoutRegistry(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	// Don't set a registry

	// Query should fail
	_, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId: "test-container",
	})

	// Should return Unavailable error
	if err == nil {
		t.Fatal("Expected error when registry is not available")
	}

	if !contains(err.Error(), "not available") {
		t.Errorf("Error message should contain 'not available', got: %v", err)
	}
}

// TestContainerStatusEmptyID tests querying with empty container ID
func TestContainerStatusEmptyID(t *testing.T) {
	ctx := context.Background()
	exec := NewExecAttachable(ctx)

	mockRegistry := &mockStateRegistry{
		states: map[string]*ContainerState{},
	}
	exec.SetStateRegistry(mockRegistry)

	// Query with empty ID
	_, err := exec.ContainerStatus(ctx, &ContainerStatusRequest{
		ContainerId: "",
	})

	// Should return InvalidArgument error
	if err == nil {
		t.Fatal("Expected error for empty container ID")
	}

	if !contains(err.Error(), "cannot be empty") {
		t.Errorf("Error message should contain 'cannot be empty', got: %v", err)
	}
}

// contains is a helper to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
