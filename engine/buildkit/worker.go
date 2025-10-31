package buildkit

import (
	"context"
	"net/http"
	"sync"

	runc "github.com/containerd/go-runc"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/session/exec"
	bkcache "github.com/dagger/dagger/internal/buildkit/cache"
	"github.com/dagger/dagger/internal/buildkit/executor"
	"github.com/dagger/dagger/internal/buildkit/executor/oci"
	"github.com/dagger/dagger/internal/buildkit/frontend"
	bksession "github.com/dagger/dagger/internal/buildkit/session"
	"github.com/dagger/dagger/internal/buildkit/solver"
	"github.com/dagger/dagger/internal/buildkit/solver/llbsolver/ops"
	"github.com/dagger/dagger/internal/buildkit/solver/pb"
	"github.com/dagger/dagger/internal/buildkit/util/entitlements"
	"github.com/dagger/dagger/internal/buildkit/util/network"
	"github.com/dagger/dagger/internal/buildkit/worker"
	"github.com/dagger/dagger/internal/buildkit/worker/base"
	"github.com/docker/docker/pkg/idtools"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/semaphore"
)

/*
Worker is Dagger's custom worker. Most of the buildkit Worker interface methods are
just inherited from buildkit's base.Worker, with the exception of methods involving
executor.Executor (most importantly ResolveOp).

We need a custom Executor implementation for setting up containers (currently, just
for providing SessionID, but in the future everything the shim does will be migrated
here). For simplicity, this Worker struct also implements that Executor interface
(in executor.go) since Worker+Executor are so tightly bound together anyways.
*/
type Worker struct {
	*sharedWorkerState
	causeCtx trace.SpanContext
	execMD   *ExecutionMetadata
}

type sharedWorkerState struct {
	*base.Worker
	root             string
	executorRoot     string
	telemetryPubSub  http.Handler
	bkSessionManager *bksession.Manager
	sessionHandler   sessionHandler
	dagqlServer      dagqlServer

	runc             *runc.Runc
	cgroupParent     string
	networkProviders map[pb.NetMode]network.Provider
	processMode      oci.ProcessMode
	idmap            *idtools.IdentityMapping
	dns              *oci.DNSConfig
	apparmorProfile  string
	selinux          bool
	entitlements     entitlements.Set
	parallelismSem   *semaphore.Weighted
	workerCache      bkcache.Manager

	// Container state tracking for lifecycle events and status queries
	stateRegistry *ContainerStateRegistry

	// Shared ExecAttachable for streaming container logs across all executions
	execAttachable   *exec.ExecAttachable
	execAttachableMu sync.Mutex

	running map[string]*execState
	mu      sync.RWMutex
}

type sessionHandler interface {
	ServeHTTPToNestedClient(http.ResponseWriter, *http.Request, *ExecutionMetadata)
}

type dagqlServer interface {
	Server(ctx context.Context) (*dagql.Server, error)
}

type NewWorkerOpts struct {
	WorkerRoot       string
	ExecutorRoot     string
	BaseWorker       *base.Worker
	TelemetryPubSub  http.Handler
	BKSessionManager *bksession.Manager
	SessionHandler   sessionHandler
	DagqlServer      dagqlServer

	Runc                *runc.Runc
	DefaultCgroupParent string
	ProcessMode         oci.ProcessMode
	IDMapping           *idtools.IdentityMapping
	DNSConfig           *oci.DNSConfig
	ApparmorProfile     string
	SELinux             bool
	Entitlements        entitlements.Set
	NetworkProviders    map[pb.NetMode]network.Provider
	ParallelismSem      *semaphore.Weighted
	WorkerCache         bkcache.Manager
	StateRegistry       *ContainerStateRegistry
}

func NewWorker(opts *NewWorkerOpts) *Worker {
	return &Worker{sharedWorkerState: &sharedWorkerState{
		Worker:           opts.BaseWorker,
		root:             opts.WorkerRoot,
		executorRoot:     opts.ExecutorRoot,
		telemetryPubSub:  opts.TelemetryPubSub,
		bkSessionManager: opts.BKSessionManager,
		sessionHandler:   opts.SessionHandler,
		dagqlServer:      opts.DagqlServer,

		runc:             opts.Runc,
		cgroupParent:     opts.DefaultCgroupParent,
		networkProviders: opts.NetworkProviders,
		processMode:      opts.ProcessMode,
		idmap:            opts.IDMapping,
		dns:              opts.DNSConfig,
		apparmorProfile:  opts.ApparmorProfile,
		selinux:          opts.SELinux,
		entitlements:     opts.Entitlements,
		parallelismSem:   opts.ParallelismSem,
		workerCache:      opts.WorkerCache,
		stateRegistry:    opts.StateRegistry,

		running: make(map[string]*execState),
	}}
}

func (w *Worker) Executor() executor.Executor {
	return w
}

