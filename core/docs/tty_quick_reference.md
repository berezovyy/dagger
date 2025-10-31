# TTY Sessions - Quick Reference

## One-Minute Overview

TTY sessions provide interactive terminal access to containers through bidirectional gRPC streaming with automatic reconnection and output replay.

**Key capabilities**: Real-time I/O, terminal resize, session persistence, reconnection with gap detection.

## Basic Usage

### TypeScript SDK

```typescript
// Create session
const session = await container.terminal()

// Write to stdin
await session.writeStdin("ls -la\n")

// Read output
for await (const out of session) {
  if (out.stdout) process.stdout.write(out.stdout)
  if (out.exitCode !== undefined) break
}

// Resize terminal
await session.resize(120, 40)

// Reconnect
const session2 = await container.reconnectTerminal(
  session.sessionId,
  { replayFromSequence: session.lastSequence }
)
```

## Common Patterns

### Interactive Shell

```typescript
const session = await container.terminal({ command: ["/bin/bash"] })

await session.writeStdin("export VAR=value\n")
await session.writeStdin("echo $VAR\n")
await session.writeStdin("exit\n")
```

### Long-Running Process

```typescript
const session = await container.terminal({
  command: ["npm", "run", "dev"],
  workingDir: "/app"
})

// Stream logs in background
(async () => {
  for await (const out of session) {
    if (out.stdout) console.log(out.stdout.toString())
  }
})()

// Send Ctrl+C after 10 seconds
setTimeout(() => session.writeStdin("\x03"), 10000)
```

### Reconnection Pattern

```typescript
// Store session metadata
const sessionId = session.sessionId
const lastSeq = session.lastSequence

// Reconnect after disconnect
try {
  const newSession = await container.reconnectTerminal(sessionId, {
    replayFromSequence: lastSeq
  })
} catch (err) {
  // Session expired, create new one
  const newSession = await container.terminal()
}
```

## Resource Limits

| Resource | Limit | Action on Exceed |
|----------|-------|------------------|
| Sessions per container | 10 | ResourceExhausted error |
| Global sessions | 1000 | ResourceExhausted error |
| Stdin chunk size | 16 KB | InvalidArgument error |
| Stdin buffer | 100 messages | Backpressure error |
| Output history | 1024 chunks | Oldest data discarded |

## Timeout Policies

| Condition | Timeout | Result |
|-----------|---------|--------|
| No clients connected | 5 minutes | Session cleaned up |
| No client activity | 1 hour | Session cleaned up |
| Process exited | 5 minutes | Session and history removed |

## Error Codes

| Code | Cause | Fix |
|------|-------|-----|
| NotFound | Session expired/invalid | Create new session |
| FailedPrecondition | Container not running | Verify container state |
| ResourceExhausted | Limit reached | Close unused sessions |
| InvalidArgument | Malformed request | Check input size/format |

## Performance Metrics

| Operation | Latency | Allocations |
|-----------|---------|-------------|
| Append output | 67 ns | 0 |
| Circular buffer | 28 ns | 0 |
| Stdin write | <1 ms | 1 |
| Resize event | <50 ms | 0 |

## Protocol Messages

### Create Session
```typescript
{
  start: {
    container_id: "abc123",
    exec_id: "abc123",
    command: ["/bin/sh"],
    tty: true
  }
}
```

### Reconnect
```typescript
{
  start: {
    container_id: "abc123",
    exec_id: "abc123",
    session_id: "abc123:abc123:1234567890-deadbeef",
    replay_from_sequence: 42
  }
}
```

### Write Stdin
```typescript
{ stdin: Buffer.from("ls\n") }
```

### Resize Terminal
```typescript
{ resize: { width: 80, height: 24 } }
```

## Architecture Diagram

```
TypeScript Client
       ↓
Container.terminal()
       ↓
TTYSession.create()
       ↓
gRPC Session() stream
       ↓
ExecAttachable.Session()
       ↓
ExecSessionRegistry
       ↓
ExecInstance (TTY state, history, channels)
       ↓
PTY (pseudo-terminal)
       ↓
Container Process
```

