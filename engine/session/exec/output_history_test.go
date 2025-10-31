package exec

import (
	"sync"
	"testing"
)

func TestOutputHistoryBasic(t *testing.T) {
	h := NewOutputHistory()

	if h.GetCurrentSequence() != 0 {
		t.Errorf("Initial sequence should be 0, got %d", h.GetCurrentSequence())
	}

	if h.GetTotalSize() != 0 {
		t.Errorf("Initial size should be 0, got %d", h.GetTotalSize())
	}

	// Append stdout
	seq1 := h.AppendStdout([]byte("stdout data"))
	if seq1 != 1 {
		t.Errorf("Expected sequence 1, got %d", seq1)
	}

	// Append stderr
	seq2 := h.AppendStderr([]byte("stderr data"))
	if seq2 != 2 {
		t.Errorf("Expected sequence 2, got %d", seq2)
	}

	if h.GetCurrentSequence() != 2 {
		t.Errorf("Current sequence should be 2, got %d", h.GetCurrentSequence())
	}

	if h.GetTotalSize() != 2 {
		t.Errorf("Total size should be 2, got %d", h.GetTotalSize())
	}
}

func TestOutputHistorySequenceMonotonicity(t *testing.T) {
	h := NewOutputHistory()

	sequences := make([]uint64, 0, 100)

	for i := 0; i < 50; i++ {
		seq := h.AppendStdout([]byte("stdout"))
		sequences = append(sequences, seq)
	}

	for i := 0; i < 50; i++ {
		seq := h.AppendStderr([]byte("stderr"))
		sequences = append(sequences, seq)
	}

	// Verify sequences are monotonically increasing
	for i := 1; i < len(sequences); i++ {
		if sequences[i] <= sequences[i-1] {
			t.Errorf("Sequence not monotonic: %d followed by %d", sequences[i-1], sequences[i])
		}
	}

	// Verify final sequence
	if h.GetCurrentSequence() != 100 {
		t.Errorf("Expected final sequence 100, got %d", h.GetCurrentSequence())
	}
}

func TestOutputHistoryMergeAndSort(t *testing.T) {
	h := NewOutputHistory()

	// Add interleaved stdout and stderr
	h.AppendStdout([]byte("stdout 1")) // seq 1
	h.AppendStderr([]byte("stderr 2")) // seq 2
	h.AppendStdout([]byte("stdout 3")) // seq 3
	h.AppendStderr([]byte("stderr 4")) // seq 4
	h.AppendStdout([]byte("stdout 5")) // seq 5

	// Get all chunks
	chunks := h.GetHistoryRange(1, 5)

	if len(chunks) != 5 {
		t.Fatalf("Expected 5 chunks, got %d", len(chunks))
	}

	// Verify correct sequence order
	expectedSeqs := []uint64{1, 2, 3, 4, 5}
	expectedStreams := []StreamType{StreamStdout, StreamStderr, StreamStdout, StreamStderr, StreamStdout}

	for i, chunk := range chunks {
		if chunk.Sequence != expectedSeqs[i] {
			t.Errorf("Chunk %d: expected sequence %d, got %d", i, expectedSeqs[i], chunk.Sequence)
		}
		if chunk.Stream != expectedStreams[i] {
			t.Errorf("Chunk %d: expected stream %d, got %d", i, expectedStreams[i], chunk.Stream)
		}
	}
}

func TestOutputHistoryRangeQueries(t *testing.T) {
	h := NewOutputHistory()

	// Add 20 chunks (10 stdout, 10 stderr)
	for i := 0; i < 10; i++ {
		h.AppendStdout([]byte("stdout"))
		h.AppendStderr([]byte("stderr"))
	}

	tests := []struct {
		name        string
		fromSeq     uint64
		toSeq       uint64
		expectedLen int
	}{
		{"All chunks", 1, 20, 20},
		{"First half", 1, 10, 10},
		{"Second half", 11, 20, 10},
		{"Middle range", 5, 15, 11},
		{"Single chunk", 10, 10, 1},
		{"Outside range", 21, 30, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := h.GetHistoryRange(tt.fromSeq, tt.toSeq)
			if len(chunks) != tt.expectedLen {
				t.Errorf("Expected %d chunks, got %d", tt.expectedLen, len(chunks))
			}
		})
	}
}

func TestOutputHistorySeparateStreams(t *testing.T) {
	h := NewOutputHistory()

	// Add mixed chunks
	h.AppendStdout([]byte("out1")) // seq 1
	h.AppendStderr([]byte("err2")) // seq 2
	h.AppendStdout([]byte("out3")) // seq 3
	h.AppendStderr([]byte("err4")) // seq 4

	// Get only stdout
	stdoutChunks := h.GetStdoutRange(1, 4)
	if len(stdoutChunks) != 2 {
		t.Errorf("Expected 2 stdout chunks, got %d", len(stdoutChunks))
	}
	for _, chunk := range stdoutChunks {
		if chunk.Stream != StreamStdout {
			t.Errorf("Expected stdout stream, got %d", chunk.Stream)
		}
	}

	// Get only stderr
	stderrChunks := h.GetStderrRange(1, 4)
	if len(stderrChunks) != 2 {
		t.Errorf("Expected 2 stderr chunks, got %d", len(stderrChunks))
	}
	for _, chunk := range stderrChunks {
		if chunk.Stream != StreamStderr {
			t.Errorf("Expected stderr stream, got %d", chunk.Stream)
		}
	}
}

