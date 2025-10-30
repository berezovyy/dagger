package buildkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	"github.com/google/uuid"
)

// ContainerStatus represents the lifecycle state of a container.
type ContainerStatus string

const (
	// ContainerStatusCreated means container has been created but not started.
	ContainerStatusCreated ContainerStatus = "created"

	// ContainerStatusRunning means container is currently executing.
	ContainerStatusRunning ContainerStatus = "running"

	// ContainerStatusPaused means container execution is paused.
	ContainerStatusPaused ContainerStatus = "paused"

	// ContainerStatusStopped means container was stopped gracefully.
	ContainerStatusStopped ContainerStatus = "stopped"

	// ContainerStatusExited means container has exited.
	ContainerStatusExited ContainerStatus = "exited"

	// ContainerStatusUnknown means container state is unknown.
	ContainerStatusUnknown ContainerStatus = "unknown"
)

// ContainerState represents the current state of a container.
type ContainerState struct {
	// ContainerID is the unique identifier for this container.
	ContainerID string

	// Status is the current lifecycle state.
	Status ContainerStatus

	// ExitCode is the container's exit code if it has exited.
	// Nil if not yet exited.
	ExitCode *int32

	// StartedAt is when the container started running.
	// Will be zero if not yet started.
	StartedAt time.Time

	// FinishedAt is when the container exited.
	// Nil if not yet exited.
	FinishedAt *time.Time

	// ResourceUsage contains the latest resource usage metrics.
	ResourceUsage *ResourceUsage

	// LastUpdated is when this state was last updated.
	LastUpdated time.Time
}

// ResourceUsage contains resource consumption metrics for a container.
type ResourceUsage struct {
	// CPUPercent is CPU usage as a percentage (0-100 per core).
	CPUPercent float64

	// MemoryBytes is current memory usage in bytes.
	MemoryBytes uint64

	// MemoryLimit is the memory limit in bytes.
	// Will be 0 if no limit is set.
	MemoryLimit uint64

	// IOReadBytes is total bytes read from disk.
	IOReadBytes uint64

	// IOWriteBytes is total bytes written to disk.
	IOWriteBytes uint64
}

// EventType represents the type of lifecycle event.
type EventType string

const (
	// EventTypeRegistered indicates a container was registered.
	EventTypeRegistered EventType = "registered"

	// EventTypeStarted indicates a container started running.
	EventTypeStarted EventType = "started"

	// EventTypePaused indicates a container was paused.
	EventTypePaused EventType = "paused"

	// EventTypeResumed indicates a container resumed from pause.
	EventTypeResumed EventType = "resumed"

	// EventTypeExited indicates a container exited.
	EventTypeExited EventType = "exited"

	// EventTypeUnregistered indicates a container was unregistered.
	EventTypeUnregistered EventType = "unregistered"
)

// LifecycleEvent represents a container lifecycle event.
type LifecycleEvent struct {
	// ContainerID is the unique identifier for the container.
	ContainerID string

	// EventType is the type of event that occurred.
	EventType EventType

	// Status is the current container status after the event.
	Status ContainerStatus

	// Timestamp is when the event occurred.
	Timestamp time.Time

	// ExitCode is the container's exit code (for exited events).
	// Nil if not applicable.
	ExitCode *int32

	// Message is a human-readable description of the event.
	Message string
}

// ContainerStateRegistry tracks the state of all running and recently completed containers.
// It provides non-blocking queries by maintaining a cached view of container states.
// All methods are thread-safe and can be called concurrently.
type ContainerStateRegistry struct {
	// mu protects concurrent access to states map
	mu sync.RWMutex

	// states maps container ID to current state.
	// Protected by mu for concurrent access.
	states map[string]*ContainerState

	// ctx is the registry lifecycle context
	ctx context.Context

	// cancel stops background operations
	cancel context.CancelFunc

	// closed tracks whether Close has been called
	closed atomic.Bool

	// Background updater fields
	wg             sync.WaitGroup
	updateTicker   *time.Ticker
	updateInterval time.Duration

	// Runc/buildkit integration
	runcClient     *runc.Runc
	cgroupParent   string
	cgroupMountpoint string

	// Event broadcasting
	eventSubs   map[string]chan *LifecycleEvent // Subscriber ID -> event channel
	eventSubsMu sync.RWMutex
}

