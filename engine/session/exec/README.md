# exec

gRPC service for streaming container execution output in real-time.

## overview

This package provides streaming stdout/stderr from container execution via gRPC. Clients receive output as it's produced rather than only after completion.

## components

### ExecAttachable

gRPC service implementing the `Exec` service from `exec.proto`.

Manages streaming sessions for container execution output. Multiple clients can connect to the same session and each receives the same data through independent streams.

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

### executor

Located in `engine/buildkit/executor_spec.go`.

Three integration points:

**setupExecStreaming**: Creates ExecAttachable before container starts
```go
execAttachable := exec.NewExecAttachable(ctx)
state.execAttachable = execAttachable
```

**setupStdio**: Connects TeeWriters with stream writers
```go
stdoutTee := NewTeeWriter(ctx, stdoutFile, w.getStreamWriter(state, "stdout"))
stderrTee := NewTeeWriter(ctx, stderrFile, w.getStreamWriter(state, "stderr"))
```

**runContainer**: Sends exit code after completion
```go
if state.execAttachable != nil {
    state.execAttachable.SendExitCode(exitCode)
}
```

### typescript sdk

Located in `sdk/typescript/src/common/`.

`GRPCConnectionManager` handles client lifecycle and authentication.

Session info (host, port, token) is stored during engine provisioning for later gRPC use.

## current status

ExecAttachable is created and receives data via TeeWriter. However, session manager integration is not yet complete:

- TeeWriter dual output works
- ExecAttachable receives stdout/stderr/exit
- Clients cannot yet discover/connect to ExecAttachable (session manager work pending)

## implementation notes

### non-blocking writes

TeeWriter uses buffered channel (size 100). If full, writes are dropped with warning. File writes never block on stream operations.

### graceful degradation

| failure | behavior |
|---------|----------|
| no clients | writes only to file |
| stream write fails | logs warning, continues |
| channel full | drops write, logs warning |
| client disconnects | other clients unaffected |

### resource management

Server-side channels are buffered (100 items). Close ensures no goroutine leaks. `sync.Once` prevents double-close panics.

Client-side connection is lazy-initialized and authentication metadata is reused.

## testing

See `exec_test.go` and `control_test.go` for unit tests covering session lifecycle, output streaming, exit codes, and error handling.
