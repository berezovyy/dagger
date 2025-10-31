package exec

import (
	context "context"
	"errors"
	"fmt"
	"io"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	"github.com/dagger/dagger/internal/buildkit/util/grpcerrors"
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
func (e *ExecAttachable) RegisterExecution(containerID, execID string) error {
	_, err := e.registry.Register(containerID, execID)
	if err != nil {
		return fmt.Errorf("failed to register exec instance: %w", err)
	}
	bklog.G(e.rootCtx).Debugf("registered execution: container=%s exec=%s", containerID, execID)
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
// Flow:
// 1. Client connects and sends Start request with containerID and execID
// 2. Server looks up exec instance from registry
// 3. Server sends Ready response
// 4. Server streams stdout/stderr as container produces output
// 5. Server sends exit code when container completes
// 6. Stream closes
//
// Multiple clients can connect to the same exec instance and will all receive the same output.
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
	ctx, cancel := context.WithCancelCause(srv.Context())
	defer cancel(errors.New("exec session finished"))

	// Wait for and process Start request from client
	req, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("waiting for start request: %w", err)
	}

	startMsg := req.GetStart()
	if startMsg == nil {
		return fmt.Errorf("first message must be Start request")
	}

	// Extract containerID and execID from Start message
	containerID := startMsg.ContainerId
	execID := startMsg.ExecId
	if containerID == "" {
		return status.Errorf(codes.InvalidArgument, "container_id cannot be empty")
	}
	if execID == "" {
		return status.Errorf(codes.InvalidArgument, "exec_id cannot be empty")
	}

	// Look up ExecInstance from registry
	instance, err := e.registry.GetInstance(containerID, execID)
	if err != nil {
		bklog.G(ctx).WithError(err).Debugf("exec instance not found: %s/%s", containerID, execID)
		return status.Errorf(codes.NotFound, "exec instance not found: %s/%s", containerID, execID)
	}

	// Generate a unique client ID for tracking
	clientID := fmt.Sprintf("client-%d", time.Now().UnixNano())

	// Track client connection in instance
	instance.AddClient(clientID)
	defer instance.RemoveClient(clientID)

	// Log the exec session start
	bklog.G(ctx).Debugf("exec session started: container=%s exec=%s client=%s command=%v",
		containerID, execID, clientID, startMsg.Command)

	// Send ready signal to client
	if err := e.sendReady(srv); err != nil {
		return fmt.Errorf("sending ready: %w", err)
	}

	// Start goroutines to handle client requests and stream output
	errChan := make(chan error, 2)

	// Goroutine to handle incoming client messages (stdin, resize, etc.)
	go func() {
		errChan <- e.handleClientMessages(ctx, srv, instance)
	}()

	// Goroutine to stream output to client
	go func() {
		errChan <- e.streamOutput(ctx, srv, instance)
	}()

	// Wait for either goroutine to finish or error
	err = <-errChan
	cancel(errors.New("exec session finishing"))

	// Handle errors appropriately
	if err != nil {
		if errors.Is(err, context.Canceled) || grpcerrors.Code(err) == codes.Canceled {
			// Normal cancellation
			return nil
		}
		if errors.Is(err, io.EOF) {
			// Normal stream close
			return nil
		}
		if grpcerrors.Code(err) == codes.Unavailable {
			// Client disconnected
			bklog.G(ctx).Debugf("exec session: client %s disconnected", clientID)
			return nil
		}
		return err
	}

	return nil
}

// handleClientMessages processes incoming messages from the client.
// Handles stdin and resize requests by forwarding them to the exec instance.
func (e *ExecAttachable) handleClientMessages(ctx context.Context, srv Exec_SessionServer, instance *ExecInstance) error {
	for {
		req, err := srv.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Client closed their side of the stream
				return nil
			}
			if errors.Is(err, context.Canceled) || grpcerrors.Code(err) == codes.Canceled {
				return nil
			}
			if grpcerrors.Code(err) == codes.Unavailable {
				return nil
			}
			return fmt.Errorf("receiving client message: %w", err)
		}

		// Handle different message types
		switch msg := req.GetMsg().(type) {
		case *SessionRequest_Stdin:
			// Forward stdin to container (currently not implemented in ExecInstance)
			if err := instance.WriteStdin(msg.Stdin); err != nil {
				bklog.G(ctx).WithError(err).Debug("failed to forward stdin")
			} else {
				bklog.G(ctx).Debugf("forwarded stdin: %d bytes", len(msg.Stdin))
			}

		case *SessionRequest_Resize:
			// Forward resize to container (currently not implemented in ExecInstance)
			if err := instance.SendResize(uint32(msg.Resize.Width), uint32(msg.Resize.Height)); err != nil {
				bklog.G(ctx).WithError(err).Debug("failed to forward resize")
			} else {
				bklog.G(ctx).Debugf("forwarded resize: %dx%d", msg.Resize.Width, msg.Resize.Height)
			}

		case *SessionRequest_Start:
			// Unexpected - Start should only be sent once at beginning
			bklog.G(ctx).Warn("received unexpected Start message after session started")

		default:
			bklog.G(ctx).Warnf("received unknown message type: %T", msg)
		}
	}
}

// streamOutput streams stdout, stderr, and exit code to the client from the exec instance.
// This runs until the session completes or the context is cancelled.
func (e *ExecAttachable) streamOutput(ctx context.Context, srv Exec_SessionServer, instance *ExecInstance) error {
	// Get channels from instance
	stdoutChan := instance.stdoutChan
	stderrChan := instance.stderrChan
	exitChan := instance.exitChan

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case data, ok := <-stdoutChan:
			if !ok {
				// Stdout channel closed, continue to get stderr and exit
				stdoutChan = nil
				continue
			}
			if err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stdout{
					Stdout: data,
				},
			}); err != nil {
				return fmt.Errorf("sending stdout: %w", err)
			}

		case data, ok := <-stderrChan:
			if !ok {
				// Stderr channel closed, continue to get exit
				stderrChan = nil
				continue
			}
			if err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stderr{
					Stderr: data,
				},
			}); err != nil {
				return fmt.Errorf("sending stderr: %w", err)
			}

		case exitCode, ok := <-exitChan:
			if !ok {
				// Exit channel closed without exit code
				return fmt.Errorf("exit channel closed unexpectedly")
			}
			// Send exit code to client
			if err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Exit{
					Exit: exitCode,
				},
			}); err != nil {
				return fmt.Errorf("sending exit code: %w", err)
			}
			// Exit code sent, session complete
			bklog.G(ctx).Debugf("exec session completed with exit code %d", exitCode)
			return nil
		}

		// If all channels are closed and we haven't received exit, something is wrong
		if stdoutChan == nil && stderrChan == nil && exitChan == nil {
			return fmt.Errorf("all channels closed without exit code")
		}
	}
}

// sendReady sends the Ready message to the client, indicating the session is ready.
func (e *ExecAttachable) sendReady(srv Exec_SessionServer) error {
	return srv.Send(&SessionResponse{
		Msg: &SessionResponse_Ready{
			Ready: &Ready{},
		},
	})
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
		   (err.Error() != "" && (
		       errors.Is(err, fmt.Errorf("container %s not found", req.ContainerId)) ||
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
