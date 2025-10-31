package exec

import (
	context "context"
	"errors"
	"fmt"
	"io"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	"google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// RuncClient interface abstracts runc operations for testing.
type RuncClient interface {
	Pause(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	Kill(ctx context.Context, id string, sig int, opts *runc.KillOpts) error
}

// ContainerStateRegistry interface to avoid circular import with buildkit package.
// This interface defines the methods needed to query container state.
type ContainerStateRegistry interface {
	GetState(containerID string) (*ContainerState, error)
	Subscribe(ctx context.Context, containerID string) (<-chan ContainerLifecycleEventData, func())
}

// ContainerLifecycleEventData represents a container lifecycle event.
type ContainerLifecycleEventData struct {
	ContainerID string
	EventType   string
	Status      string
	Timestamp   time.Time
	ExitCode    *int32
	Message     string
}

// ContainerState represents the current state of a container.
// This mirrors the structure in buildkit package but avoids the circular import.
type ContainerState struct {
	ContainerID   string
	Status        string
	ExitCode      *int32
	StartedAt     time.Time
	FinishedAt    *time.Time
	ResourceUsage *ContainerResourceUsage
	LastUpdated   time.Time
}

// ContainerResourceUsage contains resource consumption metrics for a container.
type ContainerResourceUsage struct {
	CPUPercent   float64
	MemoryBytes  uint64
	MemoryLimit  uint64
	IOReadBytes  uint64
	IOWriteBytes uint64
}

// ExecAttachable implements a gRPC service for streaming container exec output.
// It follows the same pattern as TerminalAttachable but is optimized for one-way
// streaming (container -> client) rather than bidirectional terminal I/O.
//
// Thread-safety: Multiple clients can connect to the same exec session.
// Each client receives independent streams but they all receive the same data.
// Uses ExecSessionRegistry for multiplexing exec sessions across containers.
type ExecAttachable struct {
	rootCtx context.Context

	// Registry for managing multiple exec instances
	registry *ExecSessionRegistry

	// Container control operations
	runcClient    RuncClient
	stateRegistry ContainerStateRegistry

	UnimplementedExecServer
}

// NewExecAttachable creates a new ExecAttachable for streaming container exec output.
// The rootCtx is the context for the entire session lifecycle.
// Initializes the ExecSessionRegistry for managing multiple exec instances.
func NewExecAttachable(rootCtx context.Context) *ExecAttachable {
	return &ExecAttachable{
		rootCtx:  rootCtx,
		registry: NewExecSessionRegistry(rootCtx),
	}
}

// Register implements the Attachable interface, registering this service with gRPC.
func (e *ExecAttachable) Register(srv *grpc.Server) {
	RegisterExecServer(srv, e)
}

// SetStateRegistry sets the container state registry for lifecycle event streaming and status queries.
func (e *ExecAttachable) SetStateRegistry(registry ContainerStateRegistry) {
	e.stateRegistry = registry
}

// RegisterExecution creates and registers a new exec instance in the registry.
// This must be called before GetStdoutWriter/GetStderrWriter can be used.
// Returns an error if an instance with the same containerID/execID already exists.
func (e *ExecAttachable) RegisterExecution(containerID, execID string, isTTY bool) error {
	// Let the registry generate a unique session ID
	_, err := e.registry.Register(containerID, execID, "", isTTY)
	if err != nil {
		return fmt.Errorf("failed to register exec instance: %w", err)
	}
	bklog.G(e.rootCtx).Debugf("registered execution: container=%s exec=%s tty=%t", containerID, execID, isTTY)
	return nil
}

// UnregisterExecution removes an exec instance from the registry and cleans it up.
// This should be called when the exec session is completely done.
// Returns an error if the instance is not found.
func (e *ExecAttachable) UnregisterExecution(containerID, execID string) error {
	err := e.registry.Unregister(containerID, execID)
	if err != nil {
		return fmt.Errorf("failed to unregister exec instance: %w", err)
	}
	bklog.G(e.rootCtx).Debugf("unregistered execution: container=%s exec=%s", containerID, execID)
	return nil
}

// GetInstance retrieves an exec instance by container ID and exec ID.
// Returns the instance or an error if not found.
func (e *ExecAttachable) GetInstance(containerID, execID string) (*ExecInstance, error) {
	return e.registry.GetInstance(containerID, execID)
}

// GetStdoutWriter returns an io.Writer for streaming stdout from the specified exec instance.
// The exec instance must be registered first via RegisterExecution.
// Returns nil if the instance is not found.
func (e *ExecAttachable) GetStdoutWriter(containerID, execID string) io.Writer {
	writer, err := e.registry.GetStdoutWriter(containerID, execID)
	if err != nil {
		bklog.G(e.rootCtx).WithError(err).Warnf("failed to get stdout writer for %s/%s", containerID, execID)
		return nil
	}
	return writer
}

// GetStderrWriter returns an io.Writer for streaming stderr from the specified exec instance.
// The exec instance must be registered first via RegisterExecution.
// Returns nil if the instance is not found.
func (e *ExecAttachable) GetStderrWriter(containerID, execID string) io.Writer {
	writer, err := e.registry.GetStderrWriter(containerID, execID)
	if err != nil {
		bklog.G(e.rootCtx).WithError(err).Warnf("failed to get stderr writer for %s/%s", containerID, execID)
		return nil
	}
	return writer
}

// SendExitCode sends the exit code to all clients connected to the specified exec instance.
// This marks the exec instance as exited and delivers the exit code to all connected clients.
// Returns an error if the instance is not found.
func (e *ExecAttachable) SendExitCode(containerID, execID string, code int32) error {
	err := e.registry.SendExitCode(containerID, execID, code)
	if err != nil {
		return fmt.Errorf("failed to send exit code: %w", err)
	}
	bklog.G(e.rootCtx).Debugf("sent exit code %d for %s/%s", code, containerID, execID)
	return nil
}

// Session implements the bidirectional streaming RPC for exec sessions.
// Supports both new session creation and reconnection flows.
// Flow for new session:
// 1. Client sends Start without session_id
// 2. Server creates/retrieves instance and generates session_id
// 3. Server sends Ready with session_id
// 4. Server streams output and handles input
// Flow for reconnection:
// 1. Client sends Start with session_id and replay_from_sequence
// 2. Server validates session and container/exec match
// 3. Server sends Ready with session_id
// 4. Server replays history (if needed) then continues streaming
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
	ctx := srv.Context()

	// 1. Receive Start message
	req, err := srv.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to receive start message: %v", err)
	}

	startMsg := req.GetStart()
	if startMsg == nil {
		return status.Errorf(codes.InvalidArgument, "first message must be Start")
	}

	containerID := startMsg.ContainerId
	execID := startMsg.ExecId
	sessionID := startMsg.SessionId
	isTTY := startMsg.Tty

	if containerID == "" || execID == "" {
		return status.Errorf(codes.InvalidArgument, "container_id and exec_id required")
	}

	var instance *ExecInstance
	var isReconnect bool
	var replayFromSeq uint64

	// 2. Determine if this is a reconnection or new session
	if sessionID != "" {
		// Reconnection flow
		isReconnect = true
		replayFromSeq = startMsg.ReplayFromSequence

		instance, err = e.registry.GetInstanceBySessionID(sessionID)
		if err != nil {
			return status.Errorf(codes.NotFound, "session not found: %s", sessionID)
		}

		// Verify container/exec match
		if instance.containerID != containerID || instance.execID != execID {
			return status.Errorf(codes.InvalidArgument,
				"session container/exec mismatch: expected %s:%s, got %s:%s",
				instance.containerID, instance.execID, containerID, execID)
		}
	} else {
		// New session flow
		isReconnect = false
		replayFromSeq = 0

		// Check if already registered (from executor)
		instance, err = e.registry.GetInstance(containerID, execID)
		if err != nil {
			// Not yet registered, create new instance
			instance, err = e.registry.Register(containerID, execID, "", isTTY)
			if err != nil {
				return status.Errorf(codes.Internal, "failed to register execution: %v", err)
			}
		}

		sessionID = instance.GetSessionID()
	}

	// 3. Generate client ID
	clientID := startMsg.ClientId
	if clientID == "" {
		clientID = fmt.Sprintf("client-%d", time.Now().UnixNano())
	}

	// 4. Add client to instance
	err = instance.AddClient(clientID, isReconnect, replayFromSeq)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to add client: %v", err)
	}
	defer instance.RemoveClient(clientID)

	// 5. Send Ready response with session ID
	currentSize := instance.GetCurrentSize()
	ready := &Ready{
		SessionId: sessionID,
	}

	if currentSize != nil {
		ready.TerminalSize = &Resize{
			Width:  int32(currentSize.Cols),
			Height: int32(currentSize.Rows),
		}
	}

	if isReconnect {
		ready.ReplayComplete = false
	}

	err = srv.Send(&SessionResponse{
		Msg: &SessionResponse_Ready{
			Ready: ready,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to send ready: %v", err)
	}

	// 6. Handle replay for reconnection
	if isReconnect && instance.IsTTY() {
		// Replay output history
		err = e.replayOutputHistory(ctx, srv, instance, replayFromSeq)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to replay output: %v", err)
		}

		// Send replay complete message
		err = srv.Send(&SessionResponse{
			Msg: &SessionResponse_Ready{
				Ready: &Ready{
					ReplayComplete: true,
				},
			},
		})
		if err != nil {
			return status.Errorf(codes.Internal, "failed to send replay complete: %v", err)
		}
	}

	// 7. Start bidirectional streaming
	errChan := make(chan error, 2)

	// Goroutine to handle client messages (stdin, resize)
	go func() {
		errChan <- e.handleClientMessages(ctx, srv, instance)
	}()

	// Goroutine to stream output to client
	go func() {
		errChan <- e.streamOutput(ctx, srv, instance)
	}()

	// Wait for either goroutine to complete or error
	err = <-errChan

	// Cancel context to stop other goroutine
	// (context is already from srv.Context(), will be cancelled when connection closes)

	return err
}