func (w *Worker) ResolveOp(vtx solver.Vertex, s frontend.FrontendLLBBridge, sm *bksession.Manager) (solver.Op, error) {
	customOp, ok, err := w.customOpFromVtx(vtx, s, sm)
	if err != nil {
		return nil, err
	}
	if ok {
		return customOp, nil
	}

	// if this is an ExecOp, pass in ourself as executor
	if baseOp, ok := vtx.Sys().(*pb.Op); ok {
		if execOp, ok := baseOp.Op.(*pb.Op_Exec); ok {
			execMD, ok, err := executionMetadataFromVtx(vtx)
			if err != nil {
				return nil, err
			}
			if ok {
				w = w.ExecWorker(
					SpanContextFromDescription(vtx.Options().Description),
					*execMD,
				)
			}
			return ops.NewExecOp(
				vtx,
				execOp,
				baseOp.Platform,
				w.workerCache,
				w.parallelismSem,
				sm,
				w, // executor
				w,
			)
		}
	}

	// otherwise, just use the default base.Worker's ResolveOp
	return w.Worker.ResolveOp(vtx, s, sm)
}

func (w *Worker) ExecWorker(causeCtx trace.SpanContext, execMD ExecutionMetadata) *Worker {
	return &Worker{sharedWorkerState: w.sharedWorkerState, causeCtx: causeCtx, execMD: &execMD}
}

/*
Buildkit's worker.Controller is a bit odd; it exists to manage multiple workers because that was
a planned feature years ago, but it never got implemented. So it exists to manage a single worker,
which doesn't really add much.

We still need to provide a worker.Controller value to a few places though, which this method enables.
*/
func AsWorkerController(w worker.Worker) (*worker.Controller, error) {
	wc := &worker.Controller{}
	err := wc.Add(w)
	if err != nil {
		return nil, err
	}
	return wc, nil
}

// GetOrCreateExecAttachable returns the shared ExecAttachable instance, creating it if needed.
// This ensures all container executions share the same ExecAttachable for consistent streaming
// and proper wiring of the container state registry.
//
// Thread-safe: Uses mutex to ensure only one ExecAttachable is created.
func (w *Worker) GetOrCreateExecAttachable(ctx context.Context) *exec.ExecAttachable {
	w.execAttachableMu.Lock()
	defer w.execAttachableMu.Unlock()

	// Return existing instance if already created
	if w.execAttachable != nil {
		return w.execAttachable
	}

	// Create new ExecAttachable
	w.execAttachable = exec.NewExecAttachable(ctx)

	// Wire the state registry if available (with adapter)
	if w.stateRegistry != nil {
		adapter := &stateRegistryAdapter{registry: w.stateRegistry}
		w.execAttachable.SetStateRegistry(adapter)
	}

	return w.execAttachable
}

// stateRegistryAdapter adapts the buildkit ContainerStateRegistry to the exec package's
// ContainerStateRegistry interface by converting between different ContainerState types.
type stateRegistryAdapter struct {
	registry *ContainerStateRegistry
}

// GetState implements exec.ContainerStateRegistry.GetState
func (a *stateRegistryAdapter) GetState(containerID string) (*exec.ContainerState, error) {
	state, err := a.registry.GetState(containerID)
	if err != nil {
		return nil, err
	}

	// Convert buildkit.ContainerState to exec.ContainerState
	execState := &exec.ContainerState{
		ContainerID: state.ContainerID,
		Status:      string(state.Status),
		ExitCode:    state.ExitCode,
		StartedAt:   state.StartedAt,
		FinishedAt:  state.FinishedAt,
		LastUpdated: state.LastUpdated,
	}

	// Convert resource usage if present
	if state.ResourceUsage != nil {
		execState.ResourceUsage = &exec.ContainerResourceUsage{
			CPUPercent:   state.ResourceUsage.CPUPercent,
			MemoryBytes:  state.ResourceUsage.MemoryBytes,
			MemoryLimit:  state.ResourceUsage.MemoryLimit,
			IOReadBytes:  state.ResourceUsage.IOReadBytes,
			IOWriteBytes: state.ResourceUsage.IOWriteBytes,
		}
	}

	return execState, nil
}

// Subscribe implements exec.ContainerStateRegistry.Subscribe
func (a *stateRegistryAdapter) Subscribe(ctx context.Context, containerID string) (<-chan exec.ContainerLifecycleEventData, func()) {
	eventChan, unsubscribe := a.registry.Subscribe(ctx, containerID)

	// Create a new channel for exec events
	execEventChan := make(chan exec.ContainerLifecycleEventData, 100)

	// Start a goroutine to convert events
	go func() {
		defer close(execEventChan)
		for event := range eventChan {
			execEvent := exec.ContainerLifecycleEventData{
				ContainerID: event.ContainerID,
				EventType:   string(event.EventType),
				Status:      string(event.Status),
				Timestamp:   event.Timestamp,
				ExitCode:    event.ExitCode,
				Message:     event.Message,
			}

			select {
			case execEventChan <- execEvent:
			case <-ctx.Done():
				return
			}
		}
	}()

	return execEventChan, unsubscribe
}