## Testing

### Run Tests

**Backend**:
```bash
cd engine/session/exec && go test -v
```

**TypeScript SDK**:
```bash
cd sdk/typescript && npm test
```

### Example Test

```typescript
it("should handle reconnection", async () => {
  const session1 = await container.terminal()
  await session1.writeStdin("echo test\n")

  const sessionId = session1.sessionId
  const lastSeq = session1.lastSequence

  const session2 = await container.reconnectTerminal(sessionId, {
    replayFromSequence: lastSeq
  })

  assert.ok(session2.sessionId === sessionId)
})
```

## Troubleshooting Decision Tree

```
Problem: Session not found
├─ Check: Session ID valid?
│  ├─ No → Use correct session ID
│  └─ Yes → Check time since disconnect
│     ├─ > 5 min → Create new session
│     └─ < 5 min → Check process status
│        ├─ Exited > 5 min ago → Create new session
│        └─ Still running → Check registry
│           └─ Not in registry → Bug (report)

Problem: Backpressure error
├─ Check: Send rate
│  ├─ Too fast → Add rate limiting
│  └─ Normal → Check process
│     ├─ Not reading → Fix process logic
│     └─ Reading → Check buffer size
│        └─ Full (100) → Wait for drain

Problem: Output gaps
├─ Check: Disconnect duration
│  ├─ Long → Accept data loss
│  └─ Short → Check buffer size
│     └─ Wrapped → Reconnect more frequently

Problem: High memory
├─ Check: Session count
│  ├─ > 100 → Close unused
│  └─ < 100 → Check process state
│     ├─ Many long-running → Normal
│     └─ All short → Check cleanup
│        └─ Not running → Bug (report)
```

## API Reference

### Container Methods

**`terminal(options?)`**
- Creates new TTY session
- Returns: `Promise<TTYSession>`

**`reconnectTerminal(sessionId, options?)`**
- Reconnects to existing session
- Returns: `Promise<TTYSession>`

### TTYSession Properties

**`sessionId`**: `string | null`
- Server-assigned session identifier

**`lastSequence`**: `number`
- Last received sequence number

**`exitCode`**: `number | null`
- Process exit code (null if running)

**`ready`**: `boolean`
- Session initialization status

### TTYSession Methods

**`writeStdin(data)`**
- Sends data to stdin
- Args: `string | Buffer`
- Returns: `Promise<void>`

**`writeStdinEOF()`**
- Closes stdin stream
- Returns: `Promise<void>`

**`resize(width, height)`**
- Resizes terminal
- Args: `number, number`
- Returns: `Promise<void>`

**`close()`**
- Closes session
- Returns: `Promise<void>`

**`[Symbol.asyncIterator]()`**
- Async iteration over output
- Yields: `TTYOutput`

### TTYSession Static Methods

**`create(client, options)`**
- Creates new session
- Returns: `Promise<TTYSession>`

**`reconnect(client, options)`**
- Reconnects to session
- Returns: `Promise<TTYSession>`

## File Locations

| Component | Path |
|-----------|------|
| Protocol | `engine/session/exec/exec.proto` |
| Registry | `engine/session/exec/registry.go` |
| Handler | `engine/session/exec/exec.go` |
| TTY Types | `engine/session/exec/tty_types.go` |
| History | `engine/session/exec/output_history.go` |
| Buffer | `engine/session/exec/circular_buffer.go` |
| TS Session | `sdk/typescript/src/api/tty_session.ts` |
| TS Container | `sdk/typescript/src/api/container_grpc.ts` |
| TS Types | `sdk/typescript/src/grpc/types.ts` |
| Tests (Go) | `engine/session/exec/*_test.go` |
| Tests (TS) | `sdk/typescript/src/api/test/container-tty.spec.ts` |

## See Also

- **Full Documentation**: `core/docs/tty_sessions.md`
- **Streaming Errors**: `core/docs/STREAMING_ERROR_HANDLING_STRATEGY.md`
- **Examples**: `sdk/typescript/examples/container-tty-demo.ts`
