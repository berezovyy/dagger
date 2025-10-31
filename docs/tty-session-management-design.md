# TTY Session Management Architecture with Reconnection Support

**Version:** 1.0
**Date:** 2025-10-31
**Status:** Design Document

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Background and Context](#background-and-context)
3. [Architecture Overview](#architecture-overview)
4. [TTY Session State Management](#tty-session-state-management)
5. [Reconnection Protocol](#reconnection-protocol)
6. [Stdin/Resize Pipeline Design](#stdinresize-pipeline-design)
7. [Buffer Management Strategy](#buffer-management-strategy)
8. [Edge Cases and Error Handling](#edge-cases-and-error-handling)
9. [Integration Points](#integration-points)
10. [Security Considerations](#security-considerations)
11. [Resource Limits and Policies](#resource-limits-and-policies)
12. [API Specifications](#api-specifications)
13. [Sequence Diagrams](#sequence-diagrams)
14. [Implementation Roadmap](#implementation-roadmap)

---

## Executive Summary

This document defines the architecture for adding TTY (pseudo-terminal) support with reconnection capabilities to Dagger's exec session infrastructure. Building on Phase 1's multiplexing foundation, this design enables:

- **Bidirectional TTY sessions** with stdin forwarding and terminal resize
- **Transparent reconnection** allowing clients to disconnect and resume sessions
- **Output history buffering** for reconnecting clients to catch up on missed output
- **Multi-client support** with policy-based access control
- **Production-grade reliability** with timeouts, backpressure handling, and resource limits

The design leverages existing infrastructure (`ExecSessionRegistry`, `ExecInstance`) and mirrors patterns from the reference `terminal` service while adding stateful session management for reconnection scenarios.

---

## Background and Context

### Completed Work (Phase 1)
- ✅ Multiplexing infrastructure in `ExecSessionRegistry`
- ✅ Multi-client streaming for stdout/stderr
- ✅ Exit code delivery and lifecycle management
- ✅ Proto definitions include `stdin`, `resize`, and `tty` fields

### Current State
- TODOs in `exec.go` lines 275, 283 for stdin/resize forwarding
- `ExecInstance` has stub methods `WriteStdin()` and `SendResize()` returning "not yet implemented"
- Existing PTY support in `executor.go` (lines 757-851) but not integrated with session management
- No reconnection support - clients that disconnect lose all session state

### Requirements
1. Enable TTY mode for interactive container sessions (shells, editors, TUIs)
2. Support client reconnection without disrupting the running container process
3. Buffer output history for reconnecting clients
4. Handle stdin forwarding with proper EOF and backpressure
5. Coalesce and forward terminal resize events efficiently
6. Support multiple concurrent clients with clear access policies
7. Maintain security boundaries and resource limits

---

## Architecture Overview

### Component Hierarchy

```
┌─────────────────────────────────────────────────────────────┐
│                    gRPC Session RPC                          │
│              (ExecAttachable.Session)                        │
└─────────────────────┬───────────────────────────────────────┘
                      │
                      ├── Session Handshake
                      ├── Client Authentication
                      └── Reconnection Detection
                      │
┌─────────────────────▼───────────────────────────────────────┐
│              ExecSessionRegistry                             │
│  - Session ID mapping                                        │
│  - Instance lifecycle management                             │
│  - Cleanup policies                                          │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│              TTYExecInstance                                 │
│  ┌────────────────────────────────────────────────────┐     │
│  │ Session State                                      │     │
│  │  - sessionID, containerID, execID                  │     │
│  │  - isTTY flag                                      │     │
│  │  - Active clients map                              │     │
│  │  - Creation/last activity timestamps               │     │
│  └────────────────────────────────────────────────────┘     │
│  ┌────────────────────────────────────────────────────┐     │
│  │ TTY State (when isTTY=true)                        │     │
│  │  - Current terminal size (rows, cols)              │     │
│  │  - PTY master/slave file descriptors               │     │
│  │  - Stdin pipe                                      │     │
│  └────────────────────────────────────────────────────┘     │
│  ┌────────────────────────────────────────────────────┐     │
│  │ Output Buffers                                     │     │
│  │  - Circular stdout history (64KB)                  │     │
│  │  - Circular stderr history (64KB)                  │     │
│  │  - Sequence numbers for ordering                   │     │
│  └────────────────────────────────────────────────────┘     │
│  ┌────────────────────────────────────────────────────┐     │
│  │ Channels                                           │     │
│  │  - stdoutChan, stderrChan                          │     │
│  │  - stdinChan (new)                                 │     │
│  │  - resizeChan (new)                                │     │
│  │  - exitChan                                        │     │
│  └────────────────────────────────────────────────────┘     │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│         Executor PTY Integration                             │
│         (buildkit/executor.go lines 757-851)                 │
│  - PTY master/slave creation                                 │
│  - IO forwarding (stdin → PTM, PTM → stdout)                 │
│  - Resize signal handling                                    │
│  - Container process lifecycle                               │
└──────────────────────────────────────────────────────────────┘
```

### Key Design Principles

1. **Backward Compatibility**: Non-TTY sessions continue to work unchanged
2. **Separation of Concerns**: TTY-specific logic isolated in new types/fields
3. **Thread Safety**: All shared state protected by appropriate synchronization
4. **Resource Bounded**: Fixed-size buffers, timeouts, and limits prevent resource exhaustion
5. **Fail-Safe**: Errors in reconnection don't crash running containers
6. **Observable**: Rich logging and metrics for debugging and monitoring

---

## TTY Session State Management

### Data Structures

#### Enhanced ExecInstance with TTY Support

```go
// ExecInstance is extended with TTY-specific fields
type ExecInstance struct {
    // === Existing fields (unchanged) ===
    containerID string
    execID      string
    rootCtx     context.Context
    cancel      context.CancelFunc

    mu          sync.RWMutex
    status      ExecStatus
    exitCode    *int32
    exitTime    *time.Time
    clients     map[string]*ClientInfo  // Changed from map[string]struct{}

    stdoutChan  chan []byte
    stderrChan  chan []byte
    exitChan    chan int32

    closeOnce   sync.Once
    closed      bool

    // === New TTY-specific fields ===

    // sessionID is a unique identifier for this session, used for reconnection
    // Format: "{containerID}:{execID}:{timestamp}-{random}"
    sessionID   string

    // isTTY indicates whether this is a TTY session
    isTTY       bool

    // TTY state (only used when isTTY=true)
    ttyState    *TTYState

    // Output history buffers for reconnection
    outputHistory *OutputHistory

    // Stdin/resize channels for bidirectional communication
    stdinChan   chan []byte
    resizeChan  chan WinSize

    // lastActivity tracks the last time any client interacted with this session
    lastActivity time.Time
}

// ClientInfo tracks information about a connected client
type ClientInfo struct {
    ClientID      string
    ConnectedAt   time.Time
    LastActivity  time.Time
    IsReconnect   bool
    ReplayOffset  uint64  // Sequence number where this client's replay started
}

// TTYState holds TTY-specific runtime state
type TTYState struct {
    mu          sync.RWMutex

    // Current terminal dimensions
    currentSize WinSize

    // PTY file descriptors (only set when container is running)
    ptmx        *os.File  // PTY master
    pts         *os.File  // PTY slave (used by container)

    // Stdin pipe for forwarding client input to PTY
    stdinPipe   *io.PipeWriter

    // Resize coalescing - we buffer the latest resize and debounce
    lastResize      time.Time
    pendingResize   *WinSize
    resizeDebounce  time.Duration  // 50ms default
}

// OutputHistory maintains a circular buffer of output for reconnection
type OutputHistory struct {
    mu            sync.RWMutex

    // Circular buffers for stdout/stderr
    stdoutBuffer  *CircularBuffer
    stderrBuffer  *CircularBuffer

    // Sequence counter for ordering events
    sequence      uint64

    // High water mark - highest sequence delivered to any client
    highWaterMark uint64
}

// CircularBuffer is a fixed-size ring buffer with sequence tracking
type CircularBuffer struct {
    buffer   []OutputChunk
    capacity int
    head     int  // Next write position
    size     int  // Current number of items
}

// OutputChunk represents a single output event with metadata
type OutputChunk struct {
    Sequence  uint64
    Timestamp time.Time
    Data      []byte
    Stream    StreamType  // stdout or stderr
}

type StreamType int

const (
    StreamStdout StreamType = 1
    StreamStderr StreamType = 2
)

type WinSize struct {
    Rows uint32
    Cols uint32
}
```

### Session ID Generation

Session IDs uniquely identify an exec session across reconnections:

```go
// generateSessionID creates a unique session identifier
// Format: "{containerID}:{execID}:{unixNano}-{random}"
func generateSessionID(containerID, execID string) string {
    timestamp := time.Now().UnixNano()
    random := rand.Uint64()
    return fmt.Sprintf("%s:%s:%d-%016x", containerID, execID, timestamp, random)
}
```

**Properties:**
- **Unique**: Collision probability is negligible (timestamp + 64-bit random)
- **Sortable**: Timestamp prefix allows chronological ordering
- **Parseable**: Colon-separated components for debugging
- **Opaque**: Client treats as opaque token

### Session Lifecycle States

```go
type ExecStatus string

const (
    ExecStatusPending   ExecStatus = "pending"    // Registered but not started
    ExecStatusStarting  ExecStatus = "starting"   // PTY being created (new)
    ExecStatusRunning   ExecStatus = "running"    // Container process running
    ExecStatusExited    ExecStatus = "exited"     // Process completed
    ExecStatusOrphaned  ExecStatus = "orphaned"   // No clients, awaiting cleanup (new)
)
```

**State Transitions:**
```
pending → starting → running → exited
                         ↓
                    orphaned (if all clients disconnect)
                         ↓
                    (cleanup after timeout)
```

### Timeout Policies

```go
const (
    // SessionOrphanTimeout: how long to keep a session alive with no connected clients
    SessionOrphanTimeout = 5 * time.Minute

    // SessionOutputRetentionTimeout: how long to keep output history after exit
    SessionOutputRetentionTimeout = 5 * time.Minute

    // SessionMaxIdleTimeout: maximum time without any activity before force cleanup
    SessionMaxIdleTimeout = 1 * time.Hour

    // StdinIdleTimeout: how long to wait for stdin before assuming EOF
    StdinIdleTimeout = 30 * time.Second

    // ResizeDebounceInterval: minimum time between resize events
    ResizeDebounceInterval = 50 * time.Millisecond
)
```

**Cleanup Decision Tree:**
```
Is session exited?
├─ YES: Has it been > SessionOutputRetentionTimeout since exit?
│   ├─ YES: CLEANUP
│   └─ NO: Keep (clients may still reconnect to get exit code)
└─ NO: Is session running?
    ├─ YES: Has it been > SessionMaxIdleTimeout since last activity?
    │   ├─ YES: CLEANUP (force)
    │   └─ NO: Has it been > SessionOrphanTimeout since last client?
    │       ├─ YES: CLEANUP (orphaned)
    │       └─ NO: Keep
    └─ NO: (pending/starting) - keep if age < 1 minute, else cleanup
```

---

## Reconnection Protocol

### Session Handshake

The `Start` message in the proto is extended with reconnection fields:

```protobuf
message Start {
    string container_id = 1;
    string exec_id = 2;
    repeated string command = 3;
    map<string, string> env = 4;
    string working_dir = 5;
    bool tty = 6;

    // === New fields for reconnection ===

    // session_id is provided by clients that are reconnecting to an existing session.
    // If empty, this is a new session. If provided, server attempts to reconnect.
    string session_id = 7;

    // replay_from_sequence requests output replay starting from this sequence number.
    // If 0, replay from the beginning of the buffer. Ignored for new sessions.
    uint64 replay_from_sequence = 8;

    // client_id is an optional identifier for this client instance.
    // Used for logging and debugging. Server generates one if not provided.
    string client_id = 9;
}
```

### Handshake Flow

#### New Session Initialization

```
Client                          Server (ExecAttachable.Session)
  │                                      │
  ├─── Start(session_id="") ────────────→│
  │                                      │ 1. Generate new sessionID
  │                                      │ 2. Register ExecInstance with tty=true
  │                                      │ 3. Initialize TTYState and OutputHistory
  │                                      │ 4. Store sessionID in instance
  │                                      │
  │←─── Ready(session_id=<new>) ────────┤ 5. Send Ready with sessionID
  │                                      │
  │←─── Output stream starts ───────────┤
```

#### Reconnection

```
Client                          Server (ExecAttachable.Session)
  │                                      │
  ├─── Start(session_id=<existing>) ───→│
  │    replay_from_sequence=12345       │
  │                                      │ 1. Lookup ExecInstance by sessionID
  │                                      │ 2. Verify session still exists
  │                                      │ 3. Add client to instance
  │                                      │
  │←─── Ready(session_id=<existing>) ───┤ 4. Send Ready (same sessionID)
  │                                      │
  │←─── Replay(seq=12346, stdout=...) ──┤ 5. Replay buffered output from seq
  │←─── Replay(seq=12347, stderr=...) ──┤
  │←─── ...                              │
  │                                      │ 6. Transition to live streaming
  │←─── Output stream continues ────────┤
```

### Protocol Messages

#### Ready Response (Extended)

```protobuf
message Ready {
    // session_id is the unique identifier for this session.
    // Clients must save this to reconnect later.
    string session_id = 1;

    // terminal_size is the current terminal size (for TTY sessions)
    Resize terminal_size = 2;

    // replay_complete indicates the end of replay (for reconnections)
    bool replay_complete = 3;
}
```

#### Output Response (Extended)

```protobuf
message SessionResponse {
    oneof msg {
        bytes stdout = 1;
        bytes stderr = 2;
        int32 exit = 3;
        Ready ready = 4;
        OutputChunk output_chunk = 5;  // New: for replay with metadata
    }
}

message OutputChunk {
    uint64 sequence = 1;       // Sequence number for ordering
    int64 timestamp = 2;       // Unix nanoseconds
    oneof data {
        bytes stdout = 3;
        bytes stderr = 4;
    }
    bool is_replay = 5;        // True during replay, false for live
}
```

### Reconnection Detection Logic

```go
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
    ctx := srv.Context()

    // 1. Receive Start message
    req, err := srv.Recv()
    if err != nil {
        return fmt.Errorf("waiting for start: %w", err)
    }

    startMsg := req.GetStart()
    if startMsg == nil {
        return status.Errorf(codes.InvalidArgument, "first message must be Start")
    }

    var instance *ExecInstance
    var isReconnect bool

    // 2. Check if this is a reconnection
    if startMsg.SessionId != "" {
        // Reconnection attempt
        instance, err = e.registry.GetInstanceBySessionID(startMsg.SessionId)
        if err != nil {
            return status.Errorf(codes.NotFound, "session not found: %s", startMsg.SessionId)
        }
        isReconnect = true
        bklog.G(ctx).Infof("client reconnecting to session %s", startMsg.SessionId)
    } else {
        // New session
        instance, err = e.registry.Register(startMsg.ContainerId, startMsg.ExecId)
        if err != nil {
            return status.Errorf(codes.Internal, "failed to register: %w", err)
        }

        // Generate session ID
        sessionID := generateSessionID(startMsg.ContainerId, startMsg.ExecId)
        instance.sessionID = sessionID
        instance.isTTY = startMsg.Tty

        if startMsg.Tty {
            // Initialize TTY state
            instance.ttyState = NewTTYState()
            instance.outputHistory = NewOutputHistory()
            instance.stdinChan = make(chan []byte, 100)
            instance.resizeChan = make(chan WinSize, 10)
        }

        bklog.G(ctx).Infof("new session created: %s (tty=%v)", sessionID, startMsg.Tty)
    }

    // 3. Generate client ID
    clientID := startMsg.ClientId
    if clientID == "" {
        clientID = fmt.Sprintf("client-%d", time.Now().UnixNano())
    }

    // 4. Register client
    instance.AddClient(clientID, isReconnect)
    defer instance.RemoveClient(clientID)

    // 5. Send Ready with session ID
    if err := srv.Send(&SessionResponse{
        Msg: &SessionResponse_Ready{
            Ready: &Ready{
                SessionId: instance.sessionID,
                TerminalSize: instance.GetCurrentSize(),  // nil if not TTY
                ReplayComplete: false,
            },
        },
    }); err != nil {
        return fmt.Errorf("sending ready: %w", err)
    }

    // 6. Handle output replay for reconnections
    if isReconnect && instance.outputHistory != nil {
        if err := e.replayOutputHistory(ctx, srv, instance, startMsg.ReplayFromSequence); err != nil {
            bklog.G(ctx).WithError(err).Warn("error during replay, continuing with live stream")
        }

        // Signal end of replay
        srv.Send(&SessionResponse{
            Msg: &SessionResponse_Ready{
                Ready: &Ready{
                    SessionId: instance.sessionID,
                    ReplayComplete: true,
                },
            },
        })
    }

    // 7. Continue with normal session handling
    return e.handleSession(ctx, srv, instance, clientID)
}
```

### Output Replay Strategy

**Replay Algorithm:**

1. **Lock output history** to get consistent snapshot
2. **Find start sequence**: use `replay_from_sequence` or buffer start
3. **Iterate circular buffers** from start sequence to current
4. **Send OutputChunk messages** with `is_replay=true`
5. **Mark replay complete** and transition to live streaming

```go
func (e *ExecAttachable) replayOutputHistory(
    ctx context.Context,
    srv Exec_SessionServer,
    instance *ExecInstance,
    fromSequence uint64,
) error {
    if instance.outputHistory == nil {
        return nil  // No history to replay
    }

    history := instance.outputHistory
    history.mu.RLock()
    defer history.mu.RUnlock()

    // Collect chunks to replay
    var chunks []OutputChunk

    // Merge stdout and stderr buffers, sorted by sequence
    stdoutChunks := history.stdoutBuffer.GetRange(fromSequence, history.sequence)
    stderrChunks := history.stderrBuffer.GetRange(fromSequence, history.sequence)
    chunks = mergeAndSortChunks(stdoutChunks, stderrChunks)

    // Stream chunks to client
    for _, chunk := range chunks {
        msg := &SessionResponse{
            Msg: &SessionResponse_OutputChunk{
                OutputChunk: &OutputChunk{
                    Sequence:  chunk.Sequence,
                    Timestamp: chunk.Timestamp.UnixNano(),
                    IsReplay:  true,
                },
            },
        }

        // Set stream-specific data
        switch chunk.Stream {
        case StreamStdout:
            msg.GetOutputChunk().Data = &OutputChunk_Stdout{Stdout: chunk.Data}
        case StreamStderr:
            msg.GetOutputChunk().Data = &OutputChunk_Stderr{Stderr: chunk.Data}
        }

        if err := srv.Send(msg); err != nil {
            return fmt.Errorf("sending replay chunk: %w", err)
        }
    }

    bklog.G(ctx).Infof("replayed %d output chunks (seq %d to %d)",
        len(chunks), fromSequence, history.sequence)

    return nil
}
```

**Replay Behavior:**
- **Fast-forward**: Replays at maximum speed (no delays)
- **Interruptible**: Client can cancel during replay
- **Bounded**: Maximum of `BufferCapacity` chunks replayed
- **Consistent**: Maintains stdout/stderr ordering by sequence number

### Stdin Resume Behavior

**Policy:** Stdin is **not** buffered across disconnections.

**Rationale:**
- Interactive applications (shells, editors) expect real-time input
- Buffering stdin creates complex state management and race conditions
- Security risk: buffered passwords or secrets could be exposed
- Simplicity: stdin flows only when client is connected

**Behavior:**
- When client disconnects: stdin pipe is closed (EOF sent to container)
- When client reconnects: new stdin pipe established
- Container receives EOF on disconnect, can detect and handle appropriately
- For non-interactive processes, this is acceptable (they typically don't use stdin)

**Exception - TTY Sessions:**
For TTY sessions, we keep the PTY master open even when all clients disconnect:
- PTY master remains open to prevent SIGHUP to container process
- New stdin data only accepted when client is connected
- This allows long-running interactive shells to survive temporary disconnections

---

## Stdin/Resize Pipeline Design

### Stdin Flow

```
┌──────────────┐         ┌──────────────────┐         ┌──────────────┐         ┌───────────┐
│   Client     │         │  ExecAttachable  │         │ ExecInstance │         │ Executor  │
│              │         │   .Session()     │         │              │         │  (PTY)    │
└──────┬───────┘         └────────┬─────────┘         └──────┬───────┘         └─────┬─────┘
       │                          │                           │                       │
       │ Recv(stdin bytes)        │                           │                       │
       ├─────────────────────────→│                           │                       │
       │                          │ WriteStdin(bytes)         │                       │
       │                          ├──────────────────────────→│                       │
       │                          │                           │ stdinChan <- bytes    │
       │                          │                           ├──────────────────────→│
       │                          │                           │                       │
       │                          │                           │          (stdin goroutine)
       │                          │                           │                       │
       │                          │                           │        io.Copy(ptmx, stdinReader)
       │                          │                           │                       │
       │                          │                           │                       │
       │                          │                           │                   ┌───▼───┐
       │                          │                           │                   │  PTY  │
       │                          │                           │                   │ Master│
       │                          │                           │                   └───┬───┘
       │                          │                           │                       │
       │                          │                           │                   ┌───▼──────┐
       │                          │                           │                   │Container │
       │                          │                           │                   │ Process  │
       │                          │                           │                   └──────────┘
```

### Stdin Implementation

```go
// In ExecInstance

// WriteStdin forwards stdin data from the client to the container
func (e *ExecInstance) WriteStdin(data []byte) error {
    if !e.isTTY {
        return errors.New("stdin not supported for non-TTY sessions")
    }

    if len(data) == 0 {
        // Empty write is treated as EOF signal
        e.closeStdin()
        return nil
    }

    select {
    case e.stdinChan <- data:
        e.mu.Lock()
        e.lastActivity = time.Now()
        e.mu.Unlock()
        return nil
    case <-e.rootCtx.Done():
        return e.rootCtx.Err()
    default:
        // Channel full - apply backpressure
        return errors.New("stdin buffer full")
    }
}

// closeStdin signals EOF by closing the stdin channel
func (e *ExecInstance) closeStdin() {
    e.mu.Lock()
    defer e.mu.Unlock()

    if e.ttyState != nil && e.ttyState.stdinPipe != nil {
        e.ttyState.stdinPipe.Close()
        e.ttyState.stdinPipe = nil
    }
}

// GetStdinReader returns a reader that pulls from the stdin channel
func (e *ExecInstance) GetStdinReader() io.Reader {
    if !e.isTTY {
        return nil
    }

    // Create a pipe for stdin
    r, w := io.Pipe()

    // Store writer for EOF signaling
    if e.ttyState != nil {
        e.ttyState.stdinPipe = w
    }

    // Goroutine to forward stdinChan → pipe
    go func() {
        defer w.Close()

        for {
            select {
            case data, ok := <-e.stdinChan:
                if !ok {
                    // Channel closed = EOF
                    return
                }

                _, err := w.Write(data)
                if err != nil {
                    bklog.G(e.rootCtx).WithError(err).Debug("error writing stdin")
                    return
                }

            case <-e.rootCtx.Done():
                return
            }
        }
    }()

    return r
}
```

### Backpressure Handling

**Problem:** Container may be slow to consume stdin (e.g., paused, blocking on I/O).

**Solution:** Bounded channel with explicit backpressure:

1. **Channel size**: 100 chunks (configurable)
2. **Non-blocking send**: Use select with default case
3. **Error response**: Return error to gRPC client
4. **Client retry**: Client can retry with exponential backoff
5. **Flow control**: gRPC's HTTP/2 flow control provides additional buffering

**Configuration:**

```go
const (
    StdinChannelSize = 100       // ~100KB assuming 1KB chunks
    StdinMaxChunkSize = 16 * 1024  // 16KB max per write
)
```

### Resize Flow

```
┌──────────────┐         ┌──────────────────┐         ┌──────────────┐         ┌───────────┐
│   Client     │         │  ExecAttachable  │         │ ExecInstance │         │ Executor  │
│              │         │   .Session()     │         │              │         │  (PTY)    │
└──────┬───────┘         └────────┬─────────┘         └──────┬───────┘         └─────┬─────┘
       │                          │                           │                       │
       │ Recv(resize 80x24)       │                           │                       │
       ├─────────────────────────→│                           │                       │
       │                          │ SendResize(80, 24)        │                       │
       │                          ├──────────────────────────→│                       │
       │                          │                           │ (debounce)            │
       │ Recv(resize 80x25)       │                           │   ⏱ 50ms              │
       ├─────────────────────────→│ SendResize(80, 25)        │                       │
       │                          ├──────────────────────────→│ (coalesce)            │
       │                          │                           │                       │
       │                          │                           │ resizeChan <- WinSize │
       │                          │                           ├──────────────────────→│
       │                          │                           │                       │
       │                          │                           │       ptm.Resize(WinSize)
       │                          │                           │       signal(SIGWINCH)
       │                          │                           │                       │
```

### Resize Implementation

```go
// In ExecInstance

// SendResize updates the terminal size
func (e *ExecInstance) SendResize(width, height uint32) error {
    if !e.isTTY {
        return errors.New("resize not supported for non-TTY sessions")
    }

    newSize := WinSize{Rows: height, Cols: width}

    e.ttyState.mu.Lock()

    // Update current size immediately (for new clients)
    e.ttyState.currentSize = newSize

    // Coalesce rapid resizes
    now := time.Now()
    if now.Sub(e.ttyState.lastResize) < e.ttyState.resizeDebounce {
        // Too soon - buffer this resize
        e.ttyState.pendingResize = &newSize
        e.ttyState.mu.Unlock()
        return nil
    }

    e.ttyState.lastResize = now
    e.ttyState.pendingResize = nil
    e.ttyState.mu.Unlock()

    // Send to resize channel
    select {
    case e.resizeChan <- newSize:
        e.mu.Lock()
        e.lastActivity = time.Now()
        e.mu.Unlock()
        return nil
    case <-e.rootCtx.Done():
        return e.rootCtx.Err()
    default:
        // Channel full - drop old resizes (latest is most important)
        select {
        case <-e.resizeChan:  // Drain one old resize
            e.resizeChan <- newSize
        default:
        }
        return nil
    }
}

// Debounce goroutine (started when TTYState is created)
func (t *TTYState) startResizeDebouncer(ctx context.Context, resizeChan chan WinSize) {
    ticker := time.NewTicker(t.resizeDebounce)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            t.mu.Lock()
            pending := t.pendingResize
            if pending != nil {
                t.pendingResize = nil
                t.lastResize = time.Now()
                t.mu.Unlock()

                select {
                case resizeChan <- *pending:
                case <-ctx.Done():
                    return
                }
            } else {
                t.mu.Unlock()
            }
        }
    }
}
```

**Resize Coalescing Strategy:**

1. **Immediate update**: First resize sent immediately
2. **Debounce window**: Subsequent resizes within 50ms are buffered
3. **Latest wins**: Only the most recent resize in the window is sent
4. **Channel overflow**: If channel is full, drop oldest resize (latest is most important)

**Rationale:**
- Terminal emulators often fire many resize events during window dragging
- Container can't process resizes faster than ~20Hz anyway
- Reduces PTY syscalls and SIGWINCH signals
- Ensures terminal state converges to final size

### EOF Handling

**Stdin EOF signals end-of-input to container:**

```go
// Client sends empty stdin to signal EOF
req := &SessionRequest{
    Msg: &SessionRequest_Stdin{
        Stdin: []byte{},
    },
}
```

**Server behavior:**

```go
case *SessionRequest_Stdin:
    if len(msg.Stdin) == 0 {
        // EOF signal
        if err := instance.closeStdin(); err != nil {
            bklog.G(ctx).WithError(err).Debug("error closing stdin")
        }
        bklog.G(ctx).Debug("stdin EOF received")
    } else {
        // Normal data
        if err := instance.WriteStdin(msg.Stdin); err != nil {
            return fmt.Errorf("writing stdin: %w", err)
        }
    }
```

**Container behavior:**
- TTY mode: Read from PTY returns EOF, shell may exit or ignore
- Non-TTY mode: Stdin pipe closed, process receives EOF on next read

---

## Buffer Management Strategy

### Output History Buffer

**Requirements:**
- Fixed maximum memory usage
- Fast append and range queries
- Thread-safe concurrent access
- Sequence number ordering

**Design:** Circular buffer with sequence tracking

```go
type CircularBuffer struct {
    buffer   []OutputChunk
    capacity int
    head     int  // Next write position
    size     int  // Current number of items
    mu       sync.RWMutex
}

func NewCircularBuffer(capacity int) *CircularBuffer {
    return &CircularBuffer{
        buffer:   make([]OutputChunk, capacity),
        capacity: capacity,
        head:     0,
        size:     0,
    }
}

// Append adds a chunk to the buffer, overwriting oldest if full
func (cb *CircularBuffer) Append(chunk OutputChunk) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    cb.buffer[cb.head] = chunk
    cb.head = (cb.head + 1) % cb.capacity

    if cb.size < cb.capacity {
        cb.size++
    }
}

// GetRange returns all chunks with sequence >= fromSeq and <= toSeq
func (cb *CircularBuffer) GetRange(fromSeq, toSeq uint64) []OutputChunk {
    cb.mu.RLock()
    defer cb.mu.RUnlock()

    var result []OutputChunk

    // Calculate start index (oldest item in buffer)
    start := cb.head - cb.size
    if start < 0 {
        start += cb.capacity
    }

    // Iterate through buffer from oldest to newest
    for i := 0; i < cb.size; i++ {
        idx := (start + i) % cb.capacity
        chunk := cb.buffer[idx]

        if chunk.Sequence >= fromSeq && chunk.Sequence <= toSeq {
            result = append(result, chunk)
        }
    }

    return result
}

// GetOldestSequence returns the sequence number of the oldest buffered chunk
func (cb *CircularBuffer) GetOldestSequence() uint64 {
    cb.mu.RLock()
    defer cb.mu.RUnlock()

    if cb.size == 0 {
        return 0
    }

    start := cb.head - cb.size
    if start < 0 {
        start += cb.capacity
    }

    return cb.buffer[start].Sequence
}
```

### Buffer Sizing

**Configuration:**

```go
const (
    // StdoutHistoryCapacity: number of stdout chunks to buffer
    StdoutHistoryCapacity = 1024

    // StderrHistoryCapacity: number of stderr chunks to buffer
    StderrHistoryCapacity = 1024

    // MaxChunkSize: maximum size of a single output chunk
    MaxChunkSize = 64 * 1024  // 64KB

    // TypicalChunkSize: typical size for chunk estimation
    TypicalChunkSize = 1024  // 1KB
)
```

**Memory calculation:**

```
Per session memory = (StdoutCapacity + StderrCapacity) × MaxChunkSize
                   = (1024 + 1024) × 64KB
                   = 128 MB worst case

Typical case       = 2048 × 1KB = 2 MB per session
```

**Rationale:**
- **1024 chunks**: Typical terminal has ~100 lines, so this is ~10 screens of output
- **64KB max**: Handles large paste operations or binary output bursts
- **Circular buffer**: Old data automatically evicted, no manual cleanup
- **Per-session**: Each exec session has independent buffers

### Chunk Creation Strategy

**When to create a chunk:**

1. **Size threshold**: After accumulating `TypicalChunkSize` bytes
2. **Time threshold**: After 100ms since last chunk (for low-rate output)
3. **Stream switch**: When switching between stdout and stderr
4. **Flush event**: On explicit flush (newline, container exit)

```go
type OutputAggregator struct {
    instance      *ExecInstance
    stream        StreamType
    buffer        []byte
    lastFlush     time.Time
    flushInterval time.Duration
}

func (oa *OutputAggregator) Write(p []byte) (n int, err error) {
    oa.buffer = append(oa.buffer, p...)
    now := time.Now()

    shouldFlush := len(oa.buffer) >= TypicalChunkSize ||
                   now.Sub(oa.lastFlush) >= oa.flushInterval ||
                   bytes.Contains(p, []byte{'\n'})

    if shouldFlush {
        oa.flush()
    }

    return len(p), nil
}

func (oa *OutputAggregator) flush() {
    if len(oa.buffer) == 0 {
        return
    }

    chunk := OutputChunk{
        Sequence:  oa.instance.outputHistory.nextSequence(),
        Timestamp: time.Now(),
        Data:      oa.buffer,
        Stream:    oa.stream,
    }

    // Add to history
    if oa.stream == StreamStdout {
        oa.instance.outputHistory.stdoutBuffer.Append(chunk)
    } else {
        oa.instance.outputHistory.stderrBuffer.Append(chunk)
    }

    // Send to live clients
    oa.instance.broadcastChunk(chunk)

    // Reset buffer
    oa.buffer = nil
    oa.lastFlush = time.Now()
}
```

### Sequence Number Management

**Sequence numbers provide:**
- **Ordering**: Total order across stdout and stderr
- **Deduplication**: Detect and skip duplicate replay
- **Gap detection**: Identify lost output (buffer overflow)

```go
// In OutputHistory

func (oh *OutputHistory) nextSequence() uint64 {
    oh.mu.Lock()
    defer oh.mu.Unlock()

    oh.sequence++
    return oh.sequence
}

func (oh *OutputHistory) getCurrentSequence() uint64 {
    oh.mu.RLock()
    defer oh.mu.RUnlock()

    return oh.sequence
}
```

**Sequence number properties:**
- **Monotonic**: Strictly increasing
- **Per-session**: Each session has independent sequence space
- **Persistent**: Sequence continues across client reconnections
- **64-bit**: No practical risk of overflow (2^64 is ~18 quintillion)

### Buffer Overflow Handling

**When buffer is full:**
1. Oldest chunk is evicted (FIFO)
2. Sequence number gap is created
3. Clients that reconnect past the gap see partial history

**Client gap detection:**

```go
// Client tracks last received sequence
lastSeq := uint64(0)

for {
    msg := <-stream.Recv()
    chunk := msg.GetOutputChunk()

    if chunk.Sequence != lastSeq + 1 {
        // Gap detected - some output was lost
        fmt.Fprintf(stderr, "[WARNING: %d output chunks lost due to buffer overflow]\n",
            chunk.Sequence - lastSeq - 1)
    }

    lastSeq = chunk.Sequence
    processChunk(chunk)
}
```

**Server gap notification (optional enhancement):**

```protobuf
message OutputChunk {
    uint64 sequence = 1;
    int64 timestamp = 2;
    oneof data { ... }
    bool is_replay = 5;
    uint64 gap_before = 6;  // If >0, indicates sequence gap
}
```

---

## Edge Cases and Error Handling

### Edge Case 1: Client Reconnects After Container Exits

**Scenario:**
1. Container process exits with code 0
2. All clients disconnect
3. Client reconnects 2 minutes later

**Expected Behavior:**
- Reconnection succeeds (within retention timeout)
- Replay includes all buffered output
- Exit code is delivered
- Stream closes normally

**Implementation:**

```go
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
    // ... session setup ...

    // Check if already exited
    instance.mu.RLock()
    status := instance.status
    exitCode := instance.exitCode
    instance.mu.RUnlock()

    if status == ExecStatusExited {
        // Session already completed
        bklog.G(ctx).Infof("reconnecting to completed session (exit %d)", *exitCode)

        // Send Ready
        srv.Send(&SessionResponse{Msg: &SessionResponse_Ready{...}})

        // Replay output
        e.replayOutputHistory(ctx, srv, instance, startMsg.ReplayFromSequence)

        // Send exit code
        srv.Send(&SessionResponse{
            Msg: &SessionResponse_Exit{Exit: *exitCode},
        })

        // Done
        return nil
    }

    // ... continue with live session ...
}
```

### Edge Case 2: Multiple Clients Connect to Same TTY

**Scenario:**
1. Client A connects and starts interactive shell
2. Client B connects to the same session
3. Both clients send stdin simultaneously

**Policy Decision:** **Allow multiple clients with stdin arbitration**

**Behavior:**
- **Output**: All clients receive the same output (multiplexed)
- **Stdin**: All clients can send stdin (multiplexed to same PTY)
- **Resize**: Last resize wins (clients should coordinate or use same size)
- **Exit**: Any client can trigger exit (send Ctrl-C, Ctrl-D)

**Rationale:**
- Enables collaborative debugging (pair programming)
- Mimics `screen` or `tmux` multi-attach behavior
- Simple implementation (no arbitration needed)

**Alternatives considered:**
1. **Single writer**: Only first client can send stdin
   - Con: Requires complex hand-off protocol
2. **Exclusive mode**: Only one client at a time
   - Con: Breaks reconnection use case
3. **Current choice**: Free-for-all (document and let users coordinate)
   - Pro: Simple, flexible, matches existing tools

**Documentation:**

```go
// AddClient registers a new client connection.
//
// Note: Multiple clients can connect to the same TTY session simultaneously.
// All clients receive the same output, and all clients can send stdin/resize.
// Clients should coordinate to avoid conflicts (e.g., use same terminal size).
// This behavior is similar to tmux/screen multi-attach mode.
func (e *ExecInstance) AddClient(clientID string, isReconnect bool) int {
    // ...
}
```

### Edge Case 3: Session Timeout vs Container Timeout

**Scenario:**
1. Long-running container (e.g., daemon process)
2. All clients disconnect
3. Session orphan timeout (5 min) < container still running

**Question:** Should session timeout kill the container?

**Policy Decision:** **No - session timeout only cleans up session state, not container**

**Behavior:**
- Session cleanup removes `ExecInstance` from registry
- Container continues running (detached)
- New connection with same container ID creates new session (new session ID)
- Original session output is lost (not recoverable)

**Rationale:**
- Exec sessions are observability attachments, not lifecycle controllers
- Container lifecycle managed by higher-level controller (buildkit executor)
- Matches Docker `docker exec` behavior (exec session != container lifecycle)

**Implementation:**

```go
func (r *ExecSessionRegistry) cleanupExitedInstances() {
    // ... existing cleanup logic ...

    for key, instance := range toCleanup {
        // Cleanup session state ONLY
        instance.Cleanup()  // Closes channels, cancels context

        // Do NOT kill the container
        // Container lifecycle is independent of session lifecycle

        bklog.G(r.ctx).Infof("cleaned up orphaned session %s (container may still be running)",
            instance.sessionID)
    }
}
```

### Edge Case 4: Buffer Overflow Mid-Session

**Scenario:**
1. Container produces output faster than client can consume
2. Circular buffer fills and starts evicting old chunks
3. Client catches up and continues reading

**Expected Behavior:**
- Old data is lost (FIFO eviction)
- Client receives sequence gap warning
- Streaming continues normally

**Client handling:**

```go
// Client should handle gaps gracefully
func handleOutputChunk(chunk *OutputChunk) {
    if chunk.GapBefore > 0 {
        log.Printf("[%d output chunks lost]", chunk.GapBefore)
    }

    // Process chunk normally
    os.Stdout.Write(chunk.Data)
}
```

### Edge Case 5: Rapid Reconnection Loop

**Scenario:**
1. Client has network issues causing repeated reconnects
2. Each reconnect triggers full output replay
3. Server resources exhausted

**Mitigation:** **Rate limit reconnections per client**

```go
type ReconnectRateLimit struct {
    mu              sync.Mutex
    attempts        map[string][]time.Time  // clientID -> timestamps
    maxAttempts     int
    window          time.Duration
}

func (rl *ReconnectRateLimit) Allow(clientID string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    now := time.Now()
    cutoff := now.Add(-rl.window)

    // Prune old attempts
    attempts := rl.attempts[clientID]
    var recent []time.Time
    for _, t := range attempts {
        if t.After(cutoff) {
            recent = append(recent, t)
        }
    }

    if len(recent) >= rl.maxAttempts {
        return false  // Rate limited
    }

    recent = append(recent, now)
    rl.attempts[clientID] = recent
    return true
}

// Configuration
const (
    ReconnectMaxAttempts = 10
    ReconnectWindow = 1 * time.Minute
)
```

**Usage:**

```go
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
    // ...
    if isReconnect {
        if !e.reconnectRateLimit.Allow(clientID) {
            return status.Errorf(codes.ResourceExhausted,
                "reconnection rate limit exceeded for client %s", clientID)
        }
    }
    // ...
}
```

### Edge Case 6: Race Between Cleanup and Reconnection

**Scenario:**
1. Session enters orphan state (no clients)
2. Cleanup timer fires
3. Client reconnects during cleanup

**Expected Behavior:**
- Reconnection fails with `NotFound` error
- Client detects stale session and retries with new session

**Implementation:**

```go
func (r *ExecSessionRegistry) Unregister(containerID, execID string) error {
    key := makeKey(containerID, execID)

    r.mu.Lock()
    instance, exists := r.instances[key]
    if !exists {
        r.mu.Unlock()
        return fmt.Errorf("instance not found")
    }

    // Atomically remove from registry
    delete(r.instances, key)
    r.mu.Unlock()

    // Cleanup outside lock
    instance.Cleanup()

    return nil
}

func (r *ExecSessionRegistry) GetInstanceBySessionID(sessionID string) (*ExecInstance, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    // Linear search (could optimize with sessionID index)
    for _, instance := range r.instances {
        if instance.sessionID == sessionID {
            return instance, nil
        }
    }

    return nil, fmt.Errorf("session %s not found", sessionID)
}
```

**Client retry logic:**

```go
func connectOrReconnect(sessionID string) (*Session, error) {
    if sessionID != "" {
        // Try reconnection
        sess, err := reconnect(sessionID)
        if err == nil {
            return sess, nil
        }

        // Reconnection failed - treat as new session
        log.Printf("Reconnection failed: %v (starting new session)", err)
    }

    // Start new session
    return connect()
}
```

---

## Integration Points

### 1. ExecInstance Enhancement

**File:** `/home/berezovyy/Projects/dagger/engine/session/exec/registry.go`

**Changes:**

```go
// Add TTY-specific fields to ExecInstance
type ExecInstance struct {
    // ... existing fields ...

    // New fields for TTY support
    sessionID     string
    isTTY         bool
    ttyState      *TTYState
    outputHistory *OutputHistory
    stdinChan     chan []byte
    resizeChan    chan WinSize
    lastActivity  time.Time

    // Change clients to track metadata
    clients       map[string]*ClientInfo  // was map[string]struct{}
}

// Update constructor
func NewExecInstance(ctx context.Context, containerID, execID string) *ExecInstance {
    // ... existing code ...

    return &ExecInstance{
        // ... existing fields ...
        clients: make(map[string]*ClientInfo),
    }
}

// Implement stdin/resize methods (currently stubs)
func (e *ExecInstance) WriteStdin(data []byte) error {
    // Implementation from "Stdin Implementation" section
}

func (e *ExecInstance) SendResize(width, height uint32) error {
    // Implementation from "Resize Implementation" section
}

// Add new methods
func (e *ExecInstance) GetStdinReader() io.Reader { /* ... */ }
func (e *ExecInstance) GetResizeChannel() <-chan WinSize { /* ... */ }
func (e *ExecInstance) GetCurrentSize() *WinSize { /* ... */ }
func (e *ExecInstance) AddClient(clientID string, isReconnect bool) int { /* ... */ }
```

### 2. ExecSessionRegistry Enhancement

**File:** `/home/berezovyy/Projects/dagger/engine/session/exec/registry.go`

**Changes:**

```go
type ExecSessionRegistry struct {
    // ... existing fields ...

    // New index for session ID lookups
    sessionIndex map[string]*ExecInstance  // sessionID -> instance
}

// Add session ID lookup
func (r *ExecSessionRegistry) GetInstanceBySessionID(sessionID string) (*ExecInstance, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    instance, ok := r.sessionIndex[sessionID]
    if !ok {
        return nil, fmt.Errorf("session %s not found", sessionID)
    }

    return instance, nil
}

// Update Register to create session ID and index
func (r *ExecSessionRegistry) Register(containerID, execID string) (*ExecInstance, error) {
    // ... existing code ...

    instance := NewExecInstance(r.ctx, containerID, execID)
    instance.sessionID = generateSessionID(containerID, execID)

    r.mu.Lock()
    r.instances[key] = instance
    r.sessionIndex[instance.sessionID] = instance  // New
    r.mu.Unlock()

    return instance, nil
}

// Update Unregister to remove from session index
func (r *ExecSessionRegistry) Unregister(containerID, execID string) error {
    // ... existing code ...

    r.mu.Lock()
    instance := r.instances[key]
    delete(r.instances, key)
    if instance != nil {
        delete(r.sessionIndex, instance.sessionID)  // New
    }
    r.mu.Unlock()

    // ... cleanup ...
}
```

### 3. ExecAttachable Session Handler

**File:** `/home/berezovyy/Projects/dagger/engine/session/exec/exec.go`

**Changes:**

```go
func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
    // Replace existing implementation with reconnection-aware version
    // (Implementation from "Reconnection Detection Logic" section)
}

func (e *ExecAttachable) handleClientMessages(
    ctx context.Context,
    srv Exec_SessionServer,
    instance *ExecInstance,
) error {
    // Update stdin/resize handling to call new methods
    for {
        req, err := srv.Recv()
        // ... error handling ...

        switch msg := req.GetMsg().(type) {
        case *SessionRequest_Stdin:
            // Now calls working implementation
            if err := instance.WriteStdin(msg.Stdin); err != nil {
                return fmt.Errorf("writing stdin: %w", err)
            }

        case *SessionRequest_Resize:
            // Now calls working implementation
            if err := instance.SendResize(
                uint32(msg.Resize.Width),
                uint32(msg.Resize.Height),
            ); err != nil {
                return fmt.Errorf("sending resize: %w", err)
            }
        }
    }
}

// Add new method for output replay
func (e *ExecAttachable) replayOutputHistory(
    ctx context.Context,
    srv Exec_SessionServer,
    instance *ExecInstance,
    fromSequence uint64,
) error {
    // Implementation from "Output Replay Strategy" section
}
```

### 4. Executor PTY Integration

**File:** `/home/berezovyy/Projects/dagger/engine/buildkit/executor.go`

**Changes:**

```go
// Existing PTY code (lines 757-851) needs to integrate with ExecInstance

func (w *runcExecutor) Run(
    ctx context.Context,
    id string,
    rootfs executor.Mount,
    mounts []executor.Mount,
    process executor.ProcessInfo,
    started chan<- struct{},
) (resourcestypes.Recorder, error) {
    // ... existing setup ...

    if process.Meta.Tty {
        // Get ExecInstance from context or registry
        instance := getExecInstanceFromContext(ctx)
        if instance != nil && instance.isTTY {
            // Wire up PTY to ExecInstance channels
            return w.runWithTTYSession(ctx, id, rootfs, mounts, process, started, instance)
        }

        // Fallback to existing PTY code
        return w.runWithPTY(ctx, id, rootfs, mounts, process, started)
    }

    // ... existing non-TTY code ...
}

func (w *runcExecutor) runWithTTYSession(
    ctx context.Context,
    id string,
    rootfs executor.Mount,
    mounts []executor.Mount,
    process executor.ProcessInfo,
    started chan<- struct{},
    instance *ExecInstance,
) (resourcestypes.Recorder, error) {
    // Create PTY (existing code)
    ptm, ptsName, err := console.NewPty()
    if err != nil {
        return nil, err
    }

    pts, err := os.OpenFile(ptsName, os.O_RDWR|syscall.O_NOCTTY, 0)
    if err != nil {
        ptm.Close()
        return nil, err
    }

    // Store PTY in instance
    instance.ttyState.mu.Lock()
    instance.ttyState.ptmx = ptm
    instance.ttyState.pts = pts
    instance.ttyState.mu.Unlock()

    eg, ctx := errgroup.WithContext(ctx)

    // Stdin forwarding: ExecInstance.stdinReader → PTM
    stdinReader := instance.GetStdinReader()
    if stdinReader != nil {
        eg.Go(func() error {
            _, err := io.Copy(ptm, stdinReader)
            if errors.Is(err, io.ErrClosedPipe) {
                return nil
            }
            return err
        })
    }

    // Stdout forwarding: PTM → ExecInstance.stdoutWriter
    stdoutWriter := instance.GetStdoutWriter()
    eg.Go(func() error {
        _, err := io.Copy(stdoutWriter, ptm)
        // ... handle PTM close errors ...
        return err
    })

    // Resize handling: ExecInstance.resizeChannel → PTM
    resizeChan := instance.GetResizeChannel()
    eg.Go(func() error {
        for {
            select {
            case <-ctx.Done():
                return nil
            case resize := <-resizeChan:
                err = ptm.Resize(console.WinSize{
                    Height: uint16(resize.Rows),
                    Width:  uint16(resize.Cols),
                })
                if err != nil {
                    bklog.G(ctx).Errorf("failed to resize ptm: %s", err)
                }

                // Send SIGWINCH to container
                err = runcProcess.monitorProcess.Signal(signal.SIGWINCH)
                if err != nil {
                    bklog.G(ctx).Errorf("failed to send SIGWINCH: %s", err)
                }
            }
        }
    })

    // ... rest of existing PTY setup ...
}
```

### 5. Context Propagation

**Mechanism:** Pass ExecInstance through context

```go
type execInstanceKeyType struct{}

var execInstanceKey = execInstanceKeyType{}

func WithExecInstance(ctx context.Context, instance *ExecInstance) context.Context {
    return context.WithValue(ctx, execInstanceKey, instance)
}

func GetExecInstanceFromContext(ctx context.Context) *ExecInstance {
    instance, _ := ctx.Value(execInstanceKey).(*ExecInstance)
    return instance
}
```

**Usage in buildkit:**

```go
// When starting an exec
instance, err := execAttachable.RegisterExecution(containerID, execID)
if err != nil {
    return err
}

ctx = WithExecInstance(ctx, instance)
recorder, err := executor.Run(ctx, containerID, rootfs, mounts, processInfo, started)
```

### 6. Proto Updates

**File:** `/home/berezovyy/Projects/dagger/engine/session/exec/exec.proto`

**Changes:**

```protobuf
message Start {
    // ... existing fields ...

    // New fields for reconnection (already shown in design)
    string session_id = 7;
    uint64 replay_from_sequence = 8;
    string client_id = 9;
}

message Ready {
    string session_id = 1;
    Resize terminal_size = 2;
    bool replay_complete = 3;
}

message SessionResponse {
    oneof msg {
        bytes stdout = 1;
        bytes stderr = 2;
        int32 exit = 3;
        Ready ready = 4;
        OutputChunk output_chunk = 5;  // New
    }
}

message OutputChunk {
    uint64 sequence = 1;
    int64 timestamp = 2;
    oneof data {
        bytes stdout = 3;
        bytes stderr = 4;
    }
    bool is_replay = 5;
    uint64 gap_before = 6;
}
```

**Generate code:**

```bash
cd /home/berezovyy/Projects/dagger/engine/session/exec
make generate  # or whatever the proto generation command is
```

---

## Security Considerations

### 1. Session ID Security

**Threat:** Session ID prediction/guessing allows unauthorized access

**Mitigation:**
- Use cryptographically secure random number generator
- 64-bit random component provides 2^64 keyspace
- Timestamp component prevents replay attacks
- Session IDs are single-use per connection

```go
import "crypto/rand"

func generateSecureSessionID(containerID, execID string) string {
    timestamp := time.Now().UnixNano()

    var randomBytes [8]byte
    _, err := rand.Read(randomBytes[:])
    if err != nil {
        panic(fmt.Sprintf("crypto/rand failed: %v", err))
    }

    random := binary.BigEndian.Uint64(randomBytes[:])
    return fmt.Sprintf("%s:%s:%d-%016x", containerID, execID, timestamp, random)
}
```

### 2. Output History Privacy

**Threat:** Sensitive data (passwords, secrets) in output history exposed on reconnection

**Mitigation:**
- Output history has limited lifetime (5 minutes after exit)
- Session IDs required to access history (no enumeration)
- Consider: option to disable history buffering for sensitive sessions
- Consider: scrubbing patterns (e.g., `password=***`)

```go
// Optional: disable history for sensitive sessions
func (e *ExecInstance) SetHistoryEnabled(enabled bool) {
    if !enabled && e.outputHistory != nil {
        e.outputHistory.stdoutBuffer = NewCircularBuffer(0)  // Zero capacity
        e.outputHistory.stderrBuffer = NewCircularBuffer(0)
    }
}

// Usage
if startMsg.DisableOutputHistory {
    instance.SetHistoryEnabled(false)
}
```

### 3. Stdin Injection

**Threat:** Malicious client sends crafted stdin to exploit container

**Mitigation:**
- Stdin is already isolated per container (no cross-container injection)
- Rate limiting prevents stdin flood attacks
- Max chunk size prevents memory exhaustion
- Backpressure prevents buffer overflow

```go
const (
    StdinMaxChunkSize = 16 * 1024  // 16KB
    StdinRateLimit = 1 * 1024 * 1024 / time.Second  // 1MB/s
)

// Enforce in WriteStdin
func (e *ExecInstance) WriteStdin(data []byte) error {
    if len(data) > StdinMaxChunkSize {
        return fmt.Errorf("stdin chunk too large: %d > %d", len(data), StdinMaxChunkSize)
    }

    // Rate limiting
    if !e.stdinRateLimit.Allow() {
        return fmt.Errorf("stdin rate limit exceeded")
    }

    // ... rest of implementation ...
}
```

### 4. Resource Exhaustion

**Threat:** Malicious client creates many sessions to exhaust memory

**Mitigation:**
- Max sessions per container limit
- Global session limit
- Aggressive cleanup of orphaned sessions
- Memory limits per session (fixed buffer sizes)

```go
const (
    MaxSessionsPerContainer = 10
    MaxSessionsGlobal = 1000
)

func (r *ExecSessionRegistry) Register(containerID, execID string) (*ExecInstance, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    // Check global limit
    if len(r.instances) >= MaxSessionsGlobal {
        return nil, fmt.Errorf("global session limit reached")
    }

    // Check per-container limit
    containerSessions := 0
    for _, instance := range r.instances {
        if instance.containerID == containerID {
            containerSessions++
        }
    }

    if containerSessions >= MaxSessionsPerContainer {
        return nil, fmt.Errorf("container session limit reached")
    }

    // ... proceed with registration ...
}
```

### 5. Access Control

**Threat:** Client reconnects to session they don't own

**Mitigation (future enhancement):**
- Associate session with client authentication token
- Verify token on reconnection
- For now: session ID secrecy provides basic protection

```go
// Future: add client auth
type ExecInstance struct {
    // ...
    ownerToken string  // Authentication token of session creator
}

func (e *ExecAttachable) Session(srv Exec_SessionServer) error {
    // Extract auth token from gRPC metadata
    token := getAuthTokenFromContext(srv.Context())

    if isReconnect {
        instance, _ := e.registry.GetInstanceBySessionID(startMsg.SessionId)

        // Verify ownership
        if instance.ownerToken != token {
            return status.Errorf(codes.PermissionDenied, "not authorized for this session")
        }
    } else {
        instance.ownerToken = token
    }

    // ...
}
```

---

## Resource Limits and Policies

### Memory Limits

| Resource | Limit | Justification |
|----------|-------|---------------|
| Output history per session | 128 MB | 2048 chunks × 64KB max |
| Typical per session | 2 MB | 2048 chunks × 1KB typical |
| Stdin buffer per session | 100 KB | 100 chunks × 1KB |
| Resize buffer per session | ~400 B | 10 × 40 bytes |
| Total per session (worst) | ~130 MB | Sum of above |
| Max sessions global | 1000 | ~130 GB worst case, ~2 GB typical |
| Max sessions per container | 10 | Prevents single container monopolizing |

### CPU Limits

| Operation | Target Latency | Mitigation |
|-----------|----------------|------------|
| Session registration | < 1ms | Lock-free reads, minimal allocations |
| Output chunk append | < 100μs | Lock-free write, pre-allocated buffer |
| Output replay | < 100ms | Bounded buffer size, streaming |
| Cleanup scan | < 10ms | Periodic (30s), incremental |
| Stdin write | < 1ms | Non-blocking send |
| Resize send | < 1ms | Non-blocking, debounced |

### Network Limits

| Flow | Limit | Enforcement |
|------|-------|-------------|
| Stdin rate | 1 MB/s per session | Token bucket rate limiter |
| Output rate | Unlimited | Relies on container output rate + gRPC flow control |
| Reconnect rate | 10/min per client | Sliding window rate limiter |
| gRPC stream size | System default | Uses standard gRPC settings |

### Timeout Policies (Summary)

| Timeout | Value | Purpose |
|---------|-------|---------|
| Session orphan | 5 min | Keep session with no clients |
| Output retention | 5 min | Keep history after exit |
| Max idle | 1 hour | Force cleanup stale sessions |
| Stdin idle | 30 sec | Detect hung stdin writers |
| Resize debounce | 50 ms | Coalesce resize events |
| Cleanup interval | 30 sec | Background cleanup frequency |

---

## API Specifications

### Client API (Go SDK Example)

```go
package exec

import (
    "context"
    "io"
)

// TTYSession represents an interactive TTY session with a container
type TTYSession struct {
    sessionID string
    stream    Exec_SessionClient
    stdin     io.WriteCloser
    stdout    io.Reader
    stderr    io.Reader
    resize    chan WinSize
    exit      chan int32
}

// Connect starts a new TTY session
func Connect(ctx context.Context, client ExecClient, opts *ConnectOptions) (*TTYSession, error) {
    stream, err := client.Session(ctx)
    if err != nil {
        return nil, err
    }

    // Send Start message
    err = stream.Send(&SessionRequest{
        Msg: &SessionRequest_Start{
            Start: &Start{
                ContainerId: opts.ContainerID,
                ExecId:      opts.ExecID,
                Command:     opts.Command,
                Env:         opts.Env,
                WorkingDir:  opts.WorkingDir,
                Tty:         true,
                ClientId:    opts.ClientID,
            },
        },
    })
    if err != nil {
        return nil, err
    }

    // Wait for Ready
    resp, err := stream.Recv()
    if err != nil {
        return nil, err
    }

    ready := resp.GetReady()
    if ready == nil {
        return nil, fmt.Errorf("expected Ready, got %T", resp.Msg)
    }

    session := &TTYSession{
        sessionID: ready.SessionId,
        stream:    stream,
        resize:    make(chan WinSize, 1),
        exit:      make(chan int32, 1),
    }

    // Setup I/O pipes
    session.stdin = &stdinWriter{stream: stream}
    session.stdout = &outputReader{stream: stream, streamType: StreamStdout}
    session.stderr = &outputReader{stream: stream, streamType: StreamStderr}

    // Start background goroutines
    go session.sendResizes()
    go session.receiveOutput()

    return session, nil
}

// Reconnect resumes an existing TTY session
func Reconnect(ctx context.Context, client ExecClient, sessionID string, opts *ReconnectOptions) (*TTYSession, error) {
    stream, err := client.Session(ctx)
    if err != nil {
        return nil, err
    }

    // Send Start with session ID
    err = stream.Send(&SessionRequest{
        Msg: &SessionRequest_Start{
            Start: &Start{
                SessionId:          sessionID,
                ReplayFromSequence: opts.ReplayFromSequence,
                ClientId:           opts.ClientID,
            },
        },
    })
    if err != nil {
        return nil, err
    }

    // Wait for Ready
    resp, err := stream.Recv()
    if err != nil {
        return nil, err
    }

    ready := resp.GetReady()
    if ready == nil {
        return nil, fmt.Errorf("expected Ready, got %T", resp.Msg)
    }

    if ready.SessionId != sessionID {
        return nil, fmt.Errorf("session ID mismatch: want %s, got %s", sessionID, ready.SessionId)
    }

    // ... similar setup as Connect ...
}

// SessionID returns the unique session identifier for reconnection
func (s *TTYSession) SessionID() string {
    return s.sessionID
}

// Stdin returns a writer for sending input to the container
func (s *TTYSession) Stdin() io.WriteCloser {
    return s.stdin
}

// Stdout returns a reader for receiving stdout from the container
func (s *TTYSession) Stdout() io.Reader {
    return s.stdout
}

// Stderr returns a reader for receiving stderr from the container
func (s *TTYSession) Stderr() io.Reader {
    return s.stderr
}

// Resize sends a terminal resize event
func (s *TTYSession) Resize(rows, cols uint32) error {
    select {
    case s.resize <- WinSize{Rows: rows, Cols: cols}:
        return nil
    default:
        return fmt.Errorf("resize channel full")
    }
}

// Wait waits for the session to exit and returns the exit code
func (s *TTYSession) Wait() (int32, error) {
    exitCode := <-s.exit
    return exitCode, nil
}

// Close closes the session
func (s *TTYSession) Close() error {
    return s.stream.CloseSend()
}

// ConnectOptions configures a new session
type ConnectOptions struct {
    ContainerID string
    ExecID      string
    Command     []string
    Env         map[string]string
    WorkingDir  string
    ClientID    string  // Optional
}

// ReconnectOptions configures reconnection
type ReconnectOptions struct {
    ReplayFromSequence uint64  // 0 = replay all available history
    ClientID           string  // Optional
}
```

### Usage Example

```go
// Start new session
session, err := exec.Connect(ctx, client, &exec.ConnectOptions{
    ContainerID: "container-123",
    ExecID:      "exec-456",
    Command:     []string{"/bin/bash"},
    Env:         map[string]string{"TERM": "xterm-256color"},
})
if err != nil {
    log.Fatal(err)
}

// Save session ID for reconnection
sessionID := session.SessionID()
fmt.Printf("Session ID: %s\n", sessionID)

// Connect stdin/stdout/stderr
go io.Copy(session.Stdin(), os.Stdin)
go io.Copy(os.Stdout, session.Stdout())
go io.Copy(os.Stderr, session.Stderr())

// Handle terminal resize
go func() {
    for {
        rows, cols := getTerminalSize()
        session.Resize(rows, cols)
        time.Sleep(1 * time.Second)
    }
}()

// Wait for exit
exitCode, err := session.Wait()
fmt.Printf("Exited with code %d\n", exitCode)

// Later: reconnect to the same session
session2, err := exec.Reconnect(ctx, client, sessionID, &exec.ReconnectOptions{
    ReplayFromSequence: 0,  // Replay all history
})
if err != nil {
    log.Fatal(err)
}

// Resume interaction
go io.Copy(os.Stdout, session2.Stdout())
// ...
```

---

## Sequence Diagrams

### Diagram 1: Initial TTY Session Connection

```
┌────────┐         ┌──────────┐         ┌──────────┐         ┌──────────┐         ┌──────────┐
│ Client │         │  gRPC    │         │ Exec     │         │  Exec    │         │ Executor │
│        │         │ Stream   │         │Attachable│         │ Instance │         │  (PTY)   │
└───┬────┘         └────┬─────┘         └────┬─────┘         └────┬─────┘         └────┬─────┘
    │                   │                    │                    │                    │
    │ Start(tty=true)   │                    │                    │                    │
    ├──────────────────→│                    │                    │                    │
    │                   │ Start msg          │                    │                    │
    │                   ├───────────────────→│                    │                    │
    │                   │                    │ Register(...)      │                    │
    │                   │                    ├───────────────────→│                    │
    │                   │                    │                    │ NewExecInstance    │
    │                   │                    │                    │ (tty=true)         │
    │                   │                    │                    │ Generate sessionID │
    │                   │                    │                    │ Init TTYState      │
    │                   │                    │                    │ Init OutputHistory │
    │                   │                    │←───────────────────┤                    │
    │                   │                    │ AddClient()        │                    │
    │                   │                    ├───────────────────→│                    │
    │                   │                    │                    │ Client registered  │
    │                   │                    │←───────────────────┤                    │
    │                   │ Ready(sessionID)   │                    │                    │
    │                   │←───────────────────┤                    │                    │
    │ Ready             │                    │                    │                    │
    │←──────────────────┤                    │                    │                    │
    │                   │                    │                    │                    │
    │ [User types]      │                    │                    │                    │
    │ Stdin("ls\n")     │                    │                    │                    │
    ├──────────────────→│ WriteStdin()       │                    │                    │
    │                   ├───────────────────→│ stdinChan <- data  │                    │
    │                   │                    ├───────────────────→│ GetStdinReader()   │
    │                   │                    │                    ├───────────────────→│
    │                   │                    │                    │                    │ io.Copy(ptm)
    │                   │                    │                    │                    │
    │                   │                    │                    │ Container writes   │
    │                   │                    │                    │ stdout             │
    │                   │                    │                    │←───────────────────┤
    │                   │                    │                    │ OutputChunk        │
    │                   │                    │                    │ (seq=1, data)      │
    │                   │                    │                    │ → history buffer   │
    │                   │                    │                    │ → stdoutChan       │
    │                   │                    │ Stdout(data)       │                    │
    │                   │←───────────────────┤←───────────────────┤                    │
    │ Stdout(data)      │                    │                    │                    │
    │←──────────────────┤                    │                    │                    │
    │                   │                    │                    │                    │
    │ [User resizes]    │                    │                    │                    │
    │ Resize(80x24)     │                    │                    │                    │
    ├──────────────────→│ SendResize()       │                    │                    │
    │                   ├───────────────────→│ resizeChan <- size │                    │
    │                   │                    ├───────────────────→│ GetResizeChannel() │
    │                   │                    │                    ├───────────────────→│
    │                   │                    │                    │                    │ ptm.Resize()
    │                   │                    │                    │                    │ signal(SIGWINCH)
    │                   │                    │                    │                    │
    │ [Container exits] │                    │                    │                    │
    │                   │                    │                    │ Container exit(0)  │
    │                   │                    │                    │←───────────────────┤
    │                   │                    │ SendExitCode(0)    │                    │
    │                   │                    ├───────────────────→│                    │
    │                   │                    │                    │ MarkExited(0)      │
    │                   │                    │                    │ exitChan <- 0      │
    │                   │                    │ Exit(0)            │                    │
    │                   │←───────────────────┤←───────────────────┤                    │
    │ Exit(0)           │                    │                    │                    │
    │←──────────────────┤                    │                    │                    │
    │                   │                    │                    │                    │
    │ [Close]           │                    │ RemoveClient()     │                    │
    │                   │                    ├───────────────────→│                    │
    │                   │                    │                    │ (session orphaned) │
    │                   │                    │                    │ → cleanup timer    │
```

### Diagram 2: Client Reconnection with Output Replay

```
┌────────┐         ┌──────────┐         ┌──────────┐         ┌──────────┐
│Client 2│         │  gRPC    │         │ Exec     │         │  Exec    │
│(recon) │         │ Stream   │         │Attachable│         │ Instance │
└───┬────┘         └────┬─────┘         └────┬─────┘         └────┬─────┘
    │                   │                    │                    │
    │ Start(            │                    │                    │
    │   sessionID=      │                    │                    │
    │   "abc-123",      │                    │                    │
    │   replayFrom=10)  │                    │                    │
    ├──────────────────→│                    │                    │
    │                   │ Start msg          │                    │
    │                   ├───────────────────→│                    │
    │                   │                    │ GetInstance        │
    │                   │                    │   BySessionID()    │
    │                   │                    ├───────────────────→│
    │                   │                    │ instance (exists)  │
    │                   │                    │←───────────────────┤
    │                   │                    │                    │
    │                   │                    │ AddClient()        │
    │                   │                    │ (isReconnect=true) │
    │                   │                    ├───────────────────→│
    │                   │                    │                    │
    │                   │ Ready(sessionID)   │                    │
    │                   │←───────────────────┤                    │
    │ Ready             │                    │                    │
    │←──────────────────┤                    │                    │
    │                   │                    │                    │
    │                   │                    │ replayOutput()     │
    │                   │                    │ (fromSeq=10)       │
    │                   │                    ├──────────┐         │
    │                   │                    │          │ Lock history
    │                   │                    │          │ GetRange(10, 25)
    │                   │                    │          │ Sort by sequence
    │                   │                    │←─────────┘         │
    │                   │                    │                    │
    │                   │ OutputChunk        │                    │
    │                   │ (seq=10, replay)   │                    │
    │                   │←───────────────────┤                    │
    │ Chunk(seq=10)     │                    │                    │
    │←──────────────────┤                    │                    │
    │                   │ OutputChunk        │                    │
    │                   │ (seq=11, replay)   │                    │
    │                   │←───────────────────┤                    │
    │ Chunk(seq=11)     │                    │                    │
    │←──────────────────┤                    │                    │
    │                   │ ...                │                    │
    │                   │                    │                    │
    │                   │ OutputChunk        │                    │
    │                   │ (seq=25, replay)   │                    │
    │                   │←───────────────────┤                    │
    │ Chunk(seq=25)     │                    │                    │
    │←──────────────────┤                    │                    │
    │                   │                    │                    │
    │                   │ Ready(             │                    │
    │                   │   replayComplete)  │                    │
    │                   │←───────────────────┤                    │
    │ Ready(replay done)│                    │                    │
    │←──────────────────┤                    │                    │
    │                   │                    │                    │
    │                   │ [Continue with live streaming]          │
    │                   │                    │                    │
    │                   │ OutputChunk        │                    │
    │                   │ (seq=26, live)     │                    │
    │                   │←───────────────────┤←───────────────────┤
    │ Chunk(seq=26)     │                    │                    │
    │←──────────────────┤                    │                    │
```

### Diagram 3: Session Cleanup After Timeout

```
┌──────────┐         ┌──────────┐         ┌──────────┐         ┌──────────┐
│ Registry │         │  Cleanup │         │  Exec    │         │Container │
│          │         │  Timer   │         │ Instance │         │          │
└────┬─────┘         └────┬─────┘         └────┬─────┘         └────┬─────┘
     │                    │                    │                    │
     │                    │ [Periodic tick]    │                    │
     │                    │ (every 30s)        │                    │
     │ cleanupExited()    │                    │                    │
     │←───────────────────┤                    │                    │
     │                    │                    │                    │
     │ Lock registry      │                    │                    │
     ├─────┐              │                    │                    │
     │     │ Iterate      │                    │                    │
     │     │ instances    │                    │                    │
     │←────┘              │                    │                    │
     │                    │                    │                    │
     │ Check instance     │                    │                    │
     ├───────────────────────────────────────→│                    │
     │                    │                    │ GetStatus()        │
     │                    │                    │ GetClients()       │
     │                    │                    │ GetExitTime()      │
     │←───────────────────────────────────────┤                    │
     │                    │                    │                    │
     │ Decision:          │                    │                    │
     │ - Exited? Yes      │                    │                    │
     │ - Clients? 0       │                    │                    │
     │ - Time since exit? │                    │                    │
     │   6 minutes        │                    │                    │
     │ → CLEANUP          │                    │                    │
     │                    │                    │                    │
     │ Remove from        │                    │                    │
     │ registry           │                    │                    │
     ├─────┐              │                    │                    │
     │     │ delete()     │                    │                    │
     │←────┘              │                    │                    │
     │                    │                    │                    │
     │ Unlock registry    │                    │                    │
     │                    │                    │                    │
     │ Cleanup()          │                    │                    │
     ├───────────────────────────────────────→│                    │
     │                    │                    │ Cancel context     │
     │                    │                    │ Close channels     │
     │                    │                    │ Close PTY fds      │
     │                    │                    ├─────┐              │
     │                    │                    │     │              │
     │                    │                    │←────┘              │
     │←───────────────────────────────────────┤                    │
     │                    │                    │                    │
     │ Log cleanup        │                    │                    │
     ├─────┐              │                    │                    │
     │     │ "cleaned up  │                    │                    │
     │     │  1 session"  │                    │                    │
     │←────┘              │                    │                    │
     │                    │                    │                    │
     │                    │ [Next tick...]     │                    │
     │                    │                    │                    │

Note: Container process lifecycle is independent of session cleanup.
      Container may still be running after session cleanup.
```

---

## Implementation Roadmap

### Phase 1: Core TTY State Management (Week 1)

**Goals:** Implement basic TTY state structures and session ID management

**Tasks:**
1. Add TTY-specific fields to `ExecInstance`:
   - `sessionID`, `isTTY`, `ttyState`, `lastActivity`
   - `stdinChan`, `resizeChan`
2. Implement `TTYState` struct with PTY file descriptor management
3. Implement session ID generation with crypto/rand
4. Add session ID index to `ExecSessionRegistry`
5. Update `NewExecInstance` constructor to initialize TTY fields
6. Add unit tests for session ID generation (uniqueness, format)
7. Add unit tests for `TTYState` struct

**Deliverables:**
- Updated `registry.go` with TTY fields
- Session ID generation function
- Unit tests (>80% coverage)

**Risks:**
- None (pure data structures, no integration)

### Phase 2: Output History Buffering (Week 1-2)

**Goals:** Implement circular buffer for output history

**Tasks:**
1. Implement `CircularBuffer` struct with:
   - `Append()`, `GetRange()`, `GetOldestSequence()`
2. Implement `OutputHistory` struct with:
   - Dual buffers (stdout/stderr)
   - Sequence number management
3. Implement `OutputAggregator` for chunk creation
4. Add output history to `ExecInstance.GetStdoutWriter()` and `GetStderrWriter()`
5. Unit tests for circular buffer:
   - Test wrap-around behavior
   - Test range queries with various offsets
   - Test concurrent access (race detector)
6. Benchmark buffer performance (target: <100μs append, <10ms replay)

**Deliverables:**
- `buffer.go` with `CircularBuffer` and `OutputHistory`
- `aggregator.go` with `OutputAggregator`
- Unit tests and benchmarks
- Integration with `ExecInstance`

**Risks:**
- Medium: Circular buffer edge cases (wrap-around, empty buffer)
- Mitigation: Extensive unit tests, fuzzing

### Phase 3: Stdin/Resize Pipeline (Week 2)

**Goals:** Implement bidirectional stdin and resize forwarding

**Tasks:**
1. Implement `ExecInstance.WriteStdin()`:
   - Channel send with backpressure
   - EOF handling (empty write)
   - Rate limiting
2. Implement `ExecInstance.GetStdinReader()`:
   - Pipe creation
   - Channel → pipe goroutine
   - Context cancellation
3. Implement `ExecInstance.SendResize()`:
   - Size update
   - Debouncing logic
   - Channel send with overflow handling
4. Implement `TTYState.startResizeDebouncer()`:
   - Ticker-based debouncing
   - Pending resize coalescing
5. Unit tests for stdin:
   - Normal writes
   - EOF signaling
   - Backpressure
   - Context cancellation
6. Unit tests for resize:
   - Single resize
   - Rapid resizes (debouncing)
   - Channel overflow

**Deliverables:**
- Updated `registry.go` with stdin/resize implementations
- Debouncer goroutine
- Unit tests

**Risks:**
- Medium: Goroutine lifecycle management, channel deadlocks
- Mitigation: Careful context cancellation, deadlock detection tests

### Phase 4: Proto and gRPC Updates (Week 2-3)

**Goals:** Extend proto definitions and update gRPC handlers

**Tasks:**
1. Update `exec.proto`:
   - Add `session_id`, `replay_from_sequence`, `client_id` to `Start`
   - Add `Ready` fields: `session_id`, `terminal_size`, `replay_complete`
   - Add `OutputChunk` message
2. Regenerate proto code: `make generate`
3. Update `ExecAttachable.Session()`:
   - Detect reconnection vs new session
   - Call `GetInstanceBySessionID()` for reconnections
   - Generate session ID for new sessions
   - Send `Ready` with session ID
4. Implement `ExecAttachable.replayOutputHistory()`:
   - Lock output history
   - Merge and sort stdout/stderr chunks
   - Stream `OutputChunk` messages
   - Send replay complete signal
5. Update `ExecAttachable.handleClientMessages()`:
   - Call `WriteStdin()` instead of returning error
   - Call `SendResize()` instead of returning error
6. Integration tests:
   - New session flow
   - Reconnection flow
   - Output replay
   - Stdin/resize forwarding

**Deliverables:**
- Updated `exec.proto` and generated code
- Updated `exec.go` with reconnection logic
- Integration tests

**Risks:**
- High: Complex state machine, race conditions
- Mitigation: Careful locking, state machine diagram, extensive testing

### Phase 5: Executor PTY Integration (Week 3-4)

**Goals:** Wire up executor PTY code to ExecInstance channels

**Tasks:**
1. Add context value for `ExecInstance`:
   - `WithExecInstance()`, `GetExecInstanceFromContext()`
2. Update buildkit caller to pass `ExecInstance` in context
3. Update `executor.go` PTY code:
   - Check for `ExecInstance` in context
   - If TTY session: use `instance.GetStdinReader()`
   - If TTY session: use `instance.GetStdoutWriter()`
   - If TTY session: use `instance.GetResizeChannel()`
   - Store PTY master in `instance.ttyState`
4. Handle PTY lifecycle:
   - Open PTY on container start
   - Close PTY on container exit
   - Signal SIGWINCH on resize
5. Integration tests with real containers:
   - Start container with TTY
   - Send stdin
   - Send resize
   - Verify output
   - Verify exit code

**Deliverables:**
- Context propagation utilities
- Updated `executor.go` with session integration
- End-to-end integration tests

**Risks:**
- High: PTY lifecycle, signal handling, race conditions
- Mitigation: Manual testing with real containers, stress tests

### Phase 6: Cleanup and Timeouts (Week 4)

**Goals:** Implement robust session cleanup and timeout policies

**Tasks:**
1. Update `ExecSessionRegistry.cleanupExitedInstances()`:
   - Check orphan timeout
   - Check output retention timeout
   - Check max idle timeout
   - Handle cleanup errors gracefully
2. Implement reconnection rate limiting:
   - `ReconnectRateLimit` struct
   - Integrate into `Session()` handler
3. Add session limits:
   - Max sessions per container
   - Max sessions global
   - Enforce in `Register()`
4. Add metrics/logging:
   - Session creation/destruction
   - Reconnection events
   - Cleanup events
   - Rate limit hits
5. Load testing:
   - Create 1000 sessions
   - Simulate reconnections
   - Verify cleanup
   - Check memory usage

**Deliverables:**
- Updated cleanup logic
- Rate limiting
- Metrics and logging
- Load tests

**Risks:**
- Medium: Resource leaks, race conditions in cleanup
- Mitigation: Leak detection tests, race detector

### Phase 7: Security Hardening (Week 5)

**Goals:** Add security features and access control

**Tasks:**
1. Upgrade to cryptographically secure session ID:
   - Use `crypto/rand` instead of `math/rand`
   - Add tests for randomness
2. Add stdin rate limiting:
   - Token bucket per session
   - Enforce in `WriteStdin()`
3. Add max chunk size enforcement:
   - Check in `WriteStdin()`
   - Check in output aggregator
4. Optional: Add session ownership verification:
   - Extract auth token from gRPC context
   - Store in `ExecInstance`
   - Verify on reconnection
5. Security review:
   - Check for information leaks
   - Check for DoS vectors
   - Check for privilege escalation
6. Penetration testing:
   - Attempt session hijacking
   - Attempt resource exhaustion
   - Attempt injection attacks

**Deliverables:**
- Security-hardened implementation
- Security test suite
- Security review report

**Risks:**
- High: Security vulnerabilities are critical
- Mitigation: External security review, bug bounty

### Phase 8: Documentation and Polish (Week 5-6)

**Goals:** Finalize documentation and client SDK

**Tasks:**
1. Write client SDK (Go):
   - `TTYSession` struct
   - `Connect()`, `Reconnect()` functions
   - Helper methods (Stdin, Stdout, Resize, etc.)
2. Write user documentation:
   - Quickstart guide
   - API reference
   - Reconnection guide
   - Troubleshooting
3. Write operator documentation:
   - Configuration options
   - Tuning parameters
   - Monitoring and metrics
   - Debugging guide
4. Example applications:
   - Interactive shell
   - Log viewer with reconnection
   - Multi-client collaborative session
5. Performance tuning:
   - Optimize critical paths
   - Tune buffer sizes
   - Profile and benchmark
6. Final testing:
   - End-to-end scenarios
   - Failure injection
   - Long-running stability test

**Deliverables:**
- Client SDK
- User documentation
- Operator documentation
- Example applications
- Performance report

**Risks:**
- Low: Mostly documentation
- Mitigation: User feedback, iteration

---

## Appendix A: Configuration Parameters

```go
// TTY Session Configuration
const (
    // Session Timeouts
    SessionOrphanTimeout          = 5 * time.Minute
    SessionOutputRetentionTimeout = 5 * time.Minute
    SessionMaxIdleTimeout         = 1 * time.Hour
    StdinIdleTimeout              = 30 * time.Second
    ResizeDebounceInterval        = 50 * time.Millisecond

    // Buffer Sizes
    StdoutHistoryCapacity = 1024
    StderrHistoryCapacity = 1024
    StdinChannelSize      = 100
    ResizeChannelSize     = 10
    MaxChunkSize          = 64 * 1024
    TypicalChunkSize      = 1024

    // Rate Limits
    StdinMaxChunkSize       = 16 * 1024
    StdinRateLimitBytesPerSec = 1 * 1024 * 1024
    ReconnectMaxAttempts    = 10
    ReconnectWindow         = 1 * time.Minute

    // Session Limits
    MaxSessionsPerContainer = 10
    MaxSessionsGlobal       = 1000

    // Cleanup
    CleanupInterval = 30 * time.Second
)
```

---

## Appendix B: Error Codes

| Error Code | Scenario | Client Action |
|------------|----------|---------------|
| `NotFound` | Session ID not found | Start new session |
| `InvalidArgument` | Missing required field | Fix request and retry |
| `ResourceExhausted` | Rate limit exceeded | Exponential backoff |
| `ResourceExhausted` | Session limit reached | Wait and retry |
| `Unavailable` | Server overloaded | Exponential backoff |
| `Internal` | Server error | Retry with new session |
| `PermissionDenied` | Not authorized | Authentication required |

---

## Appendix C: Monitoring Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `exec_sessions_total` | Counter | Total sessions created |
| `exec_sessions_active` | Gauge | Currently active sessions |
| `exec_sessions_tty` | Gauge | Active TTY sessions |
| `exec_reconnections_total` | Counter | Total reconnection attempts |
| `exec_reconnections_success` | Counter | Successful reconnections |
| `exec_output_chunks_total` | Counter | Output chunks created |
| `exec_output_buffer_evictions` | Counter | Chunks evicted due to overflow |
| `exec_stdin_bytes_total` | Counter | Stdin bytes forwarded |
| `exec_resize_events_total` | Counter | Resize events processed |
| `exec_session_duration_seconds` | Histogram | Session lifetime |
| `exec_replay_duration_seconds` | Histogram | Output replay duration |
| `exec_cleanup_duration_seconds` | Histogram | Cleanup operation duration |

---

## Summary

This design document provides a comprehensive architecture for TTY session management with reconnection support in Dagger's exec infrastructure. Key features include:

1. **Stateful Session Management**: Unique session IDs, TTY state tracking, and client lifecycle management
2. **Reconnection Support**: Transparent reconnection with output history replay and resumable stdin
3. **Circular Buffer Design**: Fixed-size, bounded-memory output history with sequence tracking
4. **Bidirectional Pipelines**: Stdin forwarding with backpressure, resize coalescing with debouncing
5. **Production-Grade Reliability**: Timeouts, rate limiting, resource limits, and graceful degradation
6. **Security**: Cryptographically secure session IDs, access control hooks, injection prevention
7. **Integration**: Clean integration with existing `ExecSessionRegistry`, `ExecInstance`, and executor PTY code

The implementation roadmap provides a phased approach over 5-6 weeks, with clear deliverables, risks, and mitigations for each phase.

---

**End of Design Document**
