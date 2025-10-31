# TTY Sessions - Interactive Terminal Support

## Overview

TTY (teletypewriter) session support enables interactive terminal access to containers through bidirectional gRPC streaming. This feature provides real-time stdin/stdout/stderr communication, dynamic terminal resizing, and automatic reconnection with output replay.

## Architecture

### Components

**Backend (Go)**:
- `engine/session/exec/exec.proto` - Protocol buffer definitions
- `engine/session/exec/registry.go` - Session management and lifecycle
- `engine/session/exec/exec.go` - gRPC streaming handler
- `engine/session/exec/tty_types.go` - TTY-specific data structures
- `engine/session/exec/output_history.go` - Output buffering and replay
- `engine/session/exec/circular_buffer.go` - High-performance ring buffer

**Frontend (TypeScript SDK)**:
- `sdk/typescript/src/api/tty_session.ts` - TTYSession class
- `sdk/typescript/src/api/container_grpc.ts` - Container extensions
- `sdk/typescript/src/grpc/types.ts` - Protocol types

### Data Flow

```
Client → Container.terminal() → TTYSession.create()
         ↓
    gRPC Session() bidirectional stream
         ↓
ExecAttachable → ExecSessionRegistry → ExecInstance
         ↓
    PTY (pseudo-terminal)
         ↓
    Container process
```

## Key Features

### Session Persistence

Sessions are identified by server-generated IDs and persist on the server, allowing clients to disconnect and reconnect without losing state.

**Session ID Format**: `{containerID}:{execID}:{timestamp}-{random}`

**Lifecycle States**:
- Created: Session registered, awaiting first client
- Active: One or more clients connected
- Orphaned: No clients connected (5-minute timeout)
- Closed: Process exited or explicitly terminated

### Output History

All output is captured in circular buffers with sequence numbering, enabling:
- Gap detection during reconnection
- Partial replay from specific sequence numbers
- Memory-bounded storage (1024 chunks per stream)

**Performance**:
- Append: ~67 ns/operation (zero allocations)
- Retrieval: O(n) where n = requested chunks
- Memory: ~2 MB per session maximum

### Stdin Pipeline

Non-blocking channel-based stdin forwarding with backpressure handling.

**Specifications**:
- Buffer size: 100 messages
- Chunk limit: 16 KB per write
- Backpressure: Returns error when buffer full
- EOF handling: Empty write closes stdin

### Terminal Resize

Debounced resize events to prevent storms during window dragging.

**Configuration**:
- Debounce window: 50ms (configurable)
- Event reduction: ~97% (100 events → 3 events)
- Overflow handling: Latest resize always sent

## Resource Management

### Limits

| Resource | Per-Container | Global |
|----------|--------------|--------|
| Sessions | 10 | 1000 |
| Output history | 1024 chunks | N/A |
| Stdin buffer | 100 messages | N/A |

### Cleanup Policies

**Orphan Timeout**: 5 minutes
- Applies when all clients disconnect
- Session remains available for reconnection
- Automatic cleanup after timeout expires

**Max Idle Timeout**: 1 hour
- Applies to sessions with no client activity
- Prevents resource leaks from abandoned sessions

**Output Retention**: 5 minutes after process exit
- Allows clients to retrieve final output
- Session fully removed after retention period

## Protocol

### Session Creation

**Request** (SessionStart):
```protobuf
message Start {
  string container_id = 1;
  string exec_id = 2;
  repeated string command = 3;
  map<string, string> env = 4;
  string working_dir = 5;
  bool tty = 6;
  string session_id = 7;           // Empty for new session
  uint64 replay_from_sequence = 8; // 0 for full replay
  string client_id = 9;            // Optional tracking
}
```

**Response** (SessionReady):
```protobuf
message Ready {
  string session_id = 1;      // Server-assigned ID
  Resize terminal_size = 2;   // Current terminal size
  bool replay_complete = 3;   // True after replay finishes
}
```

### Stdin Transmission

**Client → Server**:
```protobuf
message SessionRequest {
  bytes stdin = 1;  // Data to write (empty = EOF)
}
```

### Output Reception

**Server → Client**:
```protobuf
message SessionResponse {
  bytes stdout = 1;           // Stdout data
  bytes stderr = 2;           // Stderr data
  int32 exit = 3;             // Exit code (when process exits)
  Ready ready = 4;            // Ready signal
  OutputChunk output_chunk = 5; // Sequenced output (with replay support)
}
```

