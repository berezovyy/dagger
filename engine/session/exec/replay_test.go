package exec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReplayOutputHistory(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create instance with TTY and output history
	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	history := instance.GetOutputHistory()
	if history == nil {
		t.Fatal("Output history should not be nil for TTY session")
	}

	// Add some output to history
	history.AppendStdout([]byte("line 1\n"))
	history.AppendStderr([]byte("error 1\n"))
	history.AppendStdout([]byte("line 2\n"))
	history.AppendStderr([]byte("error 2\n"))
	history.AppendStdout([]byte("line 3\n"))

	// Create mock stream
	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 20),
	}

	// Create attachable
	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Replay from sequence 1
	err = attachable.replayOutputHistory(ctx, stream, instance, 1)
	if err != nil {
		t.Fatalf("replayOutputHistory failed: %v", err)
	}

	// Verify we received all 5 chunks
	// Note: Using Stdout/Stderr messages until OutputChunk proto is regenerated
	receivedChunks := 0
	timeout := time.After(time.Second)

	for receivedChunks < 5 {
		select {
		case resp := <-stream.sendChan:
			// Check if it's stdout or stderr
			if resp.GetStdout() != nil || resp.GetStderr() != nil {
				receivedChunks++
			} else {
				t.Errorf("Expected Stdout or Stderr message, got: %T", resp.Msg)
			}

		case <-timeout:
			t.Fatalf("Timeout waiting for replay chunks, got %d/5", receivedChunks)
		}
	}

	t.Logf("Successfully received %d replay chunks", receivedChunks)
}

func TestReplayFromMiddle(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	history := instance.GetOutputHistory()

	// Add 10 chunks
	for i := 1; i <= 10; i++ {
		history.AppendStdout([]byte("line\n"))
	}

	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 20),
	}

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Replay from sequence 6 (should get 6-10)
	err = attachable.replayOutputHistory(ctx, stream, instance, 6)
	if err != nil {
		t.Fatalf("replayOutputHistory failed: %v", err)
	}

	// Should receive 5 chunks (6-10)
	receivedChunks := 0
	timeout := time.After(time.Second)

	for receivedChunks < 5 {
		select {
		case resp := <-stream.sendChan:
			// Check if it's stdout or stderr
			if resp.GetStdout() != nil || resp.GetStderr() != nil {
				receivedChunks++
			}

		case <-timeout:
			t.Fatalf("Timeout, got %d/5 chunks", receivedChunks)
		}
	}
}

func TestReplayWithGap(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create instance with small buffer (10 capacity)
	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Replace history with small capacity
	instance.outputHistory = NewOutputHistoryWithCapacity(10)
	history := instance.GetOutputHistory()

	// Add 20 chunks (will wrap buffer, oldest 10 will be lost)
	for i := 1; i <= 20; i++ {
		history.AppendStdout([]byte("line\n"))
	}

	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 30),
	}

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Try to replay from sequence 1 (but buffer only has 11-20)
	err = attachable.replayOutputHistory(ctx, stream, instance, 1)
	if err != nil {
		t.Fatalf("replayOutputHistory failed: %v", err)
	}

	// Should receive available chunks (oldest in buffer)
	receivedChunks := 0
	timeout := time.After(time.Second)

	for {
		select {
		case resp := <-stream.sendChan:
			// Check if it's stdout or stderr
			if resp.GetStdout() != nil || resp.GetStderr() != nil {
				receivedChunks++
			}

		case <-timeout:
			goto done
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}

done:
	if receivedChunks == 0 {
		t.Fatal("Expected to receive some chunks")
	}

	t.Logf("Received %d chunks (gap handled)", receivedChunks)
}

func TestReplayNoHistory(t *testing.T) {
	ctx := context.Background()
	registry := NewExecSessionRegistry(ctx)
	defer registry.Close()

	// Create non-TTY instance (no history)
	instance, err := registry.Register("container1", "exec1", "", false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10),
	}

	attachable := &ExecAttachable{
		rootCtx:  ctx,
		registry: registry,
	}

	// Replay should succeed but send nothing
	err = attachable.replayOutputHistory(ctx, stream, instance, 1)
	if err != nil {
		t.Fatalf("replayOutputHistory should succeed for non-TTY: %v", err)
	}

	// Should not receive any chunks
	select {
	case <-stream.sendChan:
		t.Error("Should not receive chunks for non-TTY session")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestReplayContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := NewExecSessionRegistry(context.Background())
	defer registry.Close()

	instance, err := registry.Register("container1", "exec1", "", true)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	history := instance.GetOutputHistory()

	// Add many chunks
	for i := 0; i < 1000; i++ {
		history.AppendStdout([]byte("line\n"))
	}

	stream := &mockExecSessionServer{
		ctx:      ctx,
		recvChan: make(chan *SessionRequest, 10),
		sendChan: make(chan *SessionResponse, 10), // Small buffer
	}

	attachable := &ExecAttachable{
		rootCtx:  context.Background(),
		registry: registry,
	}

	// Start replay in goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- attachable.replayOutputHistory(ctx, stream, instance, 1)
	}()

	// Cancel context after a bit
	time.Sleep(10 * time.Millisecond)
	cancel()

	// Should receive context error (may be wrapped)
	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error due to context cancellation")
		} else if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Expected context canceled error, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for replay to stop")
	}
}
