package exec

import (
	context "context"
	"fmt"
	"strings"
	"syscall"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// ExecOption is a functional option for configuring ExecAttachable.
type ExecOption func(*ExecAttachable)

// WithRuncClient sets the runc client for container control operations.
func WithRuncClient(runcClient *runc.Runc) ExecOption {
	return func(e *ExecAttachable) {
		e.runcClient = runcClient
	}
}

// WithStateRegistry sets the container state registry for tracking container state.
func WithStateRegistry(registry ContainerStateRegistry) ExecOption {
	return func(e *ExecAttachable) {
		e.stateRegistry = registry
	}
}

// NewExecAttachableWithOptions creates a new ExecAttachable with the given options.
func NewExecAttachableWithOptions(rootCtx context.Context, opts ...ExecOption) *ExecAttachable {
	e := NewExecAttachable(rootCtx)
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ContainerPause pauses a running container using runc.
// The container's processes are frozen until ContainerResume is called.
func (e *ExecAttachable) ContainerPause(
	ctx context.Context,
	req *ContainerControlRequest,
) (*ContainerControlResponse, error) {
	// Validate request
	if req.ContainerId == "" {
		return &ContainerControlResponse{
			Success: false,
			Message: "container_id is required",
		}, nil
	}

	// Check if runc client is available
	if e.runcClient == nil {
		return nil, status.Errorf(codes.Unavailable, "runc client not available")
	}

	bklog.G(ctx).Debugf("pausing container: %s", req.ContainerId)

	// Call runc to pause container
	if err := e.runcClient.Pause(ctx, req.ContainerId); err != nil {
		bklog.G(ctx).WithError(err).Errorf("failed to pause container: %s", req.ContainerId)
		return &ContainerControlResponse{
			Success: false,
			Message: fmt.Sprintf("failed to pause container: %v", err),
		}, nil
	}

	// Note: The state registry will be updated by the background updater
	// when it queries runc and discovers the container is paused.
	// We don't update it here to avoid tight coupling with the registry.

	bklog.G(ctx).Debugf("container paused successfully: %s", req.ContainerId)
	return &ContainerControlResponse{
		Success: true,
		Message: "container paused successfully",
	}, nil
}

// ContainerResume resumes a paused container using runc.
// The container's processes are unfrozen and continue execution.
func (e *ExecAttachable) ContainerResume(
	ctx context.Context,
	req *ContainerControlRequest,
) (*ContainerControlResponse, error) {
	// Validate request
	if req.ContainerId == "" {
		return &ContainerControlResponse{
			Success: false,
			Message: "container_id is required",
		}, nil
	}

	// Check if runc client is available
	if e.runcClient == nil {
		return nil, status.Errorf(codes.Unavailable, "runc client not available")
	}

	bklog.G(ctx).Debugf("resuming container: %s", req.ContainerId)

	// Call runc to resume container
	if err := e.runcClient.Resume(ctx, req.ContainerId); err != nil {
		bklog.G(ctx).WithError(err).Errorf("failed to resume container: %s", req.ContainerId)
		return &ContainerControlResponse{
			Success: false,
			Message: fmt.Sprintf("failed to resume container: %v", err),
		}, nil
	}

	// Note: The state registry will be updated by the background updater
	// when it queries runc and discovers the container is running again.
	// We don't update it here to avoid tight coupling with the registry.

	bklog.G(ctx).Debugf("container resumed successfully: %s", req.ContainerId)
	return &ContainerControlResponse{
		Success: true,
		Message: "container resumed successfully",
	}, nil
}

// ContainerSignal sends a signal to a container using runc.
// Supports standard Unix signals like SIGTERM, SIGKILL, SIGINT, etc.
func (e *ExecAttachable) ContainerSignal(
	ctx context.Context,
	req *ContainerSignalRequest,
) (*ContainerControlResponse, error) {
	// Validate request
	if req.ContainerId == "" {
		return &ContainerControlResponse{
			Success: false,
			Message: "container_id is required",
		}, nil
	}
	if req.Signal == "" {
		return &ContainerControlResponse{
			Success: false,
			Message: "signal is required",
		}, nil
	}

	// Check if runc client is available
	if e.runcClient == nil {
		return nil, status.Errorf(codes.Unavailable, "runc client not available")
	}

	// Parse signal name to syscall.Signal
	sig, err := parseSignal(req.Signal)
	if err != nil {
		return &ContainerControlResponse{
			Success: false,
			Message: fmt.Sprintf("invalid signal: %v", err),
		}, nil
	}

	bklog.G(ctx).Debugf("sending signal %s to container: %s", req.Signal, req.ContainerId)

	// Call runc to send signal
	// Note: runc.Kill takes an int signal, not syscall.Signal
	if err := e.runcClient.Kill(ctx, req.ContainerId, int(sig), nil); err != nil {
		bklog.G(ctx).WithError(err).Errorf("failed to send signal %s to container: %s", req.Signal, req.ContainerId)
		return &ContainerControlResponse{
			Success: false,
			Message: fmt.Sprintf("failed to send signal: %v", err),
		}, nil
	}

	bklog.G(ctx).Debugf("signal %s sent successfully to container: %s", req.Signal, req.ContainerId)
	return &ContainerControlResponse{
		Success: true,
		Message: fmt.Sprintf("signal %s sent successfully", req.Signal),
	}, nil
}

// parseSignal converts a signal name to syscall.Signal.
// Supports signal names with or without the "SIG" prefix (e.g., "TERM" or "SIGTERM").
// Returns an error if the signal name is not recognized.
func parseSignal(signalName string) (syscall.Signal, error) {
	// Normalize: uppercase and remove "SIG" prefix if present
	signalName = strings.ToUpper(strings.TrimSpace(signalName))
	signalName = strings.TrimPrefix(signalName, "SIG")

	// Map signal names to syscall.Signal values
	switch signalName {
	case "TERM":
		return syscall.SIGTERM, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "QUIT":
		return syscall.SIGQUIT, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	case "STOP":
		return syscall.SIGSTOP, nil
	case "CONT":
		return syscall.SIGCONT, nil
	case "ABRT":
		return syscall.SIGABRT, nil
	case "ALRM":
		return syscall.SIGALRM, nil
	case "PIPE":
		return syscall.SIGPIPE, nil
	default:
		return 0, fmt.Errorf("unsupported signal: %s", signalName)
	}
}
