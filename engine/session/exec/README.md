# exec

gRPC service for streaming container execution output in real-time.

## overview

This package provides streaming stdout/stderr from container execution via gRPC. Clients receive output as it's produced rather than only after completion.

Multiple containers can stream output through a single ExecAttachable instance via multiplexing. Each container execution is tracked by the ExecSessionRegistry, which manages concurrent exec instances and allows multiple clients to attach to the same execution stream.

## components

### ExecAttachable

gRPC service implementing the `Exec` service from `exec.proto`.

Manages streaming sessions for container execution output. Multiple clients can connect to the same session and each receives the same data through independent streams.

Uses ExecSessionRegistry for multiplexing exec sessions across containers. The service is registered with the Buildkit session manager during session initialization.

### ExecSessionRegistry

Thread-safe registry managing multiple concurrent exec instances.

Provides multiplexing so multiple containers can stream through a single ExecAttachable, and multiple clients can attach to the same exec instance. Each exec instance is identified by containerID/execID pair.

Features:
- Register/unregister exec instances dynamically
- Get stdout/stderr writers for streaming output
- Send exit codes to all connected clients
- Automatic cleanup of exited instances after 5-minute retention period
- Background cleanup task runs every 30 seconds

### ExecInstance

Represents a single container exec session shared across multiple gRPC client connections.

Manages lifecycle of stdout/stderr streaming and exit code delivery. Tracks connected clients and provides thread-safe, non-blocking writers for output streaming.

States: pending (registered), running (first client connected), exited (exit code received)

### TeeWriter

Located in `../buildkit/teewriter.go`.

Implements dual output:
- Primary: write to file (required for Container.Stdout() / Container.Stderr())
- Secondary: stream to gRPC clients (best-effort, non-blocking)

File writes always succeed even if streaming fails. Stream writes are non-blocking and won't slow container execution.

## protocol

Defined in `exec.proto`.

### Session RPC

Bidirectional streaming for exec I/O.

Flow:
1. Client sends `Start` with container ID and exec parameters
2. Server sends `Ready`
3. Server streams `stdout` and `stderr` chunks
4. Server sends `exit` code when container completes
5. Stream closes

### ContainerLifecycle RPC

Streams container lifecycle events (started, paused, exited, etc).

### ContainerStatus RPC

Non-blocking query for current container status and optional resource usage.

### Control RPCs

ContainerPause, ContainerResume, ContainerSignal - for future use.

## integration

### session manager

Located in `engine/server/bk_session.go`.

ExecAttachable is registered with the Buildkit session manager during session initialization:

```go
// Register ExecAttachable for container exec streaming
execAttachable := srv.worker.GetOrCreateExecAttachable(ctx)
sess.Allow(execAttachable)
```

This makes the exec service discoverable by gRPC clients through the session connection.

### executor

Located in `engine/buildkit/executor_spec.go`.

Four integration points:

**setupExecStreaming**: Registers exec instance before container starts
```go
execAttachable := w.getExecAttachable()
if execAttachable != nil {
    execAttachable.RegisterExecution(containerID, execID)
}
```

**setupStdio**: Connects TeeWriters with stream writers
```go
stdoutWriter := execAttachable.GetStdoutWriter(containerID, execID)
stderrWriter := execAttachable.GetStderrWriter(containerID, execID)
stdoutTee := NewTeeWriter(ctx, stdoutFile, stdoutWriter)
stderrTee := NewTeeWriter(ctx, stderrFile, stderrWriter)
```

**runContainer**: Sends exit code after completion
```go
if execAttachable != nil {
    execAttachable.SendExitCode(containerID, execID, exitCode)
}
```

**cleanup**: Unregisters exec instance when done
```go
if execAttachable != nil {
    execAttachable.UnregisterExecution(containerID, execID)
}
```

### typescript sdk

Located in `sdk/typescript/src/common/grpc/`.

`GRPCConnectionManager` handles client lifecycle and authentication.

Session info (host, port, token) is stored during engine provisioning for later gRPC use. The manager provides lazy-initialized client connections and reusable authentication metadata.

## current status

Phase 1 is complete. The exec service is fully integrated with the session manager and operational:

- ExecAttachable registered with session manager (clients can discover service)
- ExecSessionRegistry multiplexes exec instances across containers
- TeeWriter provides dual output (file + stream)
- Multiple clients can attach to same exec instance
- Exit code delivery and automatic cleanup working
- Integration tests validate all core functionality

Limitations:
- TTY support not implemented (stdin forwarding, terminal resize)
- Container control operations (pause/resume/signal) defined but not wired to runtime

## multiplexing

The ExecSessionRegistry enables efficient multiplexing of exec sessions:

### multiple containers per session

A single ExecAttachable instance manages output from multiple containers concurrently. Each container execution is registered with a unique containerID/execID pair.

```
ExecAttachable (one per session)
  |
  +-- ExecSessionRegistry
       |
       +-- container-1/exec-1 --> ExecInstance
       +-- container-2/exec-1 --> ExecInstance
       +-- container-2/exec-2 --> ExecInstance
```

### multiple clients per exec

Multiple clients can attach to the same exec instance and receive identical output streams independently. Each client gets its own stream but shares the underlying exec instance.

```
ExecInstance (container-1/exec-1)
  |
  +-- stdout/stderr channels (shared)
  |
  +-- client-A (independent stream)
  +-- client-B (independent stream)
  +-- client-C (independent stream)
```

Client disconnection does not affect other clients. Output continues streaming to remaining clients.

### lifecycle management

Exec instances transition through states:
- **pending**: registered but no clients connected
- **running**: at least one client connected
- **exited**: exit code received

Exited instances are retained for 5 minutes to allow late-arriving clients to retrieve exit codes. Background cleanup task removes stale instances every 30 seconds.

## implementation notes

### non-blocking writes

TeeWriter uses buffered channel (size 100). If full, writes are dropped with warning. File writes never block on stream operations.

ExecInstance writers use buffered channels (100 for stdout/stderr, 1 for exit). Non-blocking sends ensure container execution never waits on slow clients.

### graceful degradation

| failure | behavior |
|---------|----------|
| no clients | writes only to file |
| stream write fails | logs warning, continues |
| channel full | drops write, logs warning |
| client disconnects | other clients unaffected |

### resource management

Server-side channels are buffered (100 items for stdout/stderr, 1 for exit). Close ensures no goroutine leaks. `sync.Once` prevents double-close panics.

Registry cleanup task removes exited instances after retention period to prevent unbounded growth.

Client-side connection is lazy-initialized and authentication metadata is reused for efficiency.

## testing

Unit tests in `exec_test.go` and `control_test.go` cover:
- Session lifecycle (connect, stream, exit, disconnect)
- Output streaming (stdout/stderr/exit code)
- Error handling (invalid inputs, missing instances)
- Multi-client scenarios

Integration tests in `session_integration_test.go` validate:
- Service discovery through gRPC server registration
- Multiple container streaming through single ExecAttachable
- Registration/unregistration lifecycle
- Concurrent execution multiplexing (10 containers x 5 execs)
- Invalid container ID handling
- Client disconnect isolation (one client disconnect doesn't affect others)
- Registry cleanup on session end
- Automatic cleanup of exited instances after retention period
- Exited instance retention for late-arriving clients
- State registry integration for status queries
- Multiple clients receiving identical output streams

Run tests:
```bash
go test -v ./engine/session/exec/
```