func TestOutputHistoryEmptyData(t *testing.T) {
	h := NewOutputHistory()

	// Append empty data should not increment sequence
	seq1 := h.AppendStdout([]byte{})
	if seq1 != 0 {
		t.Errorf("Empty append should return 0, got %d", seq1)
	}

	if h.GetCurrentSequence() != 0 {
		t.Errorf("Sequence should still be 0, got %d", h.GetCurrentSequence())
	}

	// Append real data
	seq2 := h.AppendStdout([]byte("data"))
	if seq2 != 1 {
		t.Errorf("First real append should be sequence 1, got %d", seq2)
	}
}

func TestOutputHistoryConcurrentAppend(t *testing.T) {
	h := NewOutputHistory()

	const numGoroutines = 10
	const appendsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2) // Both stdout and stderr goroutines

	// Concurrent stdout appends
	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < appendsPerGoroutine; i++ {
				h.AppendStdout([]byte("stdout"))
			}
		}()
	}

	// Concurrent stderr appends
	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < appendsPerGoroutine; i++ {
				h.AppendStderr([]byte("stderr"))
			}
		}()
	}

	wg.Wait()

	// Should have all 2000 appends
	expectedSeq := uint64(numGoroutines * appendsPerGoroutine * 2)
	if h.GetCurrentSequence() != expectedSeq {
		t.Errorf("Expected sequence %d, got %d", expectedSeq, h.GetCurrentSequence())
	}

	// High water mark should match
	if h.GetHighWaterMark() != expectedSeq {
		t.Errorf("Expected high water mark %d, got %d", expectedSeq, h.GetHighWaterMark())
	}
}

func TestOutputHistoryHighWaterMark(t *testing.T) {
	h := NewOutputHistory()

	if h.GetHighWaterMark() != 0 {
		t.Errorf("Initial high water mark should be 0, got %d", h.GetHighWaterMark())
	}

	h.AppendStdout([]byte("data"))
	if h.GetHighWaterMark() != 1 {
		t.Errorf("High water mark should be 1, got %d", h.GetHighWaterMark())
	}

	for i := 0; i < 99; i++ {
		h.AppendStderr([]byte("data"))
	}

	if h.GetHighWaterMark() != 100 {
		t.Errorf("High water mark should be 100, got %d", h.GetHighWaterMark())
	}
}

func TestOutputHistoryGapDetection(t *testing.T) {
	h := NewOutputHistoryWithCapacity(10)

	// Fill beyond capacity to create potential gaps
	for i := 0; i < 20; i++ {
		h.AppendStdout([]byte("data"))
	}

	// Oldest available should be around sequence 11 (20 - 10 + 1)
	oldestSeq := h.GetOldestSequence()

	// Request from sequence 1 should detect gap
	gap := h.DetectGaps(1, 20)
	if gap == nil {
		t.Fatal("Expected gap detection, got nil")
	}

	if gap.ExpectedStart != 1 {
		t.Errorf("Expected gap start 1, got %d", gap.ExpectedStart)
	}

	if gap.ActualStart != oldestSeq {
		t.Errorf("Expected actual start %d, got %d", oldestSeq, gap.ActualStart)
	}

	if gap.MissingCount != oldestSeq-1 {
		t.Errorf("Expected missing count %d, got %d", oldestSeq-1, gap.MissingCount)
	}

	// Request within available range should not detect gap
	gap2 := h.DetectGaps(oldestSeq, 20)
	if gap2 != nil {
		t.Errorf("Expected no gap, got %+v", gap2)
	}
}

func TestOutputHistoryClear(t *testing.T) {
	h := NewOutputHistory()

	// Add data
	for i := 0; i < 10; i++ {
		h.AppendStdout([]byte("data"))
	}

	if h.GetTotalSize() != 10 {
		t.Errorf("Expected size 10, got %d", h.GetTotalSize())
	}

	// Clear
	h.Clear()

	if h.GetTotalSize() != 0 {
		t.Errorf("Expected size 0 after clear, got %d", h.GetTotalSize())
	}

	// Sequence counter should NOT be reset
	if h.GetCurrentSequence() != 10 {
		t.Errorf("Sequence should still be 10 after clear, got %d", h.GetCurrentSequence())
	}

	// Can append after clear
	seq := h.AppendStdout([]byte("new data"))
	if seq != 11 {
		t.Errorf("Expected sequence 11 after clear, got %d", seq)
	}
}

// Benchmark append operations
func BenchmarkOutputHistoryAppendStdout(b *testing.B) {
	h := NewOutputHistory()
	data := []byte("test data")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.AppendStdout(data)
	}
}

func BenchmarkOutputHistoryAppendInterleaved(b *testing.B) {
	h := NewOutputHistory()
	data := []byte("test data")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			h.AppendStdout(data)
		} else {
			h.AppendStderr(data)
		}
	}
}

func BenchmarkOutputHistoryGetHistoryRange(b *testing.B) {
	h := NewOutputHistory()

	// Prepopulate with 1024 chunks
	for i := 0; i < 512; i++ {
		h.AppendStdout([]byte("stdout data"))
		h.AppendStderr([]byte("stderr data"))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.GetHistoryRange(1, 1024)
	}
}
