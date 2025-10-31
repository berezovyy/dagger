package exec

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestStdinBasic(t *testing.T) {
	ctx := context.Background()

	// Create TTY instance
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Get stdin reader
	reader, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("GetStdinReader failed: %v", err)
	}

	// Write data
	testData := []byte("test input\n")
	if err := instance.WriteStdin(testData); err != nil {
		t.Fatalf("WriteStdin failed: %v", err)
	}

	// Read data
	buf := make([]byte, len(testData))
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if n != len(testData) {
		t.Errorf("Expected %d bytes, read %d", len(testData), n)
	}

	if string(buf) != string(testData) {
		t.Errorf("Expected %q, got %q", testData, buf)
	}
}

func TestStdinNonTTY(t *testing.T) {
	ctx := context.Background()

	// Create non-TTY instance
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", false)
	defer instance.Cleanup()

	// Attempt to write stdin should fail
	err := instance.WriteStdin([]byte("test"))
	if err == nil {
		t.Error("Expected error for non-TTY WriteStdin, got nil")
	}

	// Attempt to get reader should fail
	_, err = instance.GetStdinReader(ctx)
	if err == nil {
		t.Error("Expected error for non-TTY GetStdinReader, got nil")
	}
}

func TestStdinEOF(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	reader, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("GetStdinReader failed: %v", err)
	}

	// Write some data
	if err := instance.WriteStdin([]byte("data")); err != nil {
		t.Fatalf("WriteStdin failed: %v", err)
	}

	// Read data first (before sending EOF)
	buf := make([]byte, 100)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 4 {
		t.Errorf("Expected 4 bytes, got %d", n)
	}
	if string(buf[:n]) != "data" {
		t.Errorf("Expected %q, got %q", "data", buf[:n])
	}

	// Now send EOF (empty write)
	if err := instance.WriteStdin([]byte{}); err != nil {
		t.Fatalf("WriteStdin EOF failed: %v", err)
	}

	// Next read should return EOF
	_, err = reader.Read(buf)
	if err != io.EOF {
		t.Errorf("Expected EOF, got %v", err)
	}
}

func TestStdinBackpressure(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Don't create reader (so channel won't be drained)

	// Fill the channel
	for i := 0; i < StdinChannelSize; i++ {
		err := instance.WriteStdin([]byte("x"))
		if err != nil {
			t.Fatalf("WriteStdin %d failed: %v", i, err)
		}
	}

	// Next write should fail with backpressure
	err := instance.WriteStdin([]byte("y"))
	if err == nil {
		t.Error("Expected backpressure error, got nil")
	}
	if err.Error() != "stdin buffer full (backpressure)" {
		t.Errorf("Expected backpressure error, got: %v", err)
	}
}

func TestStdinMaxChunkSize(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Create reader to drain channel
	_, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("GetStdinReader failed: %v", err)
	}

	// Write within limit should succeed
	smallData := make([]byte, StdinMaxChunkSize)
	if err := instance.WriteStdin(smallData); err != nil {
		t.Errorf("WriteStdin with max size failed: %v", err)
	}

	// Write exceeding limit should fail
	largeData := make([]byte, StdinMaxChunkSize+1)
	err = instance.WriteStdin(largeData)
	if err == nil {
		t.Error("Expected error for oversized chunk, got nil")
	}
}

func TestStdinMultipleWrites(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	reader, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("GetStdinReader failed: %v", err)
	}

	// Write multiple chunks
	writes := []string{"hello ", "world", "!\n"}
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for _, write := range writes {
			if err := instance.WriteStdin([]byte(write)); err != nil {
				t.Errorf("WriteStdin failed: %v", err)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Read all data
	buf := make([]byte, 100)
	totalRead := 0

	for totalRead < len("hello world!\n") {
		n, err := reader.Read(buf[totalRead:])
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		totalRead += n
	}

	expected := "hello world!\n"
	if string(buf[:totalRead]) != expected {
		t.Errorf("Expected %q, got %q", expected, buf[:totalRead])
	}

	wg.Wait()
}

func TestStdinContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	reader, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("GetStdinReader failed: %v", err)
	}

	// Cancel context
	cancel()

	// Wait a bit for goroutine to process cancellation
	time.Sleep(50 * time.Millisecond)

	// Read should fail or return error
	buf := make([]byte, 10)
	_, err = reader.Read(buf)
	if err == nil {
		t.Error("Expected error after context cancellation, got nil")
	}
}

func TestStdinDoubleReaderCreation(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Create first reader
	_, err := instance.GetStdinReader(ctx)
	if err != nil {
		t.Fatalf("First GetStdinReader failed: %v", err)
	}

	// Second reader creation should fail
	_, err = instance.GetStdinReader(ctx)
	if err == nil {
		t.Error("Expected error for double reader creation, got nil")
	}
}
