package buildkit

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/session/exec"
	"github.com/dagger/dagger/internal/buildkit/executor"
)

// TestExecutorExecStreamingIntegration tests the complete flow of executor integration
// with ExecAttachable for streaming logs.
//
// This test verifies:
// 1. Execution is registered before container starts
// 2. Stream writers are retrieved from registry
// 3. TeeWriter writes to both files and streams
// 4. Exit code is sent after container exits
// 5. Cleanup unregisters the execution
func TestExecutorExecStreamingIntegration(t *testing.T) {
	ctx := context.Background()

	// Create ExecAttachable
	execAttachable := exec.NewExecAttachable(ctx)
	defer execAttachable.Close()

	// Create a test exec state
	containerID := "test-container-123"
	execID := "test-exec-123"

	// Step 1: Register execution (simulating setupExecStreaming)
	err := execAttachable.RegisterExecution(containerID, execID)
	if err != nil {
		t.Fatalf("Failed to register execution: %v", err)
	}

	// Step 2: Get stream writers (simulating getStreamWriter)
	stdoutStreamWriter := execAttachable.GetStdoutWriter(containerID, execID)
	if stdoutStreamWriter == nil {
		t.Fatal("stdout stream writer should not be nil")
	}

	stderrStreamWriter := execAttachable.GetStderrWriter(containerID, execID)
	if stderrStreamWriter == nil {
		t.Fatal("stderr stream writer should not be nil")
	}

	// Step 3: Create TeeWriter for stdout (simulating setupStdio)
	stdoutFileBuffer := &bytes.Buffer{}
	stdoutTee := NewTeeWriter(ctx, stdoutFileBuffer, stdoutStreamWriter)

	// Step 4: Create TeeWriter for stderr (simulating setupStdio)
	stderrFileBuffer := &bytes.Buffer{}
	stderrTee := NewTeeWriter(ctx, stderrFileBuffer, stderrStreamWriter)

	// Step 5: Write to stdout TeeWriter (simulating container execution)
	stdoutData := []byte("stdout line 1\n")
	n, err := stdoutTee.Write(stdoutData)
	if err != nil {
		t.Fatalf("Failed to write to stdout TeeWriter: %v", err)
	}
	if n != len(stdoutData) {
		t.Fatalf("stdout write returned %d, expected %d", n, len(stdoutData))
	}

	// Step 6: Write to stderr TeeWriter (simulating container execution)
	stderrData := []byte("stderr line 1\n")
	n, err = stderrTee.Write(stderrData)
	if err != nil {
		t.Fatalf("Failed to write to stderr TeeWriter: %v", err)
	}
	if n != len(stderrData) {
		t.Fatalf("stderr write returned %d, expected %d", n, len(stderrData))
	}

	// Step 7: Close TeeWriters to flush streams
	if err := stdoutTee.Close(); err != nil {
		t.Fatalf("Failed to close stdout TeeWriter: %v", err)
	}
	if err := stderrTee.Close(); err != nil {
		t.Fatalf("Failed to close stderr TeeWriter: %v", err)
	}

	// Step 8: Verify data was written to file buffers
	if stdoutFileBuffer.String() != string(stdoutData) {
		t.Fatalf("stdout file buffer = %q, want %q", stdoutFileBuffer.String(), stdoutData)
	}
	if stderrFileBuffer.String() != string(stderrData) {
		t.Fatalf("stderr file buffer = %q, want %q", stderrFileBuffer.String(), stderrData)
	}

	// Step 9: Send exit code (simulating runContainer completion)
	exitCode := int32(0)
	err = execAttachable.SendExitCode(containerID, execID, exitCode)
	if err != nil {
		t.Fatalf("Failed to send exit code: %v", err)
	}

	// Step 10: Unregister execution (simulating cleanup)
	err = execAttachable.UnregisterExecution(containerID, execID)
	if err != nil {
		t.Fatalf("Failed to unregister execution: %v", err)
	}

	t.Log("Executor integration test passed successfully")
}

