# Container Exec Service Usage Guide

User-facing guide for the gRPC-based container execution streaming service.

## Overview

The Container Exec Service provides real-time streaming of container execution output via gRPC. This enables clients to receive stdout, stderr, and exit codes as containers run, rather than waiting for completion.

## Features

- **Real-Time Streaming**: Receive stdout and stderr as the container produces output
- **Exit Code Delivery**: Get exit codes immediately when containers finish
- **Lifecycle Events**: Subscribe to container state changes (started, paused, exited, etc.)
- **Status Queries**: Non-blocking queries for current container status and resource usage
- **Multi-Client Support**: Multiple clients can attach to the same execution and receive identical streams

## TypeScript SDK Usage

### Installation

The gRPC streaming functionality is available in the Dagger TypeScript SDK v0.x and later.

```bash
npm install @dagger.io/dagger
```

### Basic Example: Stream Logs

Stream container output in real-time:

```typescript
import { connect } from "@dagger.io/dagger"

await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sh", "-c", "echo 'Starting...'; sleep 2; echo 'Done!'"])

  // Stream logs in real-time
  for await (const chunk of container.streamLogs()) {
    if (chunk.stdout) {
      process.stdout.write(chunk.stdout.toString())
    }
    if (chunk.stderr) {
      process.stderr.write(chunk.stderr.toString())
    }
    if (chunk.exitCode !== undefined) {
      console.log(`Exit code: ${chunk.exitCode}`)
    }
  }
})
```

### Query Container Status

Get current container status and resource usage:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sleep", "10"])

  // Query status (non-blocking)
  const status = await container.status({
    includeResourceUsage: true,
  })

  console.log(`Status: ${status.status}`)
  console.log(`Container ID: ${status.container_id}`)

  if (status.resource_usage) {
    console.log(`CPU: ${status.resource_usage.cpu_percent.toFixed(2)}%`)
    console.log(`Memory: ${(status.resource_usage.memory_bytes / 1024 / 1024).toFixed(2)} MB`)
  }
})
```

### Subscribe to Lifecycle Events

Receive notifications when container state changes:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["echo", "lifecycle test"])

  // Subscribe to lifecycle events
  for await (const event of container.subscribeLifecycle()) {
    console.log(`[${event.event_type}] ${event.message}`)
    console.log(`Status: ${event.status}`)

    if (event.event_type === "exited") {
      console.log(`Exit code: ${event.exit_code}`)
      break
    }
  }
})
```

### Pause and Resume Containers

Control container execution:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sleep", "30"])

  // Start container (non-blocking)
  const execPromise = container.sync()

  // Pause after 2 seconds
  await new Promise(resolve => setTimeout(resolve, 2000))
  await container.pause()
  console.log("Container paused")

  // Resume after 3 seconds
  await new Promise(resolve => setTimeout(resolve, 3000))
  await container.resume()
  console.log("Container resumed")

  // Wait for completion
  await execPromise
})
```

### Send Signals

Send Unix signals to running containers:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sleep", "60"])

  const execPromise = container.sync()

  // Send SIGTERM after 5 seconds
  await new Promise(resolve => setTimeout(resolve, 5000))
  await container.signal("SIGTERM")

  try {
    await execPromise
  } catch (e) {
    console.log(`Container terminated: ${e.message}`)
  }
})
```

## Advanced Usage

### Multiple Clients

Multiple clients can attach to the same container execution:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sh", "-c", "for i in 1 2 3 4 5; do echo $i; sleep 1; done"])

  // Client 1: Stream logs
  const stream1 = (async () => {
    console.log("Client 1 starting...")
    for await (const chunk of container.streamLogs()) {
      if (chunk.stdout) {
        console.log(`[Client 1] ${chunk.stdout.toString().trim()}`)
      }
    }
  })()

  // Client 2: Stream logs (same output)
  const stream2 = (async () => {
    console.log("Client 2 starting...")
    for await (const chunk of container.streamLogs()) {
      if (chunk.stdout) {
        console.log(`[Client 2] ${chunk.stdout.toString().trim()}`)
      }
    }
  })()

  await Promise.all([stream1, stream2])
})
```

### Combining Streams and Status

Monitor both output and resource usage:

```typescript
await connect(async (client) => {
  const container = client
    .container()
    .from("alpine:latest")
    .withExec(["sh", "-c", "for i in $(seq 1 100); do echo $i; sleep 0.1; done"])

  // Start streaming
  const streamPromise = (async () => {
    for await (const chunk of container.streamLogs()) {
      if (chunk.stdout) {
        process.stdout.write(chunk.stdout.toString())
      }
    }
  })()

  // Poll status periodically
  const statusPromise = (async () => {
    while (true) {
      await new Promise(resolve => setTimeout(resolve, 1000))
      try {
        const status = await container.status({ includeResourceUsage: true })
        if (status.resource_usage) {
          console.log(`\n[Status] CPU: ${status.resource_usage.cpu_percent.toFixed(2)}%, Memory: ${(status.resource_usage.memory_bytes / 1024 / 1024).toFixed(2)} MB`)
        }
        if (status.status === "exited") {
          break
        }
      } catch (e) {
        break
      }
    }
  })()

  await Promise.all([streamPromise, statusPromise])
})
```

## Troubleshooting

### Connection Issues

**Problem**: Cannot connect to exec service

**Solution**: Ensure the Dagger engine is running and the session is properly initialized:

```typescript
// Session info is automatically configured during connect()
await connect(async (client) => {
  // gRPC connection is established automatically
  const container = client.container().from("alpine:latest")
  // ...
}, { LogOutput: process.stderr })
```

### Stream Not Receiving Data

**Problem**: `streamLogs()` doesn't receive any output

**Solutions**:
1. Ensure the container command produces output to stdout/stderr
2. Check that the container actually starts (use `status()` to verify)
3. Verify the container doesn't exit immediately before streaming starts

```typescript
// Good: Command produces output
container.withExec(["echo", "hello"])

