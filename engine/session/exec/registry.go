package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/dagger/dagger/internal/buildkit/util/bklog"
)

// ExecStatus represents the current status of an exec instance.
type ExecStatus string

const (
	// ExecStatusPending means the exec has been registered but not started.
	ExecStatusPending ExecStatus = "pending"

	// ExecStatusRunning means the exec is currently running.
	ExecStatusRunning ExecStatus = "running"

	// ExecStatusExited means the exec has completed.
	ExecStatusExited ExecStatus = "exited"
)

const (
	// StdinChannelSize is the buffer size for stdin channel
	StdinChannelSize = 100

	// StdinMaxChunkSize is the maximum size for a single stdin chunk (16KB)
	StdinMaxChunkSize = 16 * 1024

	// ResizeChannelSize is the buffer size for resize events
	ResizeChannelSize = 10

	// ResizeDebounceDefault is the default debounce duration
	ResizeDebounceDefault = 50 * time.Millisecond

	// Cleanup intervals and timeouts
	CleanupInterval = 30 * time.Second

	// SessionOrphanTimeout is how long to keep sessions with no clients before cleanup
	SessionOrphanTimeout = 5 * time.Minute

	// SessionOutputRetentionTimeout is how long to keep exited sessions for output retrieval
	SessionOutputRetentionTimeout = 5 * time.Minute

	// SessionMaxIdleTimeout is how long to keep idle sessions before cleanup
	SessionMaxIdleTimeout = 1 * time.Hour

	// Resource limits
	MaxSessionsPerContainer = 10
	MaxSessionsGlobal       = 1000
)

// ExecInstance represents a single container exec session that can be shared
// across multiple gRPC client connections. It manages the lifecycle of stdout/stderr
// streaming and exit code delivery.
type ExecInstance struct {
	// Immutable fields (set at creation)
	containerID string
	execID      string
	sessionID   string
	rootCtx     context.Context
	cancel      context.CancelFunc

	// Status tracking (protected by mu)
	mu       sync.RWMutex
	status   ExecStatus
	exitCode *int32
	exitTime *time.Time

	// Client connection tracking (protected by clientsMu)
	clientsMu            sync.Mutex
	clients              map[string]*ClientInfo
	lastClientDisconnect time.Time

	// I/O channels for streaming
	stdoutChan chan []byte
	stderrChan chan []byte
	exitChan   chan int32

	// TTY support fields
	isTTY        bool
	ttyState     *TTYState
	stdinChan    chan []byte
	resizeChan   chan WinSize
	lastActivity time.Time

	// Output history for TTY sessions (enables reconnection)
	outputHistory *OutputHistory

	// Cleanup state
	closeOnce sync.Once
	closed    bool
}

// NewExecInstance creates a new exec instance for the given container and exec ID.
// The rootCtx is the context for the entire exec lifecycle.
func NewExecInstance(ctx context.Context, containerID, execID, sessionID string, isTTY bool) *ExecInstance {
	ctx, cancel := context.WithCancel(ctx)

	instance := &ExecInstance{
		containerID: containerID,
		execID:      execID,
		sessionID:   sessionID,
		isTTY:       isTTY,
		rootCtx:     ctx,
		cancel:      cancel,
		status:      ExecStatusPending,
		clients:     make(map[string]*ClientInfo),
		stdoutChan:  make(chan []byte, 100), // Buffered to handle bursts
		stderrChan:  make(chan []byte, 100),
		exitChan:    make(chan int32, 1), // Buffered since exit code is sent once
	}

	// Initialize TTY-specific fields if this is a TTY session
	if isTTY {
		instance.ttyState = NewTTYState()
		instance.stdinChan = make(chan []byte, StdinChannelSize)
		instance.resizeChan = make(chan WinSize, ResizeChannelSize)
		instance.outputHistory = NewOutputHistory()
	}

	// Start resize debouncer if TTY
	if isTTY {
		instance.startResizeDebouncer(ctx)
	}

	instance.lastActivity = time.Now()
	return instance
}

// GetStatus returns the current status of the exec instance.
func (e *ExecInstance) GetStatus() ExecStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