// RegistryOption configures the ContainerStateRegistry.
type RegistryOption func(*ContainerStateRegistry)

// WithRuncClient sets the runc client for querying container state.
func WithRuncClient(runcClient *runc.Runc) RegistryOption {
	return func(r *ContainerStateRegistry) {
		r.runcClient = runcClient
	}
}

// WithCgroupParent sets the cgroup parent path for querying resource usage.
func WithCgroupParent(cgroupParent string) RegistryOption {
	return func(r *ContainerStateRegistry) {
		r.cgroupParent = cgroupParent
	}
}

// WithUpdateInterval sets how often to poll for container state updates.
func WithUpdateInterval(interval time.Duration) RegistryOption {
	return func(r *ContainerStateRegistry) {
		r.updateInterval = interval
	}
}

// NewContainerStateRegistry creates a new registry.
// The registry does not start any background goroutines automatically.
// Call Start() to begin background operations.
func NewContainerStateRegistry(ctx context.Context, opts ...RegistryOption) *ContainerStateRegistry {
	ctx, cancel := context.WithCancel(ctx)

	registry := &ContainerStateRegistry{
		states:           make(map[string]*ContainerState),
		ctx:              ctx,
		cancel:           cancel,
		updateInterval:   2 * time.Second,        // Default: update every 2 seconds
		cgroupMountpoint: "/sys/fs/cgroup",       // Default cgroup v2 mountpoint
		eventSubs:        make(map[string]chan *LifecycleEvent),
	}

	// Apply options
	for _, opt := range opts {
		opt(registry)
	}

	return registry
}

// Register adds a new container to the registry with an initial state.
// Returns an error if the container is already registered.
func (r *ContainerStateRegistry) Register(containerID string, initialState ContainerState) error {
	if containerID == "" {
		return fmt.Errorf("containerID cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.states[containerID]; exists {
		return fmt.Errorf("container %s is already registered", containerID)
	}

	// Make a copy and ensure it has the correct ID and timestamp
	state := initialState
	state.ContainerID = containerID
	state.LastUpdated = time.Now()

	r.states[containerID] = &state

	// Publish registration event
	r.publishEvent(&LifecycleEvent{
		ContainerID: containerID,
		EventType:   EventTypeRegistered,
		Status:      initialState.Status,
		Timestamp:   time.Now(),
		Message:     fmt.Sprintf("Container %s registered", containerID),
	})

	return nil
}

// Unregister removes a container from the registry.
// Returns an error if the container is not found.
func (r *ContainerStateRegistry) Unregister(containerID string) error {
	if containerID == "" {
		return fmt.Errorf("containerID cannot be empty")
	}

	r.mu.Lock()
	state, exists := r.states[containerID]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("container %s not found", containerID)
	}

	delete(r.states, containerID)
	r.mu.Unlock()

	// Publish unregistration event
	r.publishEvent(&LifecycleEvent{
		ContainerID: containerID,
		EventType:   EventTypeUnregistered,
		Status:      state.Status,
		Timestamp:   time.Now(),
		Message:     fmt.Sprintf("Container %s unregistered", containerID),
	})

	return nil
}

// GetState returns the current cached state of a container.
// This is a non-blocking operation that returns immediately.
// Returns a copy of the state to prevent external modification.
func (r *ContainerStateRegistry) GetState(containerID string) (*ContainerState, error) {
	if containerID == "" {
		return nil, fmt.Errorf("containerID cannot be empty")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	state, exists := r.states[containerID]
	if !exists {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	return copyState(state), nil
}

// GetAllStates returns a map of all container states.
// Returns copies of all states to prevent external modification.
func (r *ContainerStateRegistry) GetAllStates() map[string]*ContainerState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*ContainerState, len(r.states))
	for id, state := range r.states {
		result[id] = copyState(state)
	}
	return result
}