### Terminal Resize

**Client → Server**:
```protobuf
message SessionRequest {
  Resize resize = 1;
}

message Resize {
  uint32 width = 1;   // Columns
  uint32 height = 2;  // Rows
}
```

## Reconnection Protocol

### Steps

1. **Client stores session metadata**:
   - Session ID (from initial Ready message)
   - Last received sequence number

2. **Client initiates reconnection**:
   - Sends Start with `session_id` field populated
   - Includes `replay_from_sequence` for partial replay

3. **Server handles reconnection**:
   - Validates session exists and is active
   - Sends Ready with current terminal state
   - Replays historical output from requested sequence
   - Sends Ready with `replay_complete = true`
   - Resumes normal streaming

4. **Client processes replay**:
   - Receives historical chunks (marked with `is_replay = true`)
   - Detects gaps via `gap_before` field
   - Transitions to live streaming after replay

### Gap Detection

**Scenario**: Client disconnects at sequence 100, reconnects when server is at sequence 150, but circular buffer wrapped and only has sequences 80-150.

**Server behavior**:
- Detects gap: requested 100, oldest available 80
- Returns chunks starting from 80
- Sets `gap_before` field on first chunk
- Client receives notification of missed data (sequences 1-79)

## Error Handling

### Common Errors

**NotFound**: Session ID not found or expired
- **Resolution**: Create new session with `Container.terminal()`

**FailedPrecondition**: Container not running or TTY not allocated
- **Resolution**: Ensure container is running, retry session creation

**ResourceExhausted**: Session limit reached
- **Resolution**: Close unused sessions, wait for cleanup

**InvalidArgument**: Malformed request (e.g., stdin chunk too large)
- **Resolution**: Verify chunk size < 16 KB, validate input

### Backpressure

When stdin buffer is full, write operations return error. Client should:
1. Reduce send rate
2. Implement exponential backoff
3. Monitor for buffer availability

## TypeScript SDK Usage

### Basic Session

```typescript
const container = dag.container().from("alpine:latest")
const session = await container.terminal()

await session.writeStdin("ls -la\n")
await session.writeStdin("exit\n")

for await (const output of session) {
  if (output.stdout) {
    process.stdout.write(output.stdout)
  }
  if (output.exitCode !== undefined) {
    console.log(`Exited: ${output.exitCode}`)
    break
  }
}
```

### Custom Command

```typescript
const session = await container.terminal({
  command: ["/bin/bash"],
  env: { TERM: "xterm-256color" },
  workingDir: "/app"
})
```

### Reconnection

```typescript
// Initial connection
const session1 = await container.terminal()
const sessionId = session1.sessionId
const lastSeq = session1.lastSequence

// ... disconnect ...

// Reconnect
const session2 = await container.reconnectTerminal(sessionId, {
  replayFromSequence: lastSeq
})
```

### Terminal Resize

```typescript
const session = await container.terminal()

// Set initial size
await session.resize(80, 24)

// Handle terminal size changes
process.stdout.on('resize', async () => {
  const { columns, rows } = process.stdout
  await session.resize(columns, rows)
})
```

## Implementation Details

### Thread Safety

**Lock Hierarchy**:
1. Registry lock (instances map)
2. Instance status lock
3. Clients map lock
4. Stdin channel (lock-free)

**Atomic Operations**:
- Sequence number generation (`atomic.AddUint64`)
- High water mark updates (`atomic.StoreUint64`)

### Memory Management

**Circular Buffers**:
- Fixed capacity: 1024 chunks
- Zero-allocation append via pre-allocated slice
- Wraparound handling with modulo arithmetic

**Output Chunks**:
- Deep copy of data (prevents data races)
- Timestamp capture (nanosecond precision)
- Stream tagging (stdout vs stderr)

### PTY Integration

**Stdin Source**:
- Primary: ExecInstance stdin channel (for TTY sessions)
- Fallback: buildkit process.Stdin (for non-TTY)
- Merged via reader adapter

**Resize Sources**:
- Primary: buildkit resize channel
- Secondary: ExecInstance resize channel
- Merged into single channel for PTY

## Performance Characteristics

| Operation | Latency | Throughput | Allocations |
|-----------|---------|------------|-------------|
| Append output | 67 ns | ~15M ops/sec | 0 |
| Circular buffer | 28 ns | ~35M ops/sec | 0 |
| Stdin write | <1 ms | 16 MB/sec | 1 (copy) |
| Resize event | <50 ms | N/A (debounced) | 0 |