// handleClientMessages processes incoming messages from the client.
// Handles stdin and resize requests by forwarding them to the exec instance.
func (e *ExecAttachable) handleClientMessages(ctx context.Context, srv Exec_SessionServer, instance *ExecInstance) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		req, err := srv.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil // Don't propagate errors from recv, just exit gracefully
		}

		// Handle stdin
		if stdin := req.GetStdin(); stdin != nil {
			if err := instance.WriteStdin(stdin); err != nil {
				// Log but don't fail session for backpressure
				bklog.G(ctx).WithError(err).Debug("failed to write stdin")
				continue
			}
		}

		// Handle resize
		if resize := req.GetResize(); resize != nil {
			if err := instance.SendResize(uint32(resize.Width), uint32(resize.Height)); err != nil {
				// Log but don't fail session
				bklog.G(ctx).WithError(err).Debug("failed to send resize")
				continue
			}
		}
	}
}

// streamOutput streams stdout, stderr, and exit code to the client from the exec instance.
// This runs until the session completes or the context is cancelled.
func (e *ExecAttachable) streamOutput(ctx context.Context, srv Exec_SessionServer, instance *ExecInstance) error {
	stdoutChan := instance.stdoutChan
	stderrChan := instance.stderrChan
	exitChan := instance.exitChan

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case stdout, ok := <-stdoutChan:
			if !ok {
				return nil
			}
			err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stdout{
					Stdout: stdout,
				},
			})
			if err != nil {
				return err
			}

		case stderr, ok := <-stderrChan:
			if !ok {
				return nil
			}
			err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stderr{
					Stderr: stderr,
				},
			})
			if err != nil {
				return err
			}

		case exitCode, ok := <-exitChan:
			if !ok {
				return nil
			}
			err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Exit{
					Exit: exitCode,
				},
			})
			if err != nil {
				return err
			}
			return nil // Exit code is final message
		}
	}
}