// Bad: Command might not produce output
container.withExec(["true"])
```

### Exit Code Not Received

**Problem**: Stream ends without exit code

**Solution**: Exit codes are sent only when containers complete. For long-running containers, you may need to send a signal or wait for natural completion:

```typescript
const container = client
  .container()
  .from("alpine:latest")
  .withExec(["sleep", "infinity"])

// Option 1: Send signal after some time
setTimeout(() => container.signal("SIGTERM"), 5000)

// Option 2: Use timeout
const timeout = new Promise((_, reject) =>
  setTimeout(() => reject(new Error("Timeout")), 10000)
)
await Promise.race([container.sync(), timeout])
```

### Memory Usage

**Problem**: High memory usage when streaming large outputs

**Solution**: The streaming design is non-blocking and buffered. If memory is a concern:

1. Process chunks immediately instead of accumulating:

```typescript
// Good: Process immediately
for await (const chunk of container.streamLogs()) {
  processChunk(chunk) // Process and discard
}

// Bad: Accumulate in memory
const allOutput = []
for await (const chunk of container.streamLogs()) {
  allOutput.push(chunk) // Memory grows
}
```

2. Use smaller buffer sizes if available in future SDK versions

### gRPC Errors

**Problem**: gRPC errors like "UNAVAILABLE" or "DEADLINE_EXCEEDED"

**Solutions**:
1. Check network connectivity to Dagger engine
2. Verify session token is valid
3. Ensure engine hasn't been shut down
4. Check for firewall or network proxy issues

```typescript
try {
  for await (const chunk of container.streamLogs()) {
    // ...
  }
} catch (error) {
  if (error.code === "UNAVAILABLE") {
    console.error("Engine unavailable - is it running?")
  } else if (error.code === "DEADLINE_EXCEEDED") {
    console.error("Operation timed out")
  } else {
    console.error(`gRPC error: ${error.message}`)
  }
}
```

## API Reference

### StreamLogs Options

```typescript
interface StreamLogsChunk {
  stdout?: Buffer    // Stdout data chunk
  stderr?: Buffer    // Stderr data chunk
  exitCode?: number  // Exit code (sent once when container exits)
}

async *streamLogs(): AsyncIterableIterator<StreamLogsChunk>
```

### Status Options

```typescript
interface StatusOptions {
  includeResourceUsage?: boolean  // Include CPU, memory metrics
}

interface ContainerStatus {
  container_id: string
  status: string              // "pending" | "running" | "exited"
  started_at: number         // Unix timestamp
  finished_at?: number       // Unix timestamp (if exited)
  exit_code?: number         // Exit code (if exited)
  resource_usage?: {
    cpu_percent: number      // CPU usage percentage
    memory_bytes: number     // Memory usage in bytes
    memory_limit: number     // Memory limit in bytes
    io_read_bytes: number    // I/O read bytes
    io_write_bytes: number   // I/O write bytes
  }
}

async status(options?: StatusOptions): Promise<ContainerStatus>
```

### Lifecycle Events

```typescript
interface LifecycleEvent {
  container_id: string
  event_type: string        // "started" | "paused" | "resumed" | "exited"
  status: string           // Current container status
  timestamp: number        // Unix timestamp
  exit_code?: number       // Exit code (for "exited" events)
  message: string          // Human-readable message
}

async *subscribeLifecycle(): AsyncIterableIterator<LifecycleEvent>
```

### Control Operations

```typescript
async pause(): Promise<void>
async resume(): Promise<void>
async signal(signal: string): Promise<void>  // "SIGTERM", "SIGKILL", etc.
```

## Performance Considerations

### Buffering

The service uses buffered channels (100 items) for stdout/stderr. If your container produces output faster than clients can consume it, some output may be dropped with warnings logged.

### Multiple Clients

Each client receives independent streams but shares the underlying exec instance. This is memory-efficient for multiple monitoring clients.

### Resource Cleanup

Exited containers are retained for 5 minutes to allow late-arriving clients to retrieve exit codes. After this retention period, they are automatically cleaned up.

## Best Practices

1. **Always handle errors**: Network issues can cause streams to fail
2. **Process chunks immediately**: Don't accumulate large outputs in memory
3. **Use status queries sparingly**: They're non-blocking but frequent polling adds overhead
4. **Subscribe to lifecycle events**: More efficient than polling status
5. **Clean up**: Ensure container executions complete or are terminated

## Examples

Complete examples are available in the TypeScript SDK repository:

- `/home/berezovyy/Projects/dagger/sdk/typescript/examples/container-grpc-demo.ts`

Run examples:

```bash
cd sdk/typescript/examples
npm install
npm run container-grpc-demo
```

## Further Reading

- Technical documentation: `/home/berezovyy/Projects/dagger/engine/session/exec/README.md`
- Protocol definition: `/home/berezovyy/Projects/dagger/engine/session/exec/exec.proto`
- Implementation summary: `/home/berezovyy/Projects/dagger/IMPLEMENTATION_SUMMARY.md`
