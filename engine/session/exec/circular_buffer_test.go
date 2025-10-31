package exec

import (
	"sync"
	"testing"
	"time"
)

func TestCircularBufferBasic(t *testing.T) {
	cb := NewCircularBuffer(5)

	if cb.GetSize() != 0 {
		t.Errorf("New buffer should be empty, got size %d", cb.GetSize())
	}

	if cb.GetCapacity() != 5 {
		t.Errorf("Expected capacity 5, got %d", cb.GetCapacity())
	}

	// Append a chunk
	chunk := OutputChunk{
		Sequence:  1,
		Timestamp: time.Now(),
		Data:      []byte("test"),
		Stream:    StreamStdout,
	}
	cb.Append(chunk)

	if cb.GetSize() != 1 {
		t.Errorf("Expected size 1 after append, got %d", cb.GetSize())
	}
}

func TestCircularBufferFillToCapacity(t *testing.T) {
	capacity := 5
	cb := NewCircularBuffer(capacity)

	// Fill buffer to capacity
	for i := 1; i <= capacity; i++ {
		chunk := OutputChunk{
			Sequence:  uint64(i),
			Timestamp: time.Now(),
			Data:      []byte("data"),
			Stream:    StreamStdout,
		}
		cb.Append(chunk)
	}

	if cb.GetSize() != capacity {
		t.Errorf("Expected size %d, got %d", capacity, cb.GetSize())
	}

	if cb.GetOldestSequence() != 1 {
		t.Errorf("Expected oldest sequence 1, got %d", cb.GetOldestSequence())
	}

	if cb.GetNewestSequence() != uint64(capacity) {
		t.Errorf("Expected newest sequence %d, got %d", capacity, cb.GetNewestSequence())
	}
}

func TestCircularBufferWrapAround(t *testing.T) {
	capacity := 5
	cb := NewCircularBuffer(capacity)

	// Fill buffer and then add more (causing wrap-around)
	totalChunks := 10
	for i := 1; i <= totalChunks; i++ {
		chunk := OutputChunk{
			Sequence:  uint64(i),
			Timestamp: time.Now(),
			Data:      []byte("data"),
			Stream:    StreamStdout,
		}
		cb.Append(chunk)
	}

	// Buffer should still be at capacity
	if cb.GetSize() != capacity {
		t.Errorf("Expected size %d after wrap, got %d", capacity, cb.GetSize())
	}

	// Oldest should be chunk 6 (chunks 1-5 were overwritten)
	if cb.GetOldestSequence() != 6 {
		t.Errorf("Expected oldest sequence 6 after wrap, got %d", cb.GetOldestSequence())
	}

	// Newest should be chunk 10
	if cb.GetNewestSequence() != 10 {
		t.Errorf("Expected newest sequence 10, got %d", cb.GetNewestSequence())
	}

	// Verify GetRange returns only available chunks
	chunks := cb.GetRange(1, 10)
	if len(chunks) != capacity {
		t.Errorf("Expected %d chunks, got %d", capacity, len(chunks))
	}

	// Verify sequence numbers are correct (should be 6-10)
	for i, chunk := range chunks {
		expectedSeq := uint64(6 + i)
		if chunk.Sequence != expectedSeq {
			t.Errorf("Expected sequence %d at index %d, got %d", expectedSeq, i, chunk.Sequence)
		}
	}
}

func TestCircularBufferGetRange(t *testing.T) {
	cb := NewCircularBuffer(10)

	// Add chunks with sequences 1-10
	for i := 1; i <= 10; i++ {
		chunk := OutputChunk{
			Sequence:  uint64(i),
			Timestamp: time.Now(),
			Data:      []byte("data"),
			Stream:    StreamStdout,
		}
		cb.Append(chunk)
	}

	// Test various ranges
	tests := []struct {
		name     string
		fromSeq  uint64
		toSeq    uint64
		expected []uint64
	}{
		{"All chunks", 1, 10, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{"Middle range", 3, 7, []uint64{3, 4, 5, 6, 7}},
		{"Single chunk", 5, 5, []uint64{5}},
		{"Before buffer", 0, 0, []uint64{}},
		{"After buffer", 11, 15, []uint64{}},
		{"Partial overlap start", 0, 3, []uint64{1, 2, 3}},
		{"Partial overlap end", 8, 15, []uint64{8, 9, 10}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := cb.GetRange(tt.fromSeq, tt.toSeq)

			if len(chunks) != len(tt.expected) {
				t.Errorf("Expected %d chunks, got %d", len(tt.expected), len(chunks))
				return
			}

			for i, chunk := range chunks {
				if chunk.Sequence != tt.expected[i] {
					t.Errorf("Expected sequence %d at index %d, got %d", tt.expected[i], i, chunk.Sequence)
				}
			}
		})
	}
}