// ListContainerIDs returns a list of all registered container IDs.
func (r *ContainerStateRegistry) ListContainerIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.states))
	for id := range r.states {
		ids = append(ids, id)
	}
	return ids
}

// UpdateState updates a container's state using an updater function.
// The updater function receives a pointer to the state and can modify it.
// The LastUpdated timestamp is automatically updated after the function returns.
// Returns an error if the container is not found.
func (r *ContainerStateRegistry) UpdateState(containerID string, updater func(*ContainerState)) error {
	if containerID == "" {
		return fmt.Errorf("containerID cannot be empty")
	}
	if updater == nil {
		return fmt.Errorf("updater function cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, exists := r.states[containerID]
	if !exists {
		return fmt.Errorf("container %s not found", containerID)
	}

	updater(state)
	state.LastUpdated = time.Now()
	return nil
}

// UpdateResourceUsage updates the resource usage for a container.
// This is a convenience method that wraps UpdateState.
func (r *ContainerStateRegistry) UpdateResourceUsage(containerID string, usage ResourceUsage) error {
	return r.UpdateState(containerID, func(state *ContainerState) {
		if state.ResourceUsage == nil {
			state.ResourceUsage = &ResourceUsage{}
		}
		*state.ResourceUsage = usage
	})
}

// MarkExited marks a container as exited with the given exit code.
// It sets the status to ContainerStatusExited, records the exit code and finish time.
// If the container is already marked as exited, this is a no-op.
func (r *ContainerStateRegistry) MarkExited(containerID string, exitCode int32) error {
	return r.UpdateState(containerID, func(state *ContainerState) {
		// Only update if not already exited
		if state.Status != ContainerStatusExited {
			state.Status = ContainerStatusExited
			state.ExitCode = &exitCode
			now := time.Now()
			state.FinishedAt = &now
		}
	})
}

// Start starts any background operations.
// Launches a background goroutine that periodically queries runc for container state
// and updates the registry with fresh data.
func (r *ContainerStateRegistry) Start() error {
	if r.closed.Load() {
		return fmt.Errorf("registry is closed")
	}

	// Only start updater if we have a runc client configured
	if r.runcClient != nil {
		r.updateTicker = time.NewTicker(r.updateInterval)

		r.wg.Add(1)
		go r.updateLoop()

		bklog.G(r.ctx).Debugf("Container state registry updater started (interval: %v)", r.updateInterval)
	}

	return nil
}

// Stop stops any background operations gracefully.
// Stops the update ticker and cancels the context to signal shutdown.
func (r *ContainerStateRegistry) Stop() error {
	if r.updateTicker != nil {
		r.updateTicker.Stop()
	}

	// Cancel context to signal shutdown
	r.cancel()

	// Wait for background goroutines to finish
	r.wg.Wait()

	return nil
}

// Close stops all background operations and releases resources.
// This method is idempotent and can be called multiple times safely.
// After Close is called, the registry should not be used.
func (r *ContainerStateRegistry) Close() error {
	// Ensure Close is only executed once
	if !r.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	// Stop ticker if running
	if r.updateTicker != nil {
		r.updateTicker.Stop()
	}

	// Cancel the context to signal shutdown
	r.cancel()

	// Wait for background goroutines to finish
	r.wg.Wait()

	return nil
}

// Size returns the number of containers currently tracked by the registry.
func (r *ContainerStateRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.states)
}

// Subscribe creates a subscription to lifecycle events.
// If containerID is non-empty, only events for that container are returned.
// If containerID is empty, all container events are returned.
// Returns a channel for receiving events and an unsubscribe function.
// The unsubscribe function should be called when done to clean up resources.
// Note: This method returns LifecycleEvent which can be type-asserted to the
// exec.ContainerLifecycleEventData interface.
func (r *ContainerStateRegistry) Subscribe(ctx context.Context, containerID string) (<-chan *LifecycleEvent, func()) {
	eventChan := make(chan *LifecycleEvent, 100) // Buffered to prevent blocking

	subID := uuid.New().String()

	r.eventSubsMu.Lock()
	r.eventSubs[subID] = eventChan
	r.eventSubsMu.Unlock()

	// Cleanup function
	unsubscribe := func() {
		r.eventSubsMu.Lock()
		delete(r.eventSubs, subID)
		r.eventSubsMu.Unlock()
		close(eventChan)
	}

	// If context is canceled, automatically unsubscribe
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return eventChan, unsubscribe
}

// publishEvent publishes a lifecycle event to all subscribers.
// This is a best-effort operation - if a subscriber's channel is full,
// the event is dropped with a warning log.
func (r *ContainerStateRegistry) publishEvent(event *LifecycleEvent) {
	r.eventSubsMu.RLock()
	defer r.eventSubsMu.RUnlock()

	for _, ch := range r.eventSubs {
		select {
		case ch <- event:
			// Sent successfully
		default:
			// Channel full, drop event with warning
			bklog.G(r.ctx).Warnf("lifecycle event dropped: subscriber slow (event: %s, container: %s)",
				event.EventType, event.ContainerID)
		}
	}
}

// Cleanup removes containers that exited more than retentionPeriod ago.
// Returns the number of containers removed.
func (r *ContainerStateRegistry) Cleanup(retentionPeriod time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	removed := 0

	for containerID, state := range r.states {
		// Only cleanup exited containers
		if state.Status == ContainerStatusExited && state.FinishedAt != nil {
			if now.Sub(*state.FinishedAt) > retentionPeriod {
				delete(r.states, containerID)
				removed++
			}
		}
	}

	return removed
}

// copyState creates a deep copy of a container state.
// This prevents external code from modifying the internal state.
func copyState(s *ContainerState) *ContainerState {
	if s == nil {
		return nil
	}

	copy := *s

	// Deep copy pointer fields
	if s.ExitCode != nil {
		exitCode := *s.ExitCode
		copy.ExitCode = &exitCode
	}

	if s.FinishedAt != nil {
		finishedAt := *s.FinishedAt
		copy.FinishedAt = &finishedAt
	}

	if s.ResourceUsage != nil {
		usage := *s.ResourceUsage
		copy.ResourceUsage = &usage
	}

	return &copy
}

// updateLoop periodically queries runc for container states and updates the registry.
// This runs in a background goroutine started by Start().
func (r *ContainerStateRegistry) updateLoop() {
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			// Context canceled, shutdown requested
			return
		case <-r.updateTicker.C:
			// Time to update all container states
			r.updateAllContainers()
		}
	}
}

