package exec

import (
	context "context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	"github.com/dagger/dagger/internal/buildkit/util/grpcerrors"
	"google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

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
type ExecAttachable struct {
	rootCtx context.Context

	// Channels for streaming data from container to clients
	stdoutChan chan []byte
	stderrChan chan []byte
	exitChan   chan int32

	// Session lifecycle management
	mu           sync.Mutex
	sessionReady chan struct{} // Closed when first client connects
	sessionDone  chan struct{} // Closed when session completes
	clients      int           // Number of connected clients

	// Ensure channels are closed only once
	closeOnce sync.Once

	// Container control operations
	runcClient    *runc.Runc
	stateRegistry ContainerStateRegistry

	UnimplementedExecServer
}

// NewExecAttachable creates a new ExecAttachable for streaming container exec output.
// The rootCtx is the context for the entire session lifecycle.
func NewExecAttachable(rootCtx context.Context) *ExecAttachable {
	return &ExecAttachable{
		rootCtx:      rootCtx,
		stdoutChan:   make(chan []byte, 100), // Buffered to handle bursts
		stderrChan:   make(chan []byte, 100),
		exitChan:     make(chan int32, 1), // Buffered since exit code is sent once
		sessionReady: make(chan struct{}),
		sessionDone:  make(chan struct{}),
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

// Session implements the bidirectional streaming RPC for exec sessions.
// Flow:
// 1. Client connects and sends Start request with exec parameters
// 2. Server sends Ready response
// 3. Server streams stdout/stderr as container produces output
// 4. Server sends exit code when container completes
// 5. Stream closes
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
	ctx, cancel := context.WithCancelCause(srv.Context())
	defer cancel(errors.New("exec session finished"))

	// Track client connection
	e.mu.Lock()
	e.clients++
	firstClient := e.clients == 1
	e.mu.Unlock()

	// Decrement client count on exit
	defer func() {
		e.mu.Lock()
		e.clients--
		e.mu.Unlock()
	}()

	// Wait for and process Start request from client
	req, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("waiting for start request: %w", err)
	}

	startMsg := req.GetStart()
	if startMsg == nil {
		return fmt.Errorf("first message must be Start request")
	}

	// Log the exec session start
	bklog.G(ctx).Debugf("exec session started: container=%s command=%v",
		startMsg.ContainerId, startMsg.Command)

	// Send ready signal to client
	if err := e.sendReady(srv); err != nil {
		return fmt.Errorf("sending ready: %w", err)
	}

	// Signal that session is ready (for first client only)
	if firstClient {
		close(e.sessionReady)
	}

	// Start goroutines to handle client requests and stream output
	errChan := make(chan error, 2)

	// Goroutine to handle incoming client messages (stdin, resize, etc.)
	go func() {
		errChan <- e.handleClientMessages(ctx, srv)
	}()

	// Goroutine to stream output to client
	go func() {
		errChan <- e.streamOutput(ctx, srv)
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
			bklog.G(ctx).Debug("exec session: client disconnected")
			return nil
		}
		return err
	}

	return nil
}

// handleClientMessages processes incoming messages from the client.
// Currently handles stdin and resize requests.
func (e *ExecAttachable) handleClientMessages(ctx context.Context, srv Exec_SessionServer) error {
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
			// TODO: Forward stdin to container if TTY is enabled
			bklog.G(ctx).Debugf("received stdin: %d bytes", len(msg.Stdin))

		case *SessionRequest_Resize:
			// TODO: Resize container TTY if enabled
			bklog.G(ctx).Debugf("received resize: %dx%d", msg.Resize.Width, msg.Resize.Height)

		case *SessionRequest_Start:
			// Unexpected - Start should only be sent once at beginning
			bklog.G(ctx).Warn("received unexpected Start message after session started")

		default:
			bklog.G(ctx).Warnf("received unknown message type: %T", msg)
		}
	}
}

// streamOutput streams stdout, stderr, and exit code to the client.
// This runs until the session completes or the context is cancelled.
func (e *ExecAttachable) streamOutput(ctx context.Context, srv Exec_SessionServer) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case data, ok := <-e.stdoutChan:
			if !ok {
				// Stdout channel closed, continue to get stderr and exit
				e.stdoutChan = nil
				continue
			}
			if err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stdout{
					Stdout: data,
				},
			}); err != nil {
				return fmt.Errorf("sending stdout: %w", err)
			}

		case data, ok := <-e.stderrChan:
			if !ok {
				// Stderr channel closed, continue to get exit
				e.stderrChan = nil
				continue
			}
			if err := srv.Send(&SessionResponse{
				Msg: &SessionResponse_Stderr{
					Stderr: data,
				},
			}); err != nil {
				return fmt.Errorf("sending stderr: %w", err)
			}

		case exitCode, ok := <-e.exitChan:
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
		if e.stdoutChan == nil && e.stderrChan == nil && e.exitChan == nil {
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

// streamWriter implements io.Writer and sends data to the appropriate channel.
// This is used by TeeWriter to stream container output to gRPC clients.
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

// NewStdoutWriter returns an io.Writer that streams stdout to gRPC clients.
// This writer is thread-safe and non-blocking.
func (e *ExecAttachable) NewStdoutWriter() io.Writer {
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stdoutChan,
		name:    "stdout",
	}
}

// NewStderrWriter returns an io.Writer that streams stderr to gRPC clients.
// This writer is thread-safe and non-blocking.
func (e *ExecAttachable) NewStderrWriter() io.Writer {
	return &streamWriter{
		ctx:     e.rootCtx,
		channel: e.stderrChan,
		name:    "stderr",
	}
}

// SendExitCode sends the exit code to all connected clients and closes the session.
// This should be called once when the container process completes.
// It's safe to call multiple times (subsequent calls are no-ops).
func (e *ExecAttachable) SendExitCode(exitCode int32) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Send exit code (non-blocking since channel is buffered)
	select {
	case e.exitChan <- exitCode:
		bklog.G(e.rootCtx).Debugf("sent exit code %d to clients", exitCode)
	default:
		// Exit code already sent or channel closed
		bklog.G(e.rootCtx).Debug("exit code already sent or channel closed")
	}

	return nil
}

// Close closes all channels and cleans up resources.
// This should be called when the exec session is completely done.
// It waits for all buffered data to be sent before closing.
func (e *ExecAttachable) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		// Close all channels to signal end of stream
		close(e.stdoutChan)
		close(e.stderrChan)
		close(e.exitChan)
		close(e.sessionDone)

		bklog.G(e.rootCtx).Debug("exec session channels closed")
	})

	return nil
}

// WaitReady blocks until the first client connects and the session is ready.
// This is useful for ensuring clients are connected before starting exec.
func (e *ExecAttachable) WaitReady(ctx context.Context) error {
	select {
	case <-e.sessionReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.rootCtx.Done():
		return e.rootCtx.Err()
	}
}

// Done returns a channel that's closed when the session is complete.
// Useful for waiting on session completion.
func (e *ExecAttachable) Done() <-chan struct{} {
	return e.sessionDone
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