// MarkExited marks the exec instance as exited with the given exit code.
// This is idempotent - subsequent calls with different exit codes are ignored.
func (e *ExecInstance) MarkExited(exitCode int32) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Only update if not already exited
	if e.status == ExecStatusExited {
		bklog.G(e.rootCtx).Debugf("exec %s/%s already marked as exited", e.containerID, e.execID)
		return nil
	}

	e.status = ExecStatusExited
	e.exitCode = &exitCode
	now := time.Now()
	e.exitTime = &now

	// Send exit code to channel (non-blocking since channel is buffered)
	select {
	case e.exitChan <- exitCode:
		bklog.G(e.rootCtx).Debugf("exec %s/%s exited with code %d", e.containerID, e.execID, exitCode)
	default:
		// Exit code already sent or channel closed
		bklog.G(e.rootCtx).Debug("exit code already sent or channel closed")
	}

	return nil
}

// AddClient registers a new client connection to this exec instance.
// Returns an error if the client already exists.
func (e *ExecInstance) AddClient(clientID string, isReconnect bool, replayOffset uint64) error {
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()

	if _, exists := e.clients[clientID]; exists {
		return fmt.Errorf("client %s already exists", clientID)
	}

	e.clients[clientID] = NewClientInfo(clientID, isReconnect, replayOffset)
	e.lastActivity = time.Now()

	// Mark as running when first client connects
	e.mu.Lock()
	if e.status == ExecStatusPending {
		e.status = ExecStatusRunning
		bklog.G(e.rootCtx).Debugf("exec %s/%s started (first client connected)", e.containerID, e.execID)
	}
	e.mu.Unlock()

	bklog.G(e.rootCtx).Debugf("client %s connected to exec %s/%s (total: %d)",
		clientID, e.containerID, e.execID, len(e.clients))
	return nil
}

// RemoveClient unregisters a client connection from this exec instance.
// Returns the number of remaining connected clients.
func (e *ExecInstance) RemoveClient(clientID string) int {
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()

	delete(e.clients, clientID)
	remaining := len(e.clients)

	if remaining == 0 {
		e.lastClientDisconnect = time.Now()
	}

	bklog.G(e.rootCtx).Debugf("client %s disconnected from exec %s/%s (remaining: %d)",
		clientID, e.containerID, e.execID, remaining)
	return remaining
}

// WriteStdin writes data to the stdin channel
// If data is empty, closes the stdin pipe (EOF signal)
// Returns error if not a TTY session or if channel is full (backpressure)
func (e *ExecInstance) WriteStdin(data []byte) error {
	if !e.isTTY {
		return fmt.Errorf("stdin not available for non-TTY session")
	}

	// Empty data signals EOF
	if len(data) == 0 {
		return e.closeStdin()
	}

	// Enforce max chunk size
	if len(data) > StdinMaxChunkSize {
		return fmt.Errorf("stdin chunk too large: %d bytes (max %d)", len(data), StdinMaxChunkSize)
	}

	// Non-blocking send with backpressure detection
	select {
	case e.stdinChan <- data:
		e.UpdateActivity()
		return nil
	default:
		return fmt.Errorf("stdin buffer full (backpressure)")
	}
}

// closeStdin closes the stdin pipe to signal EOF
func (e *ExecInstance) closeStdin() error {
	if e.ttyState == nil {
		return fmt.Errorf("no TTY state")
	}

	pipe := e.ttyState.GetStdinPipe()
	if pipe == nil {
		return nil // Already closed
	}

	if err := pipe.Close(); err != nil {
		return fmt.Errorf("failed to close stdin pipe: %w", err)
	}

	e.ttyState.SetStdinPipe(nil)
	return nil
}