// updateAllContainers queries runc and cgroups for all registered containers.
// Updates are done without holding the registry lock to avoid blocking queries.
func (r *ContainerStateRegistry) updateAllContainers() {
	// Get snapshot of container IDs to update (with read lock)
	r.mu.RLock()
	containerIDs := make([]string, 0, len(r.states))
	for id, state := range r.states {
		// Only update containers that are not in a terminal state
		if state.Status != ContainerStatusExited && state.Status != ContainerStatusStopped {
			containerIDs = append(containerIDs, id)
		}
	}
	r.mu.RUnlock()

	// Update each container (outside the lock to avoid blocking)
	for _, containerID := range containerIDs {
		if err := r.updateContainer(containerID); err != nil {
			// Log warning but continue with other containers
			bklog.G(r.ctx).WithError(err).
				WithField("container", containerID).
				Debug("failed to update container state")
		}
	}
}

// updateContainer queries runc for a single container's state and updates the registry.
func (r *ContainerStateRegistry) updateContainer(containerID string) error {
	// Query runc for container state
	ctx, cancel := context.WithTimeout(r.ctx, 1*time.Second)
	defer cancel()

	runcState, err := r.runcClient.State(ctx, containerID)
	if err != nil {
		// Container may have exited or been deleted
		return r.handleContainerGone(containerID, err)
	}

	// Query cgroups for resource usage (best effort)
	var resourceUsage *ResourceUsage
	if r.cgroupParent != "" {
		cgroupPath := r.buildCgroupPath(containerID)
		resourceUsage, err = r.queryCgroupUsage(ctx, cgroupPath)
		if err != nil {
			// Log but don't fail - resource stats are optional
			bklog.G(ctx).WithError(err).
				WithField("container", containerID).
				Debug("failed to query cgroup usage")
		}
	}

	// Get current state before update to detect transitions
	oldState, err := r.GetState(containerID)
	if err != nil {
		return err
	}

	// Update registry with fresh state (acquire lock only for update)
	err = r.UpdateState(containerID, func(state *ContainerState) {
		// Map runc status to our ContainerStatus enum
		newStatus := mapRuncStatusToContainerStatus(runcState.Status)

		// Track status transitions
		oldStatus := state.Status
		state.Status = newStatus

		// If container just started, record start time
		if oldStatus == ContainerStatusCreated && newStatus == ContainerStatusRunning {
			if state.StartedAt.IsZero() {
				state.StartedAt = time.Now()
			}
		}

		// If container exited, record finish time
		if (oldStatus == ContainerStatusRunning || oldStatus == ContainerStatusCreated) &&
			(newStatus == ContainerStatusExited || newStatus == ContainerStatusStopped) {
			if state.FinishedAt == nil {
				now := time.Now()
				state.FinishedAt = &now
			}
		}

		// Update resource usage if available
		if resourceUsage != nil {
			state.ResourceUsage = resourceUsage
		}
	})

	if err != nil {
		return err
	}

	// Get updated state to publish event
	newState, err := r.GetState(containerID)
	if err != nil {
		return err
	}

	// Detect and publish state transitions
	if oldState.Status != newState.Status {
		eventType := EventTypeStarted // default

		switch {
		case oldState.Status != ContainerStatusRunning && newState.Status == ContainerStatusRunning:
			if oldState.Status == ContainerStatusPaused {
				eventType = EventTypeResumed
			} else {
				eventType = EventTypeStarted
			}
		case newState.Status == ContainerStatusPaused:
			eventType = EventTypePaused
		case newState.Status == ContainerStatusExited || newState.Status == ContainerStatusStopped:
			eventType = EventTypeExited
		}

		r.publishEvent(&LifecycleEvent{
			ContainerID: containerID,
			EventType:   eventType,
			Status:      newState.Status,
			Timestamp:   time.Now(),
			ExitCode:    newState.ExitCode,
			Message:     fmt.Sprintf("Container %s: %s -> %s", containerID, oldState.Status, newState.Status),
		})
	}

	return nil
}

