package exec

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// OutputHistory manages separate circular buffers for stdout and stderr
// with monotonic sequence number tracking
type OutputHistory struct {
	mu sync.RWMutex

	// Separate buffers for stdout and stderr
	stdoutBuffer *CircularBuffer
	stderrBuffer *CircularBuffer

	// Atomic sequence counter (monotonically increasing)
	sequence uint64

	// High water mark (highest sequence number written)
	highWaterMark uint64
}

// NewOutputHistory creates a new output history manager with default buffer sizes
func NewOutputHistory() *OutputHistory {
	return NewOutputHistoryWithCapacity(1024)
}

// NewOutputHistoryWithCapacity creates a new output history with specified buffer capacity
func NewOutputHistoryWithCapacity(capacity int) *OutputHistory {
	return &OutputHistory{
		stdoutBuffer:  NewCircularBuffer(capacity),
		stderrBuffer:  NewCircularBuffer(capacity),
		sequence:      0,
		highWaterMark: 0,
	}
}

// nextSequence atomically increments and returns the next sequence number
func (h *OutputHistory) nextSequence() uint64 {
	return atomic.AddUint64(&h.sequence, 1)
}

// GetCurrentSequence returns the current sequence number (last assigned)
func (h *OutputHistory) GetCurrentSequence() uint64 {
	return atomic.LoadUint64(&h.sequence)
}

// AppendStdout adds stdout data to the history
func (h *OutputHistory) AppendStdout(data []byte) uint64 {
	if len(data) == 0 {
		return h.GetCurrentSequence()
	}

	seq := h.nextSequence()

	chunk := OutputChunk{
		Sequence:  seq,
		Timestamp: time.Now(),
		Data:      data,
		Stream:    StreamStdout,
	}

	h.stdoutBuffer.Append(chunk)
	atomic.StoreUint64(&h.highWaterMark, seq)

	return seq
}

// AppendStderr adds stderr data to the history
func (h *OutputHistory) AppendStderr(data []byte) uint64 {
	if len(data) == 0 {
		return h.GetCurrentSequence()
	}

	seq := h.nextSequence()

	chunk := OutputChunk{
		Sequence:  seq,
		Timestamp: time.Now(),
		Data:      data,
		Stream:    StreamStderr,
	}

	h.stderrBuffer.Append(chunk)
	atomic.StoreUint64(&h.highWaterMark, seq)

	return seq
}

// GetHistoryRange retrieves chunks from both stdout and stderr within the sequence range
// Returns a merged and sorted list of chunks
func (h *OutputHistory) GetHistoryRange(fromSeq, toSeq uint64) []OutputChunk {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Get chunks from both buffers
	stdoutChunks := h.stdoutBuffer.GetRange(fromSeq, toSeq)
	stderrChunks := h.stderrBuffer.GetRange(fromSeq, toSeq)

	// Merge and sort
	return mergeAndSortChunks(stdoutChunks, stderrChunks)
}

// GetStdoutRange retrieves only stdout chunks within the sequence range
func (h *OutputHistory) GetStdoutRange(fromSeq, toSeq uint64) []OutputChunk {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.stdoutBuffer.GetRange(fromSeq, toSeq)
}

// GetStderrRange retrieves only stderr chunks within the sequence range
func (h *OutputHistory) GetStderrRange(fromSeq, toSeq uint64) []OutputChunk {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.stderrBuffer.GetRange(fromSeq, toSeq)
}

// GetOldestSequence returns the oldest available sequence number
// Returns the minimum of stdout and stderr oldest sequences
func (h *OutputHistory) GetOldestSequence() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stdoutOldest := h.stdoutBuffer.GetOldestSequence()
	stderrOldest := h.stderrBuffer.GetOldestSequence()

	// Return the minimum non-zero value
	if stdoutOldest == 0 {
		return stderrOldest
	}
	if stderrOldest == 0 {
		return stdoutOldest
	}
	if stdoutOldest < stderrOldest {
		return stdoutOldest
	}
	return stderrOldest
}

// GetHighWaterMark returns the highest sequence number ever written
func (h *OutputHistory) GetHighWaterMark() uint64 {
	return atomic.LoadUint64(&h.highWaterMark)
}

// GetTotalSize returns the total number of chunks across both buffers
func (h *OutputHistory) GetTotalSize() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.stdoutBuffer.GetSize() + h.stderrBuffer.GetSize()
}

// Clear removes all data from both buffers
func (h *OutputHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.stdoutBuffer.Clear()
	h.stderrBuffer.Clear()
}

// mergeAndSortChunks merges chunks from stdout and stderr and sorts by sequence number
func mergeAndSortChunks(stdoutChunks, stderrChunks []OutputChunk) []OutputChunk {
	// Handle empty cases
	if len(stdoutChunks) == 0 && len(stderrChunks) == 0 {
		return nil
	}
	if len(stdoutChunks) == 0 {
		return stderrChunks
	}
	if len(stderrChunks) == 0 {
		return stdoutChunks
	}

	// Merge into single slice
	merged := make([]OutputChunk, 0, len(stdoutChunks)+len(stderrChunks))
	merged = append(merged, stdoutChunks...)
	merged = append(merged, stderrChunks...)

	// Sort by sequence number
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Sequence < merged[j].Sequence
	})

	return merged
}

// DetectGaps analyzes a sequence range and returns information about missing chunks
type GapInfo struct {
	ExpectedStart uint64
	ActualStart   uint64
	MissingCount  uint64
}

// DetectGaps checks if there are sequence gaps in the available history
func (h *OutputHistory) DetectGaps(fromSeq, toSeq uint64) *GapInfo {
	oldestAvailable := h.GetOldestSequence()

	if fromSeq < oldestAvailable && oldestAvailable > 0 {
		return &GapInfo{
			ExpectedStart: fromSeq,
			ActualStart:   oldestAvailable,
			MissingCount:  oldestAvailable - fromSeq,
		}
	}

	return nil
}