// replayOutputHistory replays buffered output to a reconnecting client
// Note: Currently uses existing Stdout/Stderr messages instead of OutputChunk
// until protobuf regeneration adds OutputChunk support
func (e *ExecAttachable) replayOutputHistory(ctx context.Context, srv Exec_SessionServer, instance *ExecInstance, fromSeq uint64) error {
	history := instance.GetOutputHistory()
	if history == nil {
		// No history available (non-TTY or history not initialized)
		return nil
	}

	// Get current sequence to know where to replay to
	currentSeq := history.GetCurrentSequence()
	if fromSeq > currentSeq {
		// Requested sequence is in the future, nothing to replay
		return nil
	}

	// Check for gaps (missed data due to buffer wrap)
	gap := history.DetectGaps(fromSeq, currentSeq)
	if gap != nil {
		// Log gap warning
		bklog.G(ctx).Warnf("replay gap detected: expected seq %d, oldest available %d (lost %d chunks)",
			gap.ExpectedStart, gap.ActualStart, gap.MissingCount)
		// Replay from oldest available
		fromSeq = gap.ActualStart
	}

	// Get historical chunks
	chunks := history.GetHistoryRange(fromSeq, currentSeq)

	// Stream chunks to client using existing Stdout/Stderr messages
	// TODO: Use OutputChunk message once protobuf is regenerated
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var err error
		if chunk.Stream == StreamStdout {
			err = srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stdout{
					Stdout: chunk.Data,
				},
			})
		} else if chunk.Stream == StreamStderr {
			err = srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stderr{
					Stderr: chunk.Data,
				},
			})
		}

		if err != nil {
			return fmt.Errorf("failed to send replay chunk seq=%d: %w", chunk.Sequence, err)
		}
	}

	return nil
}

