import type {
  ContainerStatusResponse,
  ContainerLifecycleEvent,
} from "../grpc/types"

/**
 * Module augmentation to extend the generated Container class
 * with gRPC-based streaming and control methods.
 *
 * These methods are added to Container.prototype at runtime by importing
 * container_grpc.ts, which provides implementations for real-time operations
 * not available through GraphQL.
 */
declare module "./client.gen" {
  interface Container {
    /**
     * Query container status (non-blocking)
     *
     * Returns the current status of the container without blocking on execution.
     * Optionally includes resource usage statistics like CPU, memory, and I/O.
     *
     * @param options.includeResourceUsage - Whether to include resource usage statistics
     * @returns Container status information including state, timestamps, and optional resource usage
     */
    status(options?: {
      includeResourceUsage?: boolean
    }): Promise<ContainerStatusResponse>

    /**
     * Pause container execution
     *
     * Pauses a running container, freezing all processes inside it.
     * The container can be resumed later with resume().
     *
     * @throws Error if the container is not running or pause operation fails
     */
    pause(): Promise<void>

    /**
     * Resume paused container
     *
     * Resumes a container that was previously paused with pause().
     * The container's processes will continue from where they were frozen.
     *
     * @throws Error if the container is not paused or resume operation fails
     */
    resume(): Promise<void>

    /**
     * Send signal to container
     *
     * Sends a Unix signal to the container's main process.
     * Common signals include:
     * - SIGTERM: Request graceful termination
     * - SIGKILL: Force immediate termination
     * - SIGINT: Interrupt (Ctrl+C)
     * - SIGHUP: Hangup
     * - SIGUSR1, SIGUSR2: User-defined signals
     *
     * @param signal - Signal name (e.g., "SIGTERM", "SIGKILL", "SIGINT")
     * @throws Error if the signal cannot be sent or is invalid
     */
    signal(signal: string): Promise<void>

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
     * const container = dag.container().from("alpine").withExec(["sh", "-c", "echo hello; sleep 1; echo world"])
     *
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
    streamLogs(): AsyncIterable<{ stdout?: Buffer; stderr?: Buffer }>

    /**
     * Subscribe to container lifecycle events
     *
     * Returns an async iterable that yields lifecycle events as they occur.
     * Event types include:
     * - registered: Container registered in the engine
     * - started: Container execution started
     * - paused: Container paused
     * - resumed: Container resumed from pause
     * - exited: Container finished execution (includes exit code)
     * - unregistered: Container removed from engine
     *
     * @returns AsyncIterable that yields lifecycle events
     *
     * @example
     * ```typescript
     * const container = dag.container().from("alpine").withExec(["echo", "hello"])
     *
     * for await (const event of container.subscribeLifecycle()) {
     *   console.log(`[${event.event_type}] ${event.message}`)
     *   if (event.event_type === 'exited') {
     *     console.log(`Exit code: ${event.exit_code}`)
     *     break
     *   }
     * }
     * ```
     */
    subscribeLifecycle(): AsyncIterable<ContainerLifecycleEvent>
  }
}
