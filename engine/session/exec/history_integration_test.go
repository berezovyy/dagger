package exec

import (
	"context"
	"testing"
	"time"
)

func TestOutputHistoryIntegration(t *testing.T) {
	ctx := context.Background()

	// Create TTY instance
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	history := instance.GetOutputHistory()
	if history == nil {
		t.Fatal("History should not be nil for TTY session")
	}

	// Get writers
	stdoutWriter := instance.GetStdoutWriter()
	stderrWriter := instance.GetStderrWriter()

	// Write some data
	stdoutWriter.Write([]byte("stdout line 1\n"))
	stderrWriter.Write([]byte("stderr line 1\n"))
	stdoutWriter.Write([]byte("stdout line 2\n"))

	// Give it a moment to process
	time.Sleep(10 * time.Millisecond)

	// Check history
	currentSeq := history.GetCurrentSequence()
	if currentSeq != 3 {
		t.Errorf("Expected 3 sequences, got %d", currentSeq)
	}

	// Verify history contains data
	chunks := history.GetHistoryRange(1, 3)
	if len(chunks) != 3 {
		t.Errorf("Expected 3 chunks in history, got %d", len(chunks))
	}

	// Verify stdout chunks
	stdoutChunks := history.GetStdoutRange(1, 3)
	if len(stdoutChunks) != 2 {
		t.Errorf("Expected 2 stdout chunks, got %d", len(stdoutChunks))
	}

	// Verify stderr chunks
	stderrChunks := history.GetStderrRange(1, 3)
	if len(stderrChunks) != 1 {
		t.Errorf("Expected 1 stderr chunk, got %d", len(stderrChunks))
	}
}

func TestOutputHistoryIntegrationNonTTY(t *testing.T) {
	ctx := context.Background()

	// Create non-TTY instance
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", false)
	defer instance.Cleanup()

	history := instance.GetOutputHistory()
	if history != nil {
		t.Error("History should be nil for non-TTY session")
	}

	// Get writers (should use basic streamWriter)
	stdoutWriter := instance.GetStdoutWriter()
	stderrWriter := instance.GetStderrWriter()

	// Write data (should not crash)
	n, err := stdoutWriter.Write([]byte("stdout\n"))
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n != 7 {
		t.Errorf("Expected 7 bytes written, got %d", n)
	}

	n, err = stderrWriter.Write([]byte("stderr\n"))
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n != 7 {
		t.Errorf("Expected 7 bytes written, got %d", n)
	}
}

func TestOutputHistoryStreamingAndHistory(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Get channels
	stdoutChan := instance.stdoutChan

	// Get writers
	stdoutWriter := instance.GetStdoutWriter()

	// Write data
	testData := []byte("test output\n")
	stdoutWriter.Write(testData)

	// Should receive on channel (live streaming)
	select {
	case data := <-stdoutChan:
		if string(data) != string(testData) {
			t.Errorf("Expected %q from channel, got %q", testData, data)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for data on channel")
	}

	// Should also be in history
	time.Sleep(10 * time.Millisecond)
	chunks := instance.GetOutputHistory().GetStdoutRange(1, 1)
	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk in history, got %d", len(chunks))
	}

	if string(chunks[0].Data) != string(testData) {
		t.Errorf("Expected %q in history, got %q", testData, chunks[0].Data)
	}
}

func TestOutputHistoryConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	stdoutWriter := instance.GetStdoutWriter()
	stderrWriter := instance.GetStderrWriter()

	// Drain channels
	go func() {
		for {
			select {
			case <-instance.stdoutChan:
			case <-instance.stderrChan:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Concurrent writes
	const numGoroutines = 10
	const writesPerGoroutine = 50

	done := make(chan bool, numGoroutines*2)

	for i := 0; i < numGoroutines; i++ {
		// Stdout writers
		go func() {
			for j := 0; j < writesPerGoroutine; j++ {
				stdoutWriter.Write([]byte("stdout\n"))
			}
			done <- true
		}()

		// Stderr writers
		go func() {
			for j := 0; j < writesPerGoroutine; j++ {
				stderrWriter.Write([]byte("stderr\n"))
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// Check history has correct number of sequences
	time.Sleep(50 * time.Millisecond)
	expectedSeqs := uint64(numGoroutines * writesPerGoroutine * 2)
	actualSeqs := instance.GetOutputHistory().GetCurrentSequence()

	if actualSeqs != expectedSeqs {
		t.Errorf("Expected %d sequences, got %d", expectedSeqs, actualSeqs)
	}
}

func TestOutputHistoryMemorySafety(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	stdoutWriter := instance.GetStdoutWriter()

	// Write data
	original := []byte("original data")
	stdoutWriter.Write(original)

	// Modify original buffer
	original[0] = 'X'

	// History should still have original data (copy was made)
	time.Sleep(10 * time.Millisecond)
	chunks := instance.GetOutputHistory().GetStdoutRange(1, 1)
	if len(chunks) != 1 {
		t.Fatal("Expected 1 chunk")
	}

	if chunks[0].Data[0] == 'X' {
		t.Error("History was modified (no copy was made)")
	}

	if chunks[0].Data[0] != 'o' {
		t.Errorf("Expected 'o', got %c", chunks[0].Data[0])
	}
}
