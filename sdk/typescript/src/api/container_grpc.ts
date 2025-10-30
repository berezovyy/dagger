import { Container } from "./client.gen.js"
import { callUnary } from "../common/grpc/client.js"
import type {
  ContainerStatusRequest,
  ContainerStatusResponse,
  ContainerControlRequest,
  ContainerControlResponse,
  ContainerSignalRequest,
  ContainerLifecycleRequest,
  ContainerLifecycleEvent,
} from "../grpc/types.js"

/**
 * Query container status (non-blocking)
 *
 * Returns the current status of the container without blocking on execution.
 * Optionally includes resource usage statistics.
 *
 * @param options.includeResourceUsage - Whether to include CPU, memory, and I/O statistics
 * @returns Container status information
 */
export async function containerStatus(
  this: Container,
  options?: { includeResourceUsage?: boolean },
): Promise<ContainerStatusResponse> {
  // Get container ID (materialize if needed)
  const containerId = await this.id()

  // Get gRPC client from connection
  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  // Make request
  const request: ContainerStatusRequest = {
    container_id: containerId,
    include_resource_usage: options?.includeResourceUsage ?? false,
  }

  return callUnary(client.ContainerStatus.bind(client), request, metadata)
}

/**
 * Pause container execution
 *
 * Pauses a running container, freezing all processes inside it.
 * The container can be resumed later with resume().
 *
 * @throws Error if the container is not running or pause operation fails
 */
export async function containerPause(this: Container): Promise<void> {
  const containerId = await this.id()

  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  const request: ContainerControlRequest = {
    container_id: containerId,
  }

  const response = await callUnary<
    ContainerControlRequest,
    ContainerControlResponse
  >(client.ContainerPause.bind(client), request, metadata)

  if (!response.success) {
    throw new Error(`Failed to pause container: ${response.message}`)
  }
}

/**
 * Resume paused container
 *
 * Resumes a container that was previously paused with pause().
 * The container's processes will continue from where they were frozen.
 *
 * @throws Error if the container is not paused or resume operation fails
 */
export async function containerResume(this: Container): Promise<void> {
  const containerId = await this.id()

  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  const request: ContainerControlRequest = {
    container_id: containerId,
  }

  const response = await callUnary<
    ContainerControlRequest,
    ContainerControlResponse
  >(client.ContainerResume.bind(client), request, metadata)

  if (!response.success) {
    throw new Error(`Failed to resume container: ${response.message}`)
  }
}

/**
 * Send signal to container
 *
 * Sends a Unix signal to the container's main process.
 * Common signals: SIGTERM, SIGKILL, SIGINT, SIGHUP, SIGUSR1, SIGUSR2
 *
 * @param signal - Signal name (e.g., "SIGTERM", "SIGKILL", "SIGINT")
 * @throws Error if the signal cannot be sent or is invalid
 */
export async function containerSignal(
  this: Container,
  signal: string,
): Promise<void> {
  const containerId = await this.id()

  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  const request: ContainerSignalRequest = {
    container_id: containerId,
    signal: signal,
  }

  const response = await callUnary<
    ContainerSignalRequest,
    ContainerControlResponse
  >(client.ContainerSignal.bind(client), request, metadata)

  if (!response.success) {
    throw new Error(`Failed to send signal: ${response.message}`)
  }
}

/**
 * Stream container logs in real-time
 *
 * Returns an async iterable that yields stdout and stderr output as it becomes available.
 * The stream continues until the container exits or the iteration is stopped.
 *
 * @returns AsyncIterable that yields log chunks with stdout and/or stderr buffers
 *
 * @example
 * ```typescript
 * for await (const chunk of container.streamLogs()) {
 *   if (chunk.stdout) {
 *     process.stdout.write(chunk.stdout)
 *   }
 *   if (chunk.stderr) {
 *     process.stderr.write(chunk.stderr)
 *   }
 * }
 * ```
 */
export async function* containerStreamLogs(
  this: Container,
): AsyncIterable<{ stdout?: Buffer; stderr?: Buffer }> {
  const containerId = await this.id()

  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  // Start streaming session
  const stream = client.Session(metadata)

  // Send Start message to initiate log streaming
  stream.write({
    start: {
      container_id: containerId,
      command: [], // Empty command means stream logs only
    },
  })

  // Yield logs as they arrive
  for await (const response of stream) {
    if (response.stdout || response.stderr) {
      yield {
        stdout: response.stdout ? Buffer.from(response.stdout) : undefined,
        stderr: response.stderr ? Buffer.from(response.stderr) : undefined,
      }
    }

    if (response.exit !== undefined) {
      break // Container finished
    }
  }
}

/**
 * Subscribe to container lifecycle events
 *
 * Returns an async iterable that yields lifecycle events as they occur.
 * Events include: registered, started, paused, resumed, exited, unregistered
 *
 * @returns AsyncIterable that yields lifecycle events
 *
 * @example
 * ```typescript
 * for await (const event of container.subscribeLifecycle()) {
 *   console.log(`${event.event_type}: ${event.message}`)
 *   if (event.event_type === 'exited') {
 *     console.log(`Exit code: ${event.exit_code}`)
 *   }
 * }
 * ```
 */
export async function* containerSubscribeLifecycle(
  this: Container,
): AsyncIterable<ContainerLifecycleEvent> {
  const containerId = await this.id()

  const connection = (this as any)._ctx._connection
  const grpcManager = connection.getGRPCManager()
  const client = grpcManager.getClient()
  const metadata = grpcManager.getMetadata()

  const request: ContainerLifecycleRequest = {
    container_id: containerId,
  }

  const stream = client.ContainerLifecycle(request, metadata)

  // Yield events as they arrive
  for await (const event of stream) {
    yield event
  }
}

// Extend Container prototype with gRPC methods
;(Container.prototype as any).status = containerStatus
;(Container.prototype as any).pause = containerPause
;(Container.prototype as any).resume = containerResume
;(Container.prototype as any).signal = containerSignal
;(Container.prototype as any).streamLogs = containerStreamLogs
;(Container.prototype as any).subscribeLifecycle = containerSubscribeLifecycle
