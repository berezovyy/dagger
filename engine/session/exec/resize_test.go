package exec

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestResizeBasic(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Send resize
	err := instance.SendResize(80, 24)
	if err != nil {
		t.Fatalf("SendResize failed: %v", err)
	}

	// Get current size
	size := instance.GetCurrentSize()
	if size == nil {
		t.Fatal("GetCurrentSize returned nil")
	}

	if size.Cols != 80 || size.Rows != 24 {
		t.Errorf("Expected 80x24, got %dx%d", size.Cols, size.Rows)
	}

	// Receive from channel
	resizeChan := instance.GetResizeChannel()
	select {
	case receivedSize := <-resizeChan:
		if receivedSize.Cols != 80 || receivedSize.Rows != 24 {
			t.Errorf("Expected 80x24 from channel, got %dx%d", receivedSize.Cols, receivedSize.Rows)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for resize event")
	}
}

func TestResizeNonTTY(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", false)
	defer instance.Cleanup()

	// Send resize should fail
	err := instance.SendResize(80, 24)
	if err == nil {
		t.Error("Expected error for non-TTY resize, got nil")
	}

	// GetCurrentSize should return nil
	size := instance.GetCurrentSize()
	if size != nil {
		t.Errorf("Expected nil size for non-TTY, got %+v", size)
	}
}

func TestResizeInvalidSize(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Zero width
	err := instance.SendResize(0, 24)
	if err == nil {
		t.Error("Expected error for zero width, got nil")
	}

	// Zero height
	err = instance.SendResize(80, 0)
	if err == nil {
		t.Error("Expected error for zero height, got nil")
	}
}

func TestResizeDebouncing(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	resizeChan := instance.GetResizeChannel()

	// Send rapid resize events (within 50ms debounce window)
	// All events sent within 50ms total, so they should all be debounced together
	for i := 1; i <= 10; i++ {
		err := instance.SendResize(uint32(70+i), uint32(20+i))
		if err != nil {
			t.Fatalf("SendResize %d failed: %v", i, err)
		}
		time.Sleep(4 * time.Millisecond) // Total: 40ms for all 10 events
	}

	// Collect all events received
	var receivedEvents []WinSize

	// First event should arrive immediately (the initial one)
	select {
	case size := <-resizeChan:
		receivedEvents = append(receivedEvents, size)
		t.Logf("Received immediate event: %dx%d", size.Cols, size.Rows)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for immediate resize event")
	}

	// Wait for debounce window plus buffer time for debounced event
	time.Sleep(150 * time.Millisecond)

	// Collect any additional events
	for {
		select {
		case size := <-resizeChan:
			receivedEvents = append(receivedEvents, size)
			t.Logf("Received event: %dx%d", size.Cols, size.Rows)
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}

done:
	// Should have received 2 events total:
	// 1. First immediate event
	// 2. Final debounced event
	// Not all 10 intermediate events
	if len(receivedEvents) > 3 {
		t.Errorf("Expected debouncing to reduce events, but got %d events", len(receivedEvents))
	}

	// Last event should be the final resize (80x30)
	if len(receivedEvents) > 0 {
		lastEvent := receivedEvents[len(receivedEvents)-1]
		if lastEvent.Cols != 80 || lastEvent.Rows != 30 {
			t.Errorf("Expected final event 80x30, got %dx%d", lastEvent.Cols, lastEvent.Rows)
		}
	}

	t.Logf("Debounced 10 rapid resize events into %d events", len(receivedEvents))
}

func TestResizeCoalescing(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	resizeChan := instance.GetResizeChannel()

	// Send many rapid resizes
	const numResizes = 100
	for i := 1; i <= numResizes; i++ {
		err := instance.SendResize(uint32(80+i), uint32(24+i))
		if err != nil {
			t.Fatalf("SendResize %d failed: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}

	// Collect all received events (with timeout)
	received := []WinSize{}
	timeout := time.After(2 * time.Second)

	for {
		select {
		case size := <-resizeChan:
			received = append(received, size)
		case <-timeout:
			goto done
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}

done:
	// Should have received significantly fewer than 100 events
	if len(received) >= numResizes {
		t.Errorf("Expected coalescing, but received %d out of %d events", len(received), numResizes)
	}

	t.Logf("Coalesced %d resize events into %d", numResizes, len(received))

	// Last event should be the final resize
	if len(received) > 0 {
		lastEvent := received[len(received)-1]
		expectedCols := uint32(80 + numResizes)
		expectedRows := uint32(24 + numResizes)

		if lastEvent.Cols != expectedCols || lastEvent.Rows != expectedRows {
			t.Errorf("Expected final event %dx%d, got %dx%d",
				expectedCols, expectedRows, lastEvent.Cols, lastEvent.Rows)
		}
	}
}

func TestResizeChannelOverflow(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Don't drain channel

	// Fill channel beyond capacity (10)
	// Due to overflow handling, should still succeed by draining old events
	for i := 1; i <= 20; i++ {
		err := instance.SendResize(uint32(70+i), uint32(20+i))
		if err != nil {
			t.Errorf("SendResize %d failed (expected success with drain): %v", i, err)
		}
	}
}

func TestResizeConcurrent(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	resizeChan := instance.GetResizeChannel()

	// Concurrent senders
	const numGoroutines = 5
	const resizesPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < resizesPerGoroutine; i++ {
				instance.SendResize(uint32(80+id), uint32(24+id))
				time.Sleep(time.Millisecond)
			}
		}(g)
	}

	// Concurrent receiver with synchronized counter
	var receivedMu sync.Mutex
	received := 0
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-resizeChan:
				receivedMu.Lock()
				received++
				receivedMu.Unlock()
			case <-done:
				return
			}
		}
	}()

	wg.Wait()
	time.Sleep(200 * time.Millisecond) // Wait for debouncer
	close(done)

	// Should have received some events (coalesced)
	receivedMu.Lock()
	finalReceived := received
	receivedMu.Unlock()

	if finalReceived == 0 {
		t.Error("Expected to receive some resize events, got 0")
	}

	t.Logf("Received %d resize events from %d concurrent senders",
		finalReceived, numGoroutines*resizesPerGoroutine)
}

func TestResizeGetCurrentSize(t *testing.T) {
	ctx := context.Background()
	instance := NewExecInstance(ctx, "container1", "exec1", "session1", true)
	defer instance.Cleanup()

	// Initial size (default 24x80 from TTYState)
	size := instance.GetCurrentSize()
	if size == nil {
		t.Fatal("Initial size should not be nil")
	}

	// Send resize
	instance.SendResize(100, 50)

	// Current size should update immediately
	size = instance.GetCurrentSize()
	if size.Cols != 100 || size.Rows != 50 {
		t.Errorf("Expected 100x50, got %dx%d", size.Cols, size.Rows)
	}

	// Send another resize rapidly
	instance.SendResize(120, 60)

	// Current size should still update immediately even if debounced
	size = instance.GetCurrentSize()
	if size.Cols != 120 || size.Rows != 60 {
		t.Errorf("Expected 120x60, got %dx%d", size.Cols, size.Rows)
	}
}