// handleContainerGone marks a container as exited when runc reports it doesn't exist.
func (r *ContainerStateRegistry) handleContainerGone(containerID string, err error) error {
	// Check if error indicates container doesn't exist
	if strings.Contains(err.Error(), "does not exist") ||
	   strings.Contains(err.Error(), "no such file or directory") {
		// Mark container as exited
		updateErr := r.UpdateState(containerID, func(state *ContainerState) {
			if state.Status != ContainerStatusExited {
				state.Status = ContainerStatusExited
				if state.FinishedAt == nil {
					now := time.Now()
					state.FinishedAt = &now
				}
			}
		})

		// If container not found in registry, that's ok
		if updateErr != nil && strings.Contains(updateErr.Error(), "not found") {
			return nil
		}
		return updateErr
	}

	// Other errors - return them
	return err
}

// buildCgroupPath constructs the full cgroup path for a container.
func (r *ContainerStateRegistry) buildCgroupPath(containerID string) string {
	// Container cgroup is typically: {cgroupParent}/{containerID}
	return filepath.Join(r.cgroupParent, containerID)
}

// queryCgroupUsage reads resource usage from cgroup files.
// Returns nil if cgroup files don't exist (graceful degradation).
func (r *ContainerStateRegistry) queryCgroupUsage(ctx context.Context, cgroupPath string) (*ResourceUsage, error) {
	usage := &ResourceUsage{}
	fullPath := filepath.Join(r.cgroupMountpoint, cgroupPath)

	// Read CPU usage from cpu.stat
	cpuUsage, err := r.readCPUStat(fullPath)
	if err != nil {
		// If file doesn't exist, return nil (graceful degradation)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cpu stat: %w", err)
	}
	usage.CPUPercent = cpuUsage

	// Read memory usage from memory.current
	memCurrent, err := r.readMemoryCurrent(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory current: %w", err)
	}
	usage.MemoryBytes = memCurrent

	// Read memory limit from memory.max (optional)
	memLimit, err := r.readMemoryMax(fullPath)
	if err == nil {
		usage.MemoryLimit = memLimit
	}

	// Read I/O stats from io.stat
	ioReadBytes, ioWriteBytes, err := r.readIOStat(fullPath)
	if err == nil {
		usage.IOReadBytes = ioReadBytes
		usage.IOWriteBytes = ioWriteBytes
	}

	return usage, nil
}

