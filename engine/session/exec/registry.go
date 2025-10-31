package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// ExecInstance represents a single container exec session that can be shared
// across multiple gRPC client connections. It manages the lifecycle of stdout/stderr
// streaming and exit code delivery.
type ExecInstance struct {
	// Immutable fields (set at creation)
	containerID string
	execID      string
	rootCtx     context.Context
	cancel      context.CancelFunc

	// Status tracking (protected by mu)
	mu       sync.RWMutex
	status   ExecStatus
	exitCode *int32
	exitTime *time.Time

	// Client connection tracking (protected by mu)
	clients map[string]struct{} // client ID -> empty struct

	// I/O channels for streaming
	stdoutChan chan []byte
	stderrChan chan []byte
	exitChan   chan int32

	// Cleanup state
	closeOnce sync.Once
	closed    bool
}

// NewExecInstance creates a new exec instance for the given container and exec ID.
// The rootCtx is the context for the entire exec lifecycle.
func NewExecInstance(ctx context.Context, containerID, execID string) *ExecInstance {
	ctx, cancel := context.WithCancel(ctx)

	return &ExecInstance{
		containerID: containerID,
		execID:      execID,
		rootCtx:     ctx,
		cancel:      cancel,
		status:      ExecStatusPending,
		clients:     make(map[string]struct{}),
		stdoutChan:  make(chan []byte, 100), // Buffered to handle bursts
		stderrChan:  make(chan []byte, 100),
		exitChan:    make(chan int32, 1), // Buffered since exit code is sent once
	}
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
// Returns the number of currently connected clients.
func (e *ExecInstance) AddClient(clientID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.clients[clientID] = struct{}{}

	// Mark as running when first client connects
	if e.status == ExecStatusPending {
		e.status = ExecStatusRunning
		bklog.G(e.rootCtx).Debugf("exec %s/%s started (first client connected)", e.containerID, e.execID)
	}

	bklog.G(e.rootCtx).Debugf("client %s connected to exec %s/%s (total: %d)",
		clientID, e.containerID, e.execID, len(e.clients))
	return len(e.clients)
}

// RemoveClient unregisters a client connection from this exec instance.
// Returns the number of remaining connected clients.
func (e *ExecInstance) RemoveClient(clientID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.clients, clientID)
	remaining := len(e.clients)

	bklog.G(e.rootCtx).Debugf("client %s disconnected from exec %s/%s (remaining: %d)",
		clientID, e.containerID, e.execID, remaining)
	return remaining
}

// WriteStdin writes data to the exec instance's stdin.
// Currently not implemented but reserved for future TTY support.
func (e *ExecInstance) WriteStdin(data []byte) error {
	// TODO: Implement stdin forwarding when TTY support is added
	return errors.New("stdin forwarding not yet implemented")
}

// SendResize sends a terminal resize event to the exec instance.
// Currently not implemented but reserved for future TTY support.
func (e *ExecInstance) SendResize(width, height uint32) error {
	// TODO: Implement resize forwarding when TTY support is added
	return errors.New("resize forwarding not yet implemented")
}

// GetStdoutWriter returns an io.Writer that streams stdout to connected clients.
// This writer is thread-safe and non-blocking.
func (e *ExecInstance) GetStdoutWriter() io.Writer {
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stdoutChan,
		name:    "stdout",
	}
}

// GetStderrWriter returns an io.Writer that streams stderr to connected clients.
// This writer is thread-safe and non-blocking.
func (e *ExecInstance) GetStderrWriter() io.Writer {
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stderrChan,
		name:    "stderr",
	}
}

// Cleanup closes all channels and releases resources.
// This is safe to call multiple times (subsequent calls are no-ops).
// It should be called when the exec instance is no longer needed.
func (e *ExecInstance) Cleanup() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		// Mark as closed
		e.closed = true

		// Cancel context to signal shutdown
		e.cancel()

		// Close all channels to signal end of stream
		close(e.stdoutChan)
		close(e.stderrChan)
		close(e.exitChan)

		bklog.G(e.rootCtx).Debugf("exec instance %s/%s cleaned up (clients: %d)",
			e.containerID, e.execID, len(e.clients))
	})

	return nil
}

// ExecSessionRegistry manages multiple concurrent exec instances for different containers.
// It provides multiplexing so multiple clients can attach to the same exec session.
// All methods are thread-safe.
type ExecSessionRegistry struct {
	// mu protects concurrent access to instances map
	mu sync.RWMutex

	// instances maps "containerID/execID" to ExecInstance
	instances map[string]*ExecInstance

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
		instances:   make(map[string]*ExecInstance),
		ctx:         ctx,
		cancel:      cancel,
		cleanupDone: make(chan struct{}),
	}

	// Start background cleanup task
	registry.startCleanupTask()

	return registry
}

// Register creates and registers a new exec instance.
// Returns an error if an instance with the same containerID/execID already exists.
func (r *ExecSessionRegistry) Register(containerID, execID string) (*ExecInstance, error) {
	if containerID == "" {
		return nil, errors.New("containerID cannot be empty")
	}
	if execID == "" {
		return nil, errors.New("execID cannot be empty")
	}

	key := makeKey(containerID, execID)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already registered
	if _, exists := r.instances[key]; exists {
		return nil, fmt.Errorf("exec instance %s already registered", key)
	}

	// Create new instance
	instance := NewExecInstance(r.ctx, containerID, execID)
	r.instances[key] = instance

	bklog.G(r.ctx).Debugf("registered exec instance: %s (total: %d)", key, len(r.instances))
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

	delete(r.instances, key)
	r.mu.Unlock()

	// Clean up instance (outside lock to avoid blocking)
	if err := instance.Cleanup(); err != nil {
		bklog.G(r.ctx).WithError(err).Warnf("error cleaning up exec instance %s", key)
	}

	bklog.G(r.ctx).Debugf("unregistered exec instance: %s (remaining: %d)", key, len(r.instances)-1)
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
	r.cleanupTicker = time.NewTicker(30 * time.Second)

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

// cleanupExitedInstances removes exec instances that have exited and have no connected clients.
// Instances are kept for 5 minutes after exit to allow late-arriving clients to retrieve the exit code.
func (r *ExecSessionRegistry) cleanupExitedInstances() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	retention := 5 * time.Minute

	var toCleanup []*ExecInstance
	var toDelete []string

	for key, instance := range r.instances {
		instance.mu.RLock()
		status := instance.status
		exitTime := instance.exitTime
		clientCount := len(instance.clients)
		instance.mu.RUnlock()

		// Cleanup instances that:
		// 1. Have exited
		// 2. Have no connected clients
		// 3. Exited more than retention period ago
		if status == ExecStatusExited && clientCount == 0 && exitTime != nil {
			if now.Sub(*exitTime) > retention {
				toCleanup = append(toCleanup, instance)
				toDelete = append(toDelete, key)
			}
		}
	}

	// Remove from registry
	for _, key := range toDelete {
		delete(r.instances, key)
	}

	// Unlock before cleanup to avoid holding lock during cleanup operations
	r.mu.Unlock()

	// Cleanup instances
	for _, instance := range toCleanup {
		if err := instance.Cleanup(); err != nil {
			bklog.G(r.ctx).WithError(err).Debug("error during automatic cleanup")
		}
	}

	if len(toCleanup) > 0 {
		bklog.G(r.ctx).Debugf("cleaned up %d exited exec instances", len(toCleanup))
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
