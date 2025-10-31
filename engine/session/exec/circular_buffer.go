package exec

import (
	"sync"
	"time"
)

// StreamType identifies whether output is stdout or stderr
type StreamType int

const (
	StreamStdout StreamType = 1
	StreamStderr StreamType = 2
)

// OutputChunk represents a single piece of output with sequence tracking
type OutputChunk struct {
	Sequence  uint64
	Timestamp time.Time
	Data      []byte
	Stream    StreamType
}

// CircularBuffer is a fixed-size ring buffer for storing output chunks
// with sequence-based retrieval support
type CircularBuffer struct {
	buffer   []OutputChunk
	capacity int
	head     int // Next write position
	size     int // Current number of items
	mu       sync.RWMutex
}

// NewCircularBuffer creates a new circular buffer with the specified capacity
func NewCircularBuffer(capacity int) *CircularBuffer {
	if capacity <= 0 {
		capacity = 1024 // Default capacity
	}

	return &CircularBuffer{
		buffer:   make([]OutputChunk, capacity),
		capacity: capacity,
		head:     0,
		size:     0,
	}
}

// Append adds a chunk to the buffer
// If buffer is full, overwrites the oldest chunk
// O(1) operation
func (cb *CircularBuffer) Append(chunk OutputChunk) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.buffer[cb.head] = chunk
	cb.head = (cb.head + 1) % cb.capacity

	if cb.size < cb.capacity {
		cb.size++
	}
}

// GetRange returns all chunks with sequence numbers in [fromSeq, toSeq]
// Returns empty slice if no chunks match
func (cb *CircularBuffer) GetRange(fromSeq, toSeq uint64) []OutputChunk {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.size == 0 {
		return nil
	}

	// Collect matching chunks
	result := make([]OutputChunk, 0, cb.size)

	// Iterate through buffer in insertion order
	start := cb.head - cb.size
	if start < 0 {
		start += cb.capacity
	}

	for i := 0; i < cb.size; i++ {
		idx := (start + i) % cb.capacity
		chunk := cb.buffer[idx]

		if chunk.Sequence >= fromSeq && chunk.Sequence <= toSeq {
			// Make a copy to avoid sharing memory
			result = append(result, OutputChunk{
				Sequence:  chunk.Sequence,
				Timestamp: chunk.Timestamp,
				Data:      append([]byte(nil), chunk.Data...),
				Stream:    chunk.Stream,
			})
		}
	}

	return result
}

// GetOldestSequence returns the sequence number of the oldest chunk
// Returns 0 if buffer is empty
func (cb *CircularBuffer) GetOldestSequence() uint64 {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.size == 0 {
		return 0
	}

	// Find oldest chunk position
	oldestIdx := cb.head - cb.size
	if oldestIdx < 0 {
		oldestIdx += cb.capacity
	}

	return cb.buffer[oldestIdx].Sequence
}

// GetNewestSequence returns the sequence number of the newest chunk
// Returns 0 if buffer is empty
func (cb *CircularBuffer) GetNewestSequence() uint64 {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.size == 0 {
		return 0
	}

	// Most recent chunk is just before head
	newestIdx := cb.head - 1
	if newestIdx < 0 {
		newestIdx += cb.capacity
	}

	return cb.buffer[newestIdx].Sequence
}

// GetSize returns the current number of chunks in the buffer
func (cb *CircularBuffer) GetSize() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.size
}

// GetCapacity returns the maximum capacity of the buffer
func (cb *CircularBuffer) GetCapacity() int {
	return cb.capacity
}

// Clear removes all chunks from the buffer
func (cb *CircularBuffer) Clear() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.head = 0
	cb.size = 0
}