// SendResize sends a terminal resize event
// Events are debounced to prevent resize storms
func (e *ExecInstance) SendResize(width, height uint32) error {
	if !e.isTTY {
		return fmt.Errorf("resize not available for non-TTY session")
	}

	if width == 0 || height == 0 {
		return fmt.Errorf("invalid terminal size: %dx%d", width, height)
	}

	newSize := WinSize{
		Rows: height,
		Cols: width,
	}

	ttyState := e.ttyState
	if ttyState == nil {
		return fmt.Errorf("TTY state not initialized")
	}

	// Update current size immediately (for queries)
	ttyState.SetCurrentSize(newSize)

	// Check if we should debounce
	now := time.Now()
	lastResize := ttyState.GetLastResize()
	debounce := ttyState.GetResizeDebounce()

	if !lastResize.IsZero() && now.Sub(lastResize) < debounce {
		// Still within debounce window, store as pending
		ttyState.SetPendingResize(&newSize)
		return nil
	}

	// Send immediately
	ttyState.SetCurrentSize(newSize)
	ttyState.ClearPendingResize()

	// Non-blocking send with overflow handling
	select {
	case e.resizeChan <- newSize:
		e.UpdateActivity()
		return nil
	default:
		// Channel full, drain one old event and send new one
		select {
		case <-e.resizeChan:
			// Drained
		default:
		}

		// Try sending again
		select {
		case e.resizeChan <- newSize:
			e.UpdateActivity()
			return nil
		default:
			return fmt.Errorf("resize channel overflow")
		}
	}
}

// GetResizeChannel returns a read-only channel for receiving resize events
func (e *ExecInstance) GetResizeChannel() <-chan WinSize {
	return e.resizeChan
}

// GetCurrentSize returns the current terminal size
// Returns nil if not a TTY session or size not set
func (e *ExecInstance) GetCurrentSize() *WinSize {
	if !e.isTTY || e.ttyState == nil {
		return nil
	}

	size := e.ttyState.GetCurrentSize()
	return &size
}

// startResizeDebouncer starts a goroutine that processes pending resize events
// This is called when the ExecInstance is created
func (e *ExecInstance) startResizeDebouncer(ctx context.Context) {
	if !e.isTTY || e.resizeChan == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(e.ttyState.GetResizeDebounce())
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				// Check if there's a pending resize
				pending := e.ttyState.GetPendingResize()
				if pending == nil {
					continue
				}

				// Send pending resize
				select {
				case e.resizeChan <- *pending:
					e.ttyState.ClearPendingResize()
				default:
					// Channel full, will try again next tick
				}
			}
		}
	}()
}

// GetStdoutWriter returns an io.Writer that streams stdout to connected clients.
// This writer is thread-safe and non-blocking.
func (e *ExecInstance) GetStdoutWriter() io.Writer {
	// If TTY with history, use historyTeeWriter
	if e.isTTY && e.outputHistory != nil {
		return &historyTeeWriter{
			instance: e,
			stream:   StreamStdout,
		}
	}

	// Otherwise use basic streamWriter (existing behavior)
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stdoutChan,
		name:    "stdout",
	}
}

// GetStderrWriter returns an io.Writer that streams stderr to connected clients.
// This writer is thread-safe and non-blocking.
func (e *ExecInstance) GetStderrWriter() io.Writer {
	// If TTY with history, use historyTeeWriter
	if e.isTTY && e.outputHistory != nil {
		return &historyTeeWriter{
			instance: e,
			stream:   StreamStderr,
		}
	}

	// Otherwise use basic streamWriter (existing behavior)
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stderrChan,
		name:    "stderr",
	}
}

// GetStdinReader returns an io.Reader that reads from the stdin channel
// Launches a goroutine to forward data from channel to pipe
func (e *ExecInstance) GetStdinReader(ctx context.Context) (io.Reader, error) {
	if !e.isTTY {
		return nil, fmt.Errorf("stdin not available for non-TTY session")
	}

	if e.stdinChan == nil {
		return nil, fmt.Errorf("stdin channel not initialized")
	}

	// Check if pipe already exists
	if pipe := e.ttyState.GetStdinPipe(); pipe != nil {
		return nil, fmt.Errorf("stdin reader already created")
	}

	// Create pipe
	pipeReader, pipeWriter := io.Pipe()
	e.ttyState.SetStdinPipe(pipeWriter)

	// Launch goroutine to forward stdin channel -> pipe
	go e.forwardStdinToPipe(ctx, pipeWriter)

	return pipeReader, nil
}

