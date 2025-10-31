package exec

import (
	"io"
	"os"
	"sync"
	"time"
)

// WinSize represents terminal window dimensions
type WinSize struct {
	Rows uint32
	Cols uint32
}

// TTYState holds the state of a TTY session
type TTYState struct {
	mu sync.RWMutex

	// Terminal dimensions
	currentSize    WinSize
	lastResize     time.Time
	pendingResize  *WinSize
	resizeDebounce time.Duration

	// PTY file descriptors
	ptmx *os.File // PTY master
	pts  *os.File // PTY slave (optional, may not be used)

	// Stdin pipeline
	stdinPipe io.WriteCloser
}

// NewTTYState creates a new TTY state with default settings
func NewTTYState() *TTYState {
	return &TTYState{
		resizeDebounce: 50 * time.Millisecond,
		currentSize: WinSize{
			Rows: 24,
			Cols: 80,
		},
	}
}

// GetCurrentSize returns a copy of the current terminal size
func (t *TTYState) GetCurrentSize() WinSize {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentSize
}

// SetCurrentSize updates the current terminal size
func (t *TTYState) SetCurrentSize(size WinSize) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentSize = size
	t.lastResize = time.Now()
}

// GetPendingResize returns any pending resize event
func (t *TTYState) GetPendingResize() *WinSize {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.pendingResize == nil {
		return nil
	}
	size := *t.pendingResize
	return &size
}

// SetPendingResize stores a pending resize event
func (t *TTYState) SetPendingResize(size *WinSize) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if size == nil {
		t.pendingResize = nil
	} else {
		t.pendingResize = &WinSize{Rows: size.Rows, Cols: size.Cols}
	}
}

// ClearPendingResize removes any pending resize
func (t *TTYState) ClearPendingResize() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingResize = nil
}

// GetLastResize returns when the last resize occurred
func (t *TTYState) GetLastResize() time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastResize
}

// GetResizeDebounce returns the debounce duration
func (t *TTYState) GetResizeDebounce() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.resizeDebounce
}

// SetPTMX stores the PTY master file descriptor
func (t *TTYState) SetPTMX(ptmx *os.File) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ptmx = ptmx
}

// GetPTMX returns the PTY master file descriptor
func (t *TTYState) GetPTMX() *os.File {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ptmx
}

// SetStdinPipe stores the stdin pipe writer
func (t *TTYState) SetStdinPipe(pipe io.WriteCloser) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stdinPipe = pipe
}

// GetStdinPipe returns the stdin pipe writer
func (t *TTYState) GetStdinPipe() io.WriteCloser {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.stdinPipe
}

// Close closes all resources held by the TTY state
func (t *TTYState) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var firstErr error

	if t.stdinPipe != nil {
		if err := t.stdinPipe.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		t.stdinPipe = nil
	}

	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		t.ptmx = nil
	}

	if t.pts != nil {
		if err := t.pts.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		t.pts = nil
	}

	return firstErr
}

// ClientInfo tracks information about a connected client
type ClientInfo struct {
	ClientID     string
	ConnectedAt  time.Time
	LastActivity time.Time
	IsReconnect  bool
	ReplayOffset uint64
}

// NewClientInfo creates a new client info record
func NewClientInfo(clientID string, isReconnect bool, replayOffset uint64) *ClientInfo {
	now := time.Now()
	return &ClientInfo{
		ClientID:     clientID,
		ConnectedAt:  now,
		LastActivity: now,
		IsReconnect:  isReconnect,
		ReplayOffset: replayOffset,
	}
}

// UpdateActivity updates the last activity timestamp
func (c *ClientInfo) UpdateActivity() {
	c.LastActivity = time.Now()
}