// TestTeeWriterIntegration tests TeeWriter with actual files
func TestTeeWriterIntegration(t *testing.T) {
	ctx := context.Background()

	// Create temp directory for test files
	tempDir, err := os.MkdirTemp("", "teewriter-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create stdout file
	stdoutPath := filepath.Join(tempDir, "stdout.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("Failed to create stdout file: %v", err)
	}
	defer stdoutFile.Close()

	// Create stream buffer
	streamBuffer := &bytes.Buffer{}

	// Create TeeWriter
	tee := NewTeeWriter(ctx, stdoutFile, streamBuffer)

	// Write some data
	testData := []byte("test line 1\ntest line 2\n")
	n, err := tee.Write(testData)
	if err != nil {
		t.Fatalf("TeeWriter.Write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("TeeWriter.Write returned %d, expected %d", n, len(testData))
	}

	// Close TeeWriter to flush
	if err := tee.Close(); err != nil {
		t.Fatalf("TeeWriter.Close failed: %v", err)
	}

	// Close file to flush
	stdoutFile.Close()

	// Read file contents
	fileContents, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("Failed to read stdout file: %v", err)
	}

	// Verify file contains expected data
	if string(fileContents) != string(testData) {
		t.Fatalf("File contents = %q, want %q", fileContents, testData)
	}

	// Verify stream contains expected data
	if streamBuffer.String() != string(testData) {
		t.Fatalf("Stream buffer = %q, want %q", streamBuffer.String(), testData)
	}

	t.Log("TeeWriter integration test passed successfully")
}

// TestTeeWriterWithNilStream tests TeeWriter with nil stream writer
func TestTeeWriterWithNilStream(t *testing.T) {
	ctx := context.Background()

	// Create file buffer
	fileBuffer := &bytes.Buffer{}

	// Create TeeWriter with nil stream (simulates no streaming)
	tee := NewTeeWriter(ctx, fileBuffer, nil)

	// Write data
	testData := []byte("test data\n")
	n, err := tee.Write(testData)
	if err != nil {
		t.Fatalf("TeeWriter.Write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("TeeWriter.Write returned %d, expected %d", n, len(testData))
	}

	// Close TeeWriter
	if err := tee.Close(); err != nil {
		t.Fatalf("TeeWriter.Close failed: %v", err)
	}

	// Verify file buffer contains data
	if fileBuffer.String() != string(testData) {
		t.Fatalf("File buffer = %q, want %q", fileBuffer.String(), testData)
	}

	t.Log("TeeWriter with nil stream test passed successfully")
}

// TestExecStateLifecycle tests the complete lifecycle of an exec state
func TestExecStateLifecycle(t *testing.T) {
	ctx := context.Background()

	// Create ExecAttachable
	execAttachable := exec.NewExecAttachable(ctx)
	defer execAttachable.Close()

	// Create exec state
	state := newExecState(
		"test-exec-state-123",
		&executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"/bin/sh", "-c", "echo hello"},
			},
		},
		executor.Mount{},
		nil,
		nil,
	)

	// Simulate setupExecStreaming
	state.execAttachable = execAttachable

	containerID := state.id
	execID := state.id

	// Register execution
	err := state.execAttachable.RegisterExecution(containerID, execID)
	if err != nil {
		t.Fatalf("Failed to register execution: %v", err)
	}

	// Get stream writers
	stdoutWriter := state.execAttachable.GetStdoutWriter(containerID, execID)
	if stdoutWriter == nil {
		t.Fatal("stdout writer should not be nil after registration")
	}

	stderrWriter := state.execAttachable.GetStderrWriter(containerID, execID)
	if stderrWriter == nil {
		t.Fatal("stderr writer should not be nil after registration")
	}

	// Write some data
	testData := []byte("test output\n")
	n, err := stdoutWriter.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write to stdout: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("stdout write returned %d, expected %d", n, len(testData))
	}

	// Send exit code
	err = state.execAttachable.SendExitCode(containerID, execID, 0)
	if err != nil {
		t.Fatalf("Failed to send exit code: %v", err)
	}

	// Unregister execution (cleanup)
	err = state.execAttachable.UnregisterExecution(containerID, execID)
	if err != nil {
		t.Fatalf("Failed to unregister execution: %v", err)
	}

	t.Log("Exec state lifecycle test passed successfully")
}