// forwardStdinToPipe forwards data from stdin channel to the pipe
// Runs in a goroutine
func (e *ExecInstance) forwardStdinToPipe(ctx context.Context, pipe *io.PipeWriter) {
	defer func() {
		pipe.Close()
		e.ttyState.SetStdinPipe(nil)
	}()

	for {
		select {
		case <-ctx.Done():
			// Context cancelled, close pipe with error
			pipe.CloseWithError(ctx.Err())
			return

		case data, ok := <-e.stdinChan:
			if !ok {
				// Channel closed, close pipe normally (EOF)
				return
			}

			// Write to pipe
			_, err := pipe.Write(data)
			if err != nil {
				// Pipe closed or error, stop forwarding
				return
			}
		}
	}
}

// Cleanup closes all channels and releases resources.
// This is safe to call multiple times (subsequent calls are no-ops).
// It should be called when the exec instance is no longer needed.
func (e *ExecInstance) Cleanup() error {
	var cleanupErr error

	e.closeOnce.Do(func() {
		e.mu.Lock()

		// Mark as closed
		e.closed = true

		// Cancel context to signal shutdown
		e.cancel()

		// Close all channels to signal end of stream
		close(e.stdoutChan)
		close(e.stderrChan)
		close(e.exitChan)

		// Close TTY-specific channels
		if e.isTTY {
			if e.stdinChan != nil {
				close(e.stdinChan)
				// Don't set to nil - forwardStdinToPipe goroutine may still be reading
			}
			if e.resizeChan != nil {
				close(e.resizeChan)
			}
			// Clear output history
			if e.outputHistory != nil {
				e.outputHistory.Clear()
			}
		}

		clientCount := len(e.clients)
		e.mu.Unlock()

		// Close stdin pipe if exists
		if e.ttyState != nil {
			if pipe := e.ttyState.GetStdinPipe(); pipe != nil {
				pipe.Close()
				e.ttyState.SetStdinPipe(nil)
			}
		}

		// Close TTY state resources
		if e.ttyState != nil {
			if err := e.ttyState.Close(); err != nil {
				cleanupErr = err
				bklog.G(e.rootCtx).WithError(err).Debug("error closing TTY state")
			}
		}

		bklog.G(e.rootCtx).Debugf("exec instance %s/%s cleaned up (clients: %d)",
			e.containerID, e.execID, clientCount)
	})

	return cleanupErr
}

// GetSessionID returns the session ID
func (e *ExecInstance) GetSessionID() string {
	return e.sessionID
}

// IsTTY returns whether this is a TTY session
func (e *ExecInstance) IsTTY() bool {
	return e.isTTY
}

// GetTTYState returns the TTY state (nil if not a TTY session)
func (e *ExecInstance) GetTTYState() *TTYState {
	return e.ttyState
}

// UpdateActivity updates the last activity timestamp
func (e *ExecInstance) UpdateActivity() {
	e.lastActivity = time.Now()
}

// GetLastActivity returns the last activity timestamp
func (e *ExecInstance) GetLastActivity() time.Time {
	return e.lastActivity
}

// GetClientCount returns the number of connected clients
func (e *ExecInstance) GetClientCount() int {
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()
	return len(e.clients)
}

// GetClients returns a copy of the clients map
func (e *ExecInstance) GetClients() map[string]*ClientInfo {
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()

	clients := make(map[string]*ClientInfo, len(e.clients))
	for k, v := range e.clients {
		clientCopy := *v
		clients[k] = &clientCopy
	}
	return clients
}

// GetOutputHistory returns the output history (nil if not TTY)
func (e *ExecInstance) GetOutputHistory() *OutputHistory {
	return e.outputHistory
}

// ExecSessionRegistry manages multiple concurrent exec instances for different containers.
// It provides multiplexing so multiple clients can attach to the same exec session.
// All methods are thread-safe.
type ExecSessionRegistry struct {
	// mu protects concurrent access to instances map
	mu sync.RWMutex

	// instances maps "containerID/execID" to ExecInstance
	instances map[string]*ExecInstance

	// sessionIndex maps session ID to ExecInstance
	sessionIndex map[string]*ExecInstance

	// ctx is the registry lifecycle context
	ctx context.Context

	// cancel stops background operations
	cancel context.CancelFunc

	// cleanup task management
	cleanupOnce   sync.Once
	cleanupDone   chan struct{}
	cleanupTicker *time.Ticker
}