func TestCircularBufferEmptyGetRange(t *testing.T) {
	cb := NewCircularBuffer(10)

	chunks := cb.GetRange(1, 10)
	if chunks != nil {
		t.Errorf("Expected nil for empty buffer, got %d chunks", len(chunks))
	}

	if cb.GetOldestSequence() != 0 {
		t.Errorf("Expected oldest sequence 0 for empty buffer, got %d", cb.GetOldestSequence())
	}

	if cb.GetNewestSequence() != 0 {
		t.Errorf("Expected newest sequence 0 for empty buffer, got %d", cb.GetNewestSequence())
	}
}

func TestCircularBufferConcurrentAppend(t *testing.T) {
	cb := NewCircularBuffer(1000)

	// Concurrent append from multiple goroutines
	const numGoroutines = 10
	const chunksPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()

			for i := 0; i < chunksPerGoroutine; i++ {
				chunk := OutputChunk{
					Sequence:  uint64(goroutineID*chunksPerGoroutine + i + 1),
					Timestamp: time.Now(),
					Data:      []byte("data"),
					Stream:    StreamStdout,
				}
				cb.Append(chunk)
			}
		}(g)
	}

	wg.Wait()

	// Should have all 1000 chunks (exactly at capacity)
	if cb.GetSize() != 1000 {
		t.Errorf("Expected size 1000, got %d", cb.GetSize())
	}
}

func TestCircularBufferConcurrentReadWrite(t *testing.T) {
	cb := NewCircularBuffer(100)

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 1; i <= 200; i++ {
			chunk := OutputChunk{
				Sequence:  uint64(i),
				Timestamp: time.Now(),
				Data:      []byte("data"),
				Stream:    StreamStdout,
			}
			cb.Append(chunk)
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// Reader goroutines
	const numReaders = 5
	var wg sync.WaitGroup
	wg.Add(numReaders)

	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()

			for {
				select {
				case <-done:
					return
				default:
					cb.GetRange(1, 200)
					cb.GetSize()
					cb.GetOldestSequence()
					cb.GetNewestSequence()
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	<-done
	close(done)
	wg.Wait()

	// Final verification
	if cb.GetSize() != 100 {
		t.Errorf("Expected final size 100, got %d", cb.GetSize())
	}
}

func TestCircularBufferClear(t *testing.T) {
	cb := NewCircularBuffer(10)

	// Add some chunks
	for i := 1; i <= 5; i++ {
		chunk := OutputChunk{
			Sequence:  uint64(i),
			Timestamp: time.Now(),
			Data:      []byte("data"),
			Stream:    StreamStdout,
		}
		cb.Append(chunk)
	}

	if cb.GetSize() != 5 {
		t.Errorf("Expected size 5 before clear, got %d", cb.GetSize())
	}

	// Clear buffer
	cb.Clear()

	if cb.GetSize() != 0 {
		t.Errorf("Expected size 0 after clear, got %d", cb.GetSize())
	}

	if cb.GetOldestSequence() != 0 {
		t.Errorf("Expected oldest sequence 0 after clear, got %d", cb.GetOldestSequence())
	}

	// Buffer should be usable after clear
	chunk := OutputChunk{
		Sequence:  100,
		Timestamp: time.Now(),
		Data:      []byte("data"),
		Stream:    StreamStdout,
	}
	cb.Append(chunk)

	if cb.GetSize() != 1 {
		t.Errorf("Expected size 1 after append post-clear, got %d", cb.GetSize())
	}
}

// Benchmark Append operation
func BenchmarkCircularBufferAppend(b *testing.B) {
	cb := NewCircularBuffer(1024)
	chunk := OutputChunk{
		Sequence:  1,
		Timestamp: time.Now(),
		Data:      []byte("test data"),
		Stream:    StreamStdout,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunk.Sequence = uint64(i)
		cb.Append(chunk)
	}
}

// Benchmark GetRange operation
func BenchmarkCircularBufferGetRange(b *testing.B) {
	cb := NewCircularBuffer(1024)

	// Fill buffer
	for i := 0; i < 1024; i++ {
		chunk := OutputChunk{
			Sequence:  uint64(i),
			Timestamp: time.Now(),
			Data:      []byte("test data"),
			Stream:    StreamStdout,
		}
		cb.Append(chunk)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.GetRange(0, 1024)
	}
}