// Close closes the registry and cleans up all exec instances.
// This should be called when the ExecAttachable is no longer needed.
func (e *ExecAttachable) Close() error {
	if e.registry != nil {
		if err := e.registry.Close(); err != nil {
			bklog.G(e.rootCtx).WithError(err).Warn("error closing exec session registry")
			return err
		}
		bklog.G(e.rootCtx).Debug("exec session registry closed")
	}
	return nil
}

// ContainerLifecycle implements the streaming RPC for container lifecycle events.
// Clients can subscribe to receive real-time notifications of container state changes
// such as started, paused, resumed, exited, etc.
func (e *ExecAttachable) ContainerLifecycle(
	req *ContainerLifecycleRequest,
	stream Exec_ContainerLifecycleServer,
) error {
	if e.stateRegistry == nil {
		return status.Errorf(codes.Unavailable, "state registry not available")
	}

	ctx := stream.Context()

	// Subscribe to lifecycle events
	eventChan, unsubscribe := e.stateRegistry.Subscribe(ctx, req.ContainerId)
	defer unsubscribe()

	bklog.G(ctx).Debugf("lifecycle event subscription started (container_id: %q)", req.ContainerId)

	// Stream events to client
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or context canceled
			bklog.G(ctx).Debug("lifecycle event stream ended: context done")
			return ctx.Err()

		case event, ok := <-eventChan:
			if !ok {
				// Channel closed, subscription ended
				bklog.G(ctx).Debug("lifecycle event stream ended: channel closed")
				return nil
			}

			// Filter by container ID if specified
			if req.ContainerId != "" && event.ContainerID != req.ContainerId {
				continue
			}

			// Convert to proto message
			resp := &ContainerLifecycleEvent{
				ContainerId: event.ContainerID,
				EventType:   string(event.EventType),
				Status:      string(event.Status),
				Timestamp:   event.Timestamp.Unix(),
				ExitCode:    0, // Default to 0
				Message:     event.Message,
			}

			// Set exit code if present
			if event.ExitCode != nil {
				resp.ExitCode = *event.ExitCode
			}

			// Send event to client
			if err := stream.Send(resp); err != nil {
				bklog.G(ctx).WithError(err).Debug("failed to send lifecycle event")
				return err
			}

			bklog.G(ctx).Debugf("sent lifecycle event: %s/%s", event.ContainerID, event.EventType)
		}
	}
}

// ContainerStatus implements the non-blocking RPC for querying container status.
// This method returns cached state from the registry immediately without blocking.
func (e *ExecAttachable) ContainerStatus(
	ctx context.Context,
	req *ContainerStatusRequest,
) (*ContainerStatusResponse, error) {
	// Validate registry availability
	if e.stateRegistry == nil {
		return nil, status.Errorf(codes.Unavailable, "state registry not available")
	}

	// Validate container ID
	if req.ContainerId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "container_id cannot be empty")
	}

	// Query state from registry (non-blocking)
	state, err := e.stateRegistry.GetState(req.ContainerId)
	if err != nil {
		// Check if container not found
		if errors.Is(err, ErrContainerNotFound) ||
			(err.Error() != "" && (errors.Is(err, fmt.Errorf("container %s not found", req.ContainerId)) ||
				err.Error() == fmt.Sprintf("container %s not found", req.ContainerId))) {
			return nil, status.Errorf(codes.NotFound, "container not found: %s", req.ContainerId)
		}
		return nil, status.Errorf(codes.Internal, "failed to get state: %v", err)
	}

	// Build response
	resp := &ContainerStatusResponse{
		ContainerId: state.ContainerID,
		Status:      state.Status,
		StartedAt:   state.StartedAt.Unix(),
	}

	// Set exit code if present
	if state.ExitCode != nil {
		resp.ExitCode = state.ExitCode
	}

	// Set finished time if present
	if state.FinishedAt != nil {
		finishedAt := state.FinishedAt.Unix()
		resp.FinishedAt = &finishedAt
	}

	// Include resource usage if requested
	if req.IncludeResourceUsage && state.ResourceUsage != nil {
		resp.ResourceUsage = &ResourceUsage{
			CpuPercent:   state.ResourceUsage.CPUPercent,
			MemoryBytes:  state.ResourceUsage.MemoryBytes,
			MemoryLimit:  state.ResourceUsage.MemoryLimit,
			IoReadBytes:  state.ResourceUsage.IOReadBytes,
			IoWriteBytes: state.ResourceUsage.IOWriteBytes,
		}
	}

	bklog.G(ctx).Debugf("container status query: %s -> %s", req.ContainerId, state.Status)
	return resp, nil
}

// ErrContainerNotFound is returned when a container is not found in the registry.
var ErrContainerNotFound = errors.New("container not found")