// NewExecSessionRegistry creates a new registry for managing exec instances.
func NewExecSessionRegistry(ctx context.Context) *ExecSessionRegistry {
	ctx, cancel := context.WithCancel(ctx)

	registry := &ExecSessionRegistry{
		instances:    make(map[string]*ExecInstance),
		sessionIndex: make(map[string]*ExecInstance),
		ctx:          ctx,
		cancel:       cancel,
		cleanupDone:  make(chan struct{}),
	}

	// Start background cleanup task
	registry.startCleanupTask()

	return registry
}

// Register creates and registers a new exec instance.
// Returns an error if an instance with the same containerID/execID already exists.
func (r *ExecSessionRegistry) Register(containerID, execID, sessionID string, isTTY bool) (*ExecInstance, error) {
	if containerID == "" {
		return nil, errors.New("containerID cannot be empty")
	}
	if execID == "" {
		return nil, errors.New("execID cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check global session limit
	if len(r.instances) >= MaxSessionsGlobal {
		return nil, fmt.Errorf("maximum number of sessions reached (%d)", MaxSessionsGlobal)
	}

	// Check per-container session limit
	containerSessions := 0
	prefix := containerID + "/"
	for key := range r.instances {
		if strings.HasPrefix(key, prefix) {
			containerSessions++
		}
	}
	if containerSessions >= MaxSessionsPerContainer {
		return nil, fmt.Errorf("maximum number of sessions for container %s reached (%d)",
			containerID, MaxSessionsPerContainer)
	}

	key := makeKey(containerID, execID)

	// Check if already registered
	if _, exists := r.instances[key]; exists {
		return nil, fmt.Errorf("exec instance %s already registered", key)
	}

	// Generate session ID if not provided
	if sessionID == "" {
		var err error
		sessionID, err = generateSessionID(containerID, execID)
		if err != nil {
			return nil, fmt.Errorf("failed to generate session ID: %w", err)
		}
	}

	// Check if session ID already exists (collision detection)
	if _, exists := r.sessionIndex[sessionID]; exists {
		return nil, fmt.Errorf("session ID collision detected: %s", sessionID)
	}

	// Create new instance
	instance := NewExecInstance(r.ctx, containerID, execID, sessionID, isTTY)

	// Store in both maps
	r.instances[key] = instance
	r.sessionIndex[sessionID] = instance

	bklog.G(r.ctx).Debugf("registered exec instance: %s (session: %s, total: %d)", key, sessionID, len(r.instances))
	return instance, nil
}

// Unregister removes an exec instance from the registry and cleans it up.
// Returns an error if the instance is not found.
func (r *ExecSessionRegistry) Unregister(containerID, execID string) error {
	if containerID == "" {
		return errors.New("containerID cannot be empty")
	}
	if execID == "" {
		return errors.New("execID cannot be empty")
	}

	key := makeKey(containerID, execID)

	r.mu.Lock()
	instance, exists := r.instances[key]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("exec instance %s not found", key)
	}

	// Get session ID before cleanup
	sessionID := instance.GetSessionID()

	// Remove from both maps
	delete(r.instances, key)
	delete(r.sessionIndex, sessionID)
	r.mu.Unlock()

	// Clean up instance (outside lock to avoid blocking)
	if err := instance.Cleanup(); err != nil {
		bklog.G(r.ctx).WithError(err).Warnf("error cleaning up exec instance %s", key)
	}

	bklog.G(r.ctx).Debugf("unregistered exec instance: %s (session: %s, remaining: %d)", key, sessionID, len(r.instances)-1)
	return nil
}

// GetInstance retrieves an exec instance by container ID and exec ID.
// Returns an error if the instance is not found.
func (r *ExecSessionRegistry) GetInstance(containerID, execID string) (*ExecInstance, error) {
	if containerID == "" {
		return nil, errors.New("containerID cannot be empty")
	}
	if execID == "" {
		return nil, errors.New("execID cannot be empty")
	}

	key := makeKey(containerID, execID)

	r.mu.RLock()
	defer r.mu.RUnlock()

	instance, exists := r.instances[key]
	if !exists {
		return nil, fmt.Errorf("exec instance %s not found", key)
	}

	return instance, nil
}

// GetInstanceBySessionID retrieves an execution instance by session ID
func (r *ExecSessionRegistry) GetInstanceBySessionID(sessionID string) (*ExecInstance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	instance, exists := r.sessionIndex[sessionID]
	if !exists {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return instance, nil
}

// GetSessionCount returns the total number of active sessions
func (r *ExecSessionRegistry) GetSessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.sessionIndex)
}

