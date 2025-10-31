package buildkit

import (
	"context"
	"io"
	"sync"

	"github.com/dagger/dagger/internal/buildkit/util/bklog"
)

// TeeWriter implements io.Writer interface to write to both a file writer (required)
// and a stream writer (optional) simultaneously. This enables dual output patterns:
// 1. Persistent storage to files (for Container.Stdout(), Container.Stderr() methods)
// 2. Real-time streaming to gRPC clients (for live log consumption)
//
// Design priorities:
// - File writes MUST succeed even if stream fails (graceful degradation)
// - Stream writes should not block file I/O (non-blocking architecture)
// - Thread-safe operation for concurrent writes
// - If stream writer is nil/closed, file writes continue unaffected
type TeeWriter struct {
	ctx context.Context

	// fileWriter is the primary writer (required, never nil)
	// All writes must succeed to this writer
	fileWriter io.Writer

	// streamWriter is the secondary writer (optional, may be nil)
	// Writes to this writer are best-effort and non-blocking
	streamWriter io.Writer

	// streamChan buffers data for non-blocking stream writes
	// This channel decouples file writes from stream writes
	streamChan chan []byte

	// mu protects concurrent writes to the TeeWriter
	mu sync.Mutex

	// streamOnce ensures the stream goroutine is started exactly once
	streamOnce sync.Once

	// streamDone signals when the stream goroutine has exited
	streamDone chan struct{}

	// streamStarted tracks whether streamWriterLoop was actually started
	streamStarted bool
}

// NewTeeWriter creates a new TeeWriter that writes to both fileWriter and streamWriter.
// The fileWriter parameter is required and must not be nil.
// The streamWriter parameter is optional and may be nil (in which case only file writes occur).
// The ctx parameter is used for logging stream errors.
func NewTeeWriter(ctx context.Context, fileWriter io.Writer, streamWriter io.Writer) *TeeWriter {
	if fileWriter == nil {
		panic("TeeWriter: fileWriter cannot be nil")
	}

	tw := &TeeWriter{
		ctx:          ctx,
		fileWriter:   fileWriter,
		streamWriter: streamWriter,
		streamDone:   make(chan struct{}),
	}

	// Only create stream infrastructure if streamWriter is provided
	if streamWriter != nil {
		// Buffer size of 100 allows for some burst writes without blocking
		// Adjust this value based on expected write patterns and memory constraints
		tw.streamChan = make(chan []byte, 100)
	}

	return tw
}

// Write implements io.Writer interface. It writes data to both the file writer
// and the stream writer (if present).
//
// Write behavior:
// 1. Acquires lock for thread safety
// 2. Writes to file writer (blocking, synchronous) - this MUST succeed
// 3. If stream writer exists, queues data for non-blocking stream write
// 4. Returns result based on file write (stream errors don't propagate)
//
// If the file write fails, the error is returned immediately.
// If the stream write fails, the error is logged but not returned.
func (t *TeeWriter) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Primary write to file (blocking, synchronous)
	// This is the critical path and must succeed
	n, err = t.fileWriter.Write(p)
	if err != nil {
		return n, err
	}

	// Secondary write to stream (non-blocking, best-effort)
	// Only attempt if streamWriter was provided
	if t.streamWriter != nil && t.streamChan != nil {
		// Start the stream writer goroutine on first write
		t.streamOnce.Do(func() {
			t.streamStarted = true
			go t.streamWriterLoop()
		})

		// Make a copy of the data since we're queuing it asynchronously
		// The original slice may be reused by the caller
		dataCopy := make([]byte, len(p))
		copy(dataCopy, p)

		// Non-blocking send to stream channel
		select {
		case t.streamChan <- dataCopy:
			// Successfully queued for streaming
		default:
			// Channel is full, drop this write to avoid blocking
			// This maintains the non-blocking guarantee
			bklog.G(t.ctx).Warnf("TeeWriter: stream channel full, dropping %d bytes", len(p))
		}
	}

	return n, nil
}

// streamWriterLoop runs in a separate goroutine and handles all stream writes.
// This design ensures that slow or failing stream writes never block file writes.
func (t *TeeWriter) streamWriterLoop() {
	defer close(t.streamDone)

	for data := range t.streamChan {
		_, err := t.streamWriter.Write(data)
		if err != nil {
			// Log the error but continue processing
			// The stream may recover, or we may need to drain the channel
			bklog.G(t.ctx).WithError(err).Debugf("TeeWriter: stream write failed for %d bytes", len(data))
		}
	}
}

// Close closes the TeeWriter and waits for any pending stream writes to complete.
// This should be called when you're done writing to ensure all buffered data is flushed.
//
// Note: This does NOT close the underlying fileWriter or streamWriter.
// The caller is responsible for closing those writers if needed.
//
// This method is idempotent and safe to call multiple times.
func (t *TeeWriter) Close() error {
	t.mu.Lock()
	// Close the stream channel to signal the goroutine to exit
	// Check if channel is still open before closing to avoid panic
	if t.streamChan != nil {
		select {
		case <-t.streamDone:
			// Stream goroutine already finished, channel already closed
		default:
			// Channel still open, close it
			close(t.streamChan)
		}
	}
	streamStarted := t.streamStarted
	t.mu.Unlock()

	// Wait for the stream goroutine to finish processing any buffered data
	// Only wait if the goroutine was actually started
	if streamStarted {
		<-t.streamDone
	}

	return nil
}