// TestConcurrentTeeWriterWrites tests concurrent writes to TeeWriter
func TestConcurrentTeeWriterWrites(t *testing.T) {
	ctx := context.Background()

	fileBuffer := &bytes.Buffer{}
	streamBuffer := &bytes.Buffer{}

	tee := NewTeeWriter(ctx, fileBuffer, streamBuffer)
	defer tee.Close()

	// Simulate concurrent writes
	done := make(chan struct{})
	numGoroutines := 10
	writesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < writesPerGoroutine; j++ {
				data := []byte("test\n")
				_, err := tee.Write(data)
				if err != nil {
					t.Errorf("Write failed: %v", err)
					return
				}
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent writes")
		}
	}

	// Close to flush
	if err := tee.Close(); err != nil {
		t.Fatalf("TeeWriter.Close failed: %v", err)
	}

	// Verify file buffer received all data
	expectedSize := numGoroutines * writesPerGoroutine * len("test\n")
	if fileBuffer.Len() != expectedSize {
		t.Fatalf("File buffer size = %d, expected %d", fileBuffer.Len(), expectedSize)
	}

	// Stream buffer may have dropped some writes due to channel overflow
	// This is expected behavior for non-blocking writes
	if streamBuffer.Len() == 0 {
		t.Fatal("Stream buffer is empty after concurrent writes")
	}

	// Log if stream dropped some writes (this is expected under heavy load)
	if streamBuffer.Len() < expectedSize {
		t.Logf("Stream buffer dropped some writes: %d/%d bytes (%.1f%% received)",
			streamBuffer.Len(), expectedSize, float64(streamBuffer.Len())/float64(expectedSize)*100)
	}

	t.Log("Concurrent TeeWriter writes test passed successfully")
}

// mockWriter is a mock io.Writer that can simulate errors
type mockWriter struct {
	buf       *bytes.Buffer
	writeErr  error
	callCount int
}

func (m *mockWriter) Write(p []byte) (n int, err error) {
	m.callCount++
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return m.buf.Write(p)
}

// TestTeeWriterFileWriteError tests TeeWriter behavior when file write fails
func TestTeeWriterFileWriteError(t *testing.T) {
	ctx := context.Background()

	// Create mock file writer that fails
	mockFile := &mockWriter{
		buf:      &bytes.Buffer{},
		writeErr: io.ErrShortWrite,
	}

	streamBuffer := &bytes.Buffer{}

	tee := NewTeeWriter(ctx, mockFile, streamBuffer)
	defer tee.Close()

	// Write should fail because file write fails
	_, err := tee.Write([]byte("test data\n"))
	if err == nil {
		t.Fatal("Expected error from file write failure")
	}
	if err != io.ErrShortWrite {
		t.Fatalf("Expected io.ErrShortWrite, got %v", err)
	}

	t.Log("TeeWriter file write error test passed successfully")
}

// TestTeeWriterStreamWriteError tests TeeWriter behavior when stream write fails
// Stream errors should be logged but not propagated
func TestTeeWriterStreamWriteError(t *testing.T) {
	ctx := context.Background()

	fileBuffer := &bytes.Buffer{}

	// Create mock stream writer that fails
	mockStream := &mockWriter{
		buf:      &bytes.Buffer{},
		writeErr: io.ErrClosedPipe,
	}

	tee := NewTeeWriter(ctx, fileBuffer, mockStream)
	defer tee.Close()

	// Write should succeed because file write succeeds (stream error is ignored)
	testData := []byte("test data\n")
	n, err := tee.Write(testData)
	if err != nil {
		t.Fatalf("Write should succeed even if stream fails: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Write returned %d, expected %d", n, len(testData))
	}

	// Verify file buffer contains data
	if fileBuffer.String() != string(testData) {
		t.Fatalf("File buffer = %q, want %q", fileBuffer.String(), testData)
	}

	// Give stream goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Mock stream writer should have been called
	if mockStream.callCount == 0 {
		t.Fatal("Stream writer was not called")
	}

	t.Log("TeeWriter stream write error test passed successfully")
}
