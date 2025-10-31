import type {
  ContainerStatusResponse,
  ContainerLifecycleEvent,
} from "../grpc/types"
import type { TTYSession } from "./tty_session"

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

    /**
     * Create an interactive TTY session for the container
     *
     * Opens a bidirectional streaming connection that allows you to:
     * - Send stdin input to the container
     * - Receive stdout/stderr output in real-time
     * - Resize the terminal dynamically
     * - Reconnect to the session if disconnected
     *
     * This method creates a new TTY session with a shell (or custom command) running inside
     * the container. The session persists on the server, allowing reconnection in case of
     * network interruption.
     *
     * @param options - Configuration for the TTY session
     * @param options.command - Command to execute (defaults to ["/bin/sh"])
     * @param options.env - Environment variables for the session
     * @param options.workingDir - Working directory for the command
     * @param options.clientId - Optional client identifier for tracking
     * @returns TTYSession instance for interacting with the terminal
     *
     * @example
     * ```typescript
     * const container = dag.container().from("ubuntu:latest")
     * const session = await container.terminal({ command: ["/bin/bash"] })
     *
     * // Write commands to stdin
     * await session.writeStdin("ls -la\n")
     * await session.writeStdin("echo hello\n")
     *
     * // Read output
     * for await (const output of session) {
     *   if (output.stdout) {
     *     process.stdout.write(output.stdout)
     *   }
     *   if (output.exitCode !== undefined) {
     *     console.log(`Exited with code: ${output.exitCode}`)
     *     break
     *   }
     * }
     * ```
     */
    terminal(options?: {
      command?: string[]
      env?: Record<string, string>
      workingDir?: string
      clientId?: string
    }): Promise<TTYSession>

    /**
     * Reconnect to an existing TTY session
     *
     * Reconnects to a TTY session that was previously created with terminal().
     * The server will replay any output that was generated while disconnected,
     * starting from the specified sequence number.
     *
     * This enables reliable terminal sessions that can survive network interruptions,
     * client restarts, or intentional disconnections.
     *
     * @param sessionId - The session ID to reconnect to (obtained from previous session)
     * @param options - Reconnection options
     * @param options.replayFromSequence - Sequence number to start replay from (0 = replay all available)
     * @param options.clientId - Optional client identifier for tracking
     * @returns TTYSession instance reconnected to the existing session
     *
     * @example
     * ```typescript
     * // Create initial session
     * const session1 = await container.terminal()
     * const sessionId = session1.sessionId
     *
     * // ... later, after disconnect ...
     *
     * // Reconnect to the same session
     * const session2 = await container.reconnectTerminal(sessionId, {
     *   replayFromSequence: session1.lastSequence
     * })
     *
     * // Continue where you left off
     * await session2.writeStdin("pwd\n")
     * ```
     */
    reconnectTerminal(
      sessionId: string,
      options?: {
        replayFromSequence?: number
        clientId?: string
      },
    ): Promise<TTYSession>
  }
}