// GetContainerSessionCount returns the number of sessions for a specific container
func (r *ExecSessionRegistry) GetContainerSessionCount(containerID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	prefix := containerID + "/"
	for key := range r.instances {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

// ListContainers returns a list of all container IDs with active exec instances.
// The result is sorted and deduplicated.
func (r *ExecSessionRegistry) ListContainers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Use map to deduplicate container IDs
	containers := make(map[string]struct{})
	for key := range r.instances {
		containerID, _ := splitKey(key)
		containers[containerID] = struct{}{}
	}

	// Convert to slice
	result := make([]string, 0, len(containers))
	for containerID := range containers {
		result = append(result, containerID)
	}

	return result
}

// GetStdoutWriter returns an io.Writer for streaming stdout from the exec instance.
// This is a convenience method that wraps GetInstance and GetStdoutWriter.
func (r *ExecSessionRegistry) GetStdoutWriter(containerID, execID string) (io.Writer, error) {
	instance, err := r.GetInstance(containerID, execID)
	if err != nil {
		return nil, err
	}
	return instance.GetStdoutWriter(), nil
}

// GetStderrWriter returns an io.Writer for streaming stderr from the exec instance.
// This is a convenience method that wraps GetInstance and GetStderrWriter.
func (r *ExecSessionRegistry) GetStderrWriter(containerID, execID string) (io.Writer, error) {
	instance, err := r.GetInstance(containerID, execID)
	if err != nil {
		return nil, err
	}
	return instance.GetStderrWriter(), nil
}

// SendExitCode sends the exit code to all clients connected to the exec instance.
// This is a convenience method that wraps GetInstance and MarkExited.
func (r *ExecSessionRegistry) SendExitCode(containerID, execID string, exitCode int32) error {
	instance, err := r.GetInstance(containerID, execID)
	if err != nil {
		return err
	}
	return instance.MarkExited(exitCode)
}

// Close stops the registry and cleans up all registered exec instances.
// This is safe to call multiple times (subsequent calls are no-ops).
func (r *ExecSessionRegistry) Close() error {
	// Stop cleanup task
	r.cleanupOnce.Do(func() {
		if r.cleanupTicker != nil {
			r.cleanupTicker.Stop()
		}
		r.cancel()
		close(r.cleanupDone)
	})

	// Clean up all instances
	r.mu.Lock()
	instances := make([]*ExecInstance, 0, len(r.instances))
	for _, instance := range r.instances {
		instances = append(instances, instance)
	}
	r.instances = make(map[string]*ExecInstance)
	r.sessionIndex = make(map[string]*ExecInstance)
	r.mu.Unlock()

	// Cleanup instances (outside lock to avoid blocking)
	var errs []error
	for _, instance := range instances {
		if err := instance.Cleanup(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during cleanup: %v", errs)
	}

	bklog.G(r.ctx).Debug("exec session registry closed")
	return nil
}

// startCleanupTask starts a background goroutine that periodically cleans up
// exited exec instances that have no connected clients.
func (r *ExecSessionRegistry) startCleanupTask() {
	r.cleanupTicker = time.NewTicker(CleanupInterval)

	go func() {
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-r.cleanupDone:
				return
			case <-r.cleanupTicker.C:
				r.cleanupExitedInstances()
			}
		}
	}()
}