// readCPUStat reads CPU usage from cpu.stat file.
// Returns CPU usage as a percentage (0-100 per core).
func (r *ContainerStateRegistry) readCPUStat(cgroupPath string) (float64, error) {
	cpuStatPath := filepath.Join(cgroupPath, "cpu.stat")
	data, err := os.ReadFile(cpuStatPath)
	if err != nil {
		return 0, err
	}

	// Parse cpu.stat for usage_usec
	// Format: usage_usec 12345678
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "usage_usec" {
			if value, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				// Convert microseconds to a simple value
				// Note: CPU percentage calculation would require tracking delta over time
				// For now, just return the raw microseconds value as a float
				// A proper percentage would need: (delta_cpu_usec / delta_time_usec) * 100
				return float64(value) / 10000.0, nil // Rough approximation
			}
		}
	}

	return 0, fmt.Errorf("usage_usec not found in cpu.stat")
}

// readMemoryCurrent reads current memory usage from memory.current file.
func (r *ContainerStateRegistry) readMemoryCurrent(cgroupPath string) (uint64, error) {
	memCurrentPath := filepath.Join(cgroupPath, "memory.current")
	data, err := os.ReadFile(memCurrentPath)
	if err != nil {
		return 0, err
	}

	valueStr := strings.TrimSpace(string(data))
	value, err := strconv.ParseInt(valueStr, 10, 64)
	if err != nil {
		return 0, err
	}

	return uint64(value), nil
}

// readMemoryMax reads memory limit from memory.max file.
func (r *ContainerStateRegistry) readMemoryMax(cgroupPath string) (uint64, error) {
	memMaxPath := filepath.Join(cgroupPath, "memory.max")
	data, err := os.ReadFile(memMaxPath)
	if err != nil {
		return 0, err
	}

	// memory.max can be "max" (unlimited) or a number
	valueStr := strings.TrimSpace(string(data))
	if valueStr == "max" {
		return 0, nil // 0 indicates unlimited
	}

	value, err := strconv.ParseInt(valueStr, 10, 64)
	if err != nil {
		return 0, err
	}

	return uint64(value), nil
}

// readIOStat reads I/O statistics from io.stat file.
func (r *ContainerStateRegistry) readIOStat(cgroupPath string) (readBytes, writeBytes uint64, err error) {
	ioStatPath := filepath.Join(cgroupPath, "io.stat")
	data, err := os.ReadFile(ioStatPath)
	if err != nil {
		return 0, 0, err
	}

	// Parse io.stat for rbytes and wbytes
	// Format: 8:0 rbytes=1048576 wbytes=2097152 rios=256 wios=512
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Skip device ID (first field), parse key=value pairs
		for _, field := range fields[1:] {
			parts := strings.Split(field, "=")
			if len(parts) != 2 {
				continue
			}
			key := parts[0]
			value, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				continue
			}
			switch key {
			case "rbytes":
				readBytes += value
			case "wbytes":
				writeBytes += value
			}
		}
	}

	return readBytes, writeBytes, nil
}

// mapRuncStatusToContainerStatus converts runc status string to ContainerStatus.
func mapRuncStatusToContainerStatus(runcStatus string) ContainerStatus {
	switch runcStatus {
	case "created":
		return ContainerStatusCreated
	case "running":
		return ContainerStatusRunning
	case "paused":
		return ContainerStatusPaused
	case "stopped":
		return ContainerStatusStopped
	default:
		// Treat unknown statuses as exited
		return ContainerStatusExited
	}
}