**Scalability**:
- Concurrent sessions: Limited by OS file descriptors
- Memory per session: ~2-3 MB (buffers + overhead)
- CPU overhead: <1% per active session

## Security Considerations

### Session Isolation

- Each session has independent stdin/stdout/stderr streams
- Environment variables are session-scoped
- No cross-session communication

### Authentication

- Session IDs are cryptographically random (64 bits entropy)
- Client ID is optional and used for tracking only
- Access control delegated to container-level permissions

### Resource Protection

- Hard limits prevent resource exhaustion
- Automatic cleanup prevents abandoned sessions
- Backpressure prevents memory overflow

## Testing

### Unit Tests

Located in `engine/session/exec/`:
- `session_id_test.go` - ID generation
- `circular_buffer_test.go` - Ring buffer operations
- `output_history_test.go` - History management
- `stdin_test.go` - Stdin pipeline
- `resize_test.go` - Resize debouncing
- `session_reconnect_test.go` - Reconnection logic
- `replay_test.go` - Output replay
- `cleanup_test.go` - Resource cleanup

### Integration Tests

Located in `sdk/typescript/src/api/test/`:
- `container-tty.spec.ts` - End-to-end TTY tests (36 tests)

**Test Coverage**:
- Session creation and basic I/O
- Stdin operations (sequence, buffers, rapid writes)
- Terminal resize (dynamic, multiple, edge cases)
- Reconnection with replay
- Exit codes and error handling
- Concurrent sessions
- Real-world scenarios (CLI tools, file editing)

### Running Tests

**Backend**:
```bash
cd engine/session/exec
go test -v
```

**TypeScript SDK**:
```bash
cd sdk/typescript
npm test
```

## Troubleshooting

### Session Not Found

**Symptom**: `NotFound` error when reconnecting

**Causes**:
- Session expired (orphan timeout)
- Session cleaned up (process exited > 5 minutes ago)
- Invalid session ID

**Resolution**:
- Verify session ID is correct
- Check time since last disconnect (must be < 5 minutes for orphaned sessions)
- Create new session if expired

### Stdin Backpressure

**Symptom**: "stdin buffer full (backpressure)" error

**Causes**:
- Client sending data faster than process consumes
- Process blocked (waiting for input, paused, deadlocked)

**Resolution**:
- Implement rate limiting on client
- Verify process is actively reading stdin
- Check for process deadlock or pause state

### Output Gaps

**Symptom**: `gap_before` field set in output chunks

**Causes**:
- Client disconnected for extended period
- Circular buffer wrapped (> 1024 chunks generated)

**Resolution**:
- Reduce disconnect duration
- Increase reconnection frequency
- Accept data loss for very long disconnections

### High Memory Usage

**Symptom**: Unexpectedly high memory consumption

**Causes**:
- Too many concurrent sessions
- Output history not cleaned up (process still running)
- Memory leak (rare)

**Resolution**:
- Review session count (check global and per-container limits)
- Close finished sessions explicitly
- Monitor cleanup goroutine activity

## Future Enhancements

### Planned Features

- **Configurable buffer sizes**: Allow tuning per session
- **Compression**: Optional gzip for output chunks
- **Multi-attach**: Multiple clients to same stdin/stdout
- **Recording**: Built-in session recording/playback
- **Metrics**: Prometheus metrics for session activity

### Under Consideration

- **WebSocket transport**: Browser-based terminal access
- **Session sharing**: Collaborative terminal sessions
- **Access control**: Fine-grained permissions per session
- **Audit logging**: Track all session activity

## References

### Related Documentation

- `STREAMING_ERROR_HANDLING_STRATEGY.md` - LLM streaming patterns
- `engine/session/exec/exec.proto` - Protocol definitions
- `sdk/typescript/src/api/tty_session.ts` - Client implementation

### External Resources

- [PTY man page](https://man7.org/linux/man-pages/man7/pty.7.html)
- [gRPC Bidirectional Streaming](https://grpc.io/docs/what-is-grpc/core-concepts/#bidirectional-streaming-rpc)
- [ANSI Terminal Escape Codes](https://en.wikipedia.org/wiki/ANSI_escape_code)

## Version History

**Version 1.0** (2024-10-31)
- Initial TTY session support
- Session persistence and reconnection
- Output history with replay
- Stdin pipeline with backpressure
- Terminal resize with debouncing
- TypeScript SDK integration
- Comprehensive test coverage