// cleanupExitedInstances removes exec instances based on various timeout policies
func (r *ExecSessionRegistry) cleanupExitedInstances() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	var toCleanup []*ExecInstance
	var toDelete []string
	var toDeleteSessions []string

	for key, instance := range r.instances {
		instance.mu.RLock()
		status := instance.status
		exitTime := instance.exitTime
		instance.mu.RUnlock()

		// Get timing info
		clientCount := instance.GetClientCount()
		lastActivity := instance.GetLastActivity()
		lastDisconnect := instance.lastClientDisconnect

		var shouldCleanup bool
		var reason string

		// Policy 1: Exited sessions with no clients (output retention)
		if status == ExecStatusExited && clientCount == 0 && exitTime != nil {
			if now.Sub(*exitTime) > SessionOutputRetentionTimeout {
				shouldCleanup = true
				reason = "output retention timeout"
			}
		}

		// Policy 2: Orphaned sessions (no clients, not exited)
		if status != ExecStatusExited && clientCount == 0 {
			if !lastDisconnect.IsZero() && now.Sub(lastDisconnect) > SessionOrphanTimeout {
				shouldCleanup = true
				reason = "orphan timeout"
			}
		}

		// Policy 3: Max idle timeout (no activity for long time, and no clients)
		if clientCount == 0 && !lastActivity.IsZero() && now.Sub(lastActivity) > SessionMaxIdleTimeout {
			shouldCleanup = true
			reason = "max idle timeout"
		}

		if shouldCleanup {
			bklog.G(r.ctx).Debugf("cleaning up instance %s: %s", key, reason)
			toCleanup = append(toCleanup, instance)
			toDelete = append(toDelete, key)
			toDeleteSessions = append(toDeleteSessions, instance.GetSessionID())
		}
	}

	// Remove from both registries
	for i, key := range toDelete {
		delete(r.instances, key)
		delete(r.sessionIndex, toDeleteSessions[i])
	}

	// Unlock before cleanup to avoid holding lock during cleanup operations
	r.mu.Unlock()

	// Cleanup instances
	for _, instance := range toCleanup {
		if err := instance.Cleanup(); err != nil {
			bklog.G(r.ctx).WithError(err).Debug("error during automatic cleanup")
		}
	}

	r.mu.Lock()
}

// makeKey creates a registry key from container ID and exec ID.
func makeKey(containerID, execID string) string {
	return fmt.Sprintf("%s/%s", containerID, execID)
}

// splitKey splits a registry key into container ID and exec ID.
// Returns empty strings if the key format is invalid.
func splitKey(key string) (containerID, execID string) {
	// Find the first slash separator
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return "", ""
}

// streamWriter implements io.Writer and sends data to the appropriate channel.
// This is used to stream container output to gRPC clients via ExecInstance channels.
type streamWriter struct {
	ctx     context.Context
	channel chan []byte
	name    string // "stdout" or "stderr" for logging
}

// Write implements io.Writer interface.
// It copies the data and sends it to the channel non-blockingly.
func (w *streamWriter) Write(p []byte) (n int, err error) {
	// Don't write empty data
	if len(p) == 0 {
		return 0, nil
	}

	// Make a copy since the caller may reuse the buffer
	data := make([]byte, len(p))
	copy(data, p)

	// Non-blocking send
	select {
	case w.channel <- data:
		return len(p), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	default:
		// Channel is full, log warning but don't block
		// This matches the TeeWriter behavior
		bklog.G(w.ctx).Warnf("exec %s channel full, dropping %d bytes", w.name, len(p))
		return len(p), nil
	}
}

// historyTeeWriter writes to both the streaming channel and output history
type historyTeeWriter struct {
	instance *ExecInstance
	stream   StreamType
}

// Write implements io.Writer
func (h *historyTeeWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	// Make a copy of the data (important!)
	data := make([]byte, len(p))
	copy(data, p)

	// Write to history first (for TTY sessions)
	if h.instance.outputHistory != nil {
		if h.stream == StreamStdout {
			h.instance.outputHistory.AppendStdout(data)
		} else if h.stream == StreamStderr {
			h.instance.outputHistory.AppendStderr(data)
		}
	}

	// Then send to streaming channel (existing behavior)
	var chan_ chan []byte
	if h.stream == StreamStdout {
		chan_ = h.instance.stdoutChan
	} else if h.stream == StreamStderr {
		chan_ = h.instance.stderrChan
	} else {
		return 0, fmt.Errorf("invalid stream type: %d", h.stream)
	}

	if chan_ == nil {
		return len(p), nil
	}

	// Non-blocking send
	select {
	case chan_ <- data:
		return len(p), nil
	default:
		// Channel full, drop message
		// (This matches streamWriter behavior)
		return len(p), nil
	}
}
