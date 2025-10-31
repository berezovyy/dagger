import type { Client } from "./client.gen.js"
import type * as grpc from "@grpc/grpc-js"
import type {
  SessionStart,
  SessionRequest,
  SessionResponse,
  SessionReady,
  OutputChunk,
} from "../grpc/types.js"

/**
 * Options for creating a new TTY session
 */
export interface TTYSessionOptions {
  /** Container ID */
  containerId: string
  /** Execution ID */
  execId: string
  /** Command to execute (defaults to ["/bin/sh"]) */
  command?: string[]
  /** Environment variables */
  env?: Record<string, string>
  /** Working directory */
  workingDir?: string
  /** Client identifier (for tracking) */
  clientId?: string
}

/**
 * Options for reconnecting to an existing TTY session
 */
export interface TTYReconnectOptions {
  /** Session ID to reconnect to */
  sessionId: string
  /** Sequence number to replay from (0 = replay all available history) */
  replayFromSequence?: number
  /** Client identifier (for tracking) */
  clientId?: string
}

/**
 * Output chunk from TTY session
 */
export interface TTYOutput {
  /** Stdout data (if any) */
  stdout?: Buffer
  /** Stderr data (if any) */
  stderr?: Buffer
  /** Exit code (if process exited) */
  exitCode?: number
  /** Sequence number (for sequenced output) */
  sequence?: number
  /** True if this is replayed data */
  isReplay?: boolean
}

/**
 * TTYSession provides a high-level interface for managing TTY sessions
 * with support for stdin, resize, and reconnection.
 *
 * Example:
 * ```typescript
 * const session = await TTYSession.create(client, {
 *   containerId: "container-1",
 *   execId: "exec-1",
 *   command: ["/bin/bash"],
 * })
 *
 * // Send stdin
 * await session.writeStdin("ls -la\n")
 *
 * // Resize terminal
 * await session.resize(80, 24)
 *
 * // Read output
 * for await (const output of session) {
 *   if (output.stdout) process.stdout.write(output.stdout)
 * }
 *
 * // Reconnect later
 * const reconnected = await TTYSession.reconnect(client, {
 *   sessionId: session.sessionId,
 *   replayFromSequence: session.lastSequence,
 * })
 * ```
 */
export class TTYSession {
  private client: Client
  private stream: grpc.ClientDuplexStream<SessionRequest, SessionResponse> | null =
    null
  private _sessionId: string | null = null
  private _lastSequence: number = 0
  private _exitCode: number | null = null
  private _ready: boolean = false

  private constructor(client: Client) {
    this.client = client
  }

  /**
   * Create a new TTY session
   */
  static async create(
    client: Client,
    options: TTYSessionOptions,
  ): Promise<TTYSession> {
    const session = new TTYSession(client)

    // Get gRPC connection from client
    const connection = (client as any)._ctx._connection
    const grpcManager = connection.getGRPCManager()
    const grpcClient = grpcManager.getClient()
    const metadata = grpcManager.getMetadata()

    // Create bidirectional stream
    session.stream = grpcClient.Session(metadata)

    // Prepare Start message
    const start: SessionStart = {
      container_id: options.containerId,
      exec_id: options.execId,
      command: options.command || ["/bin/sh"],
      env: options.env,
      working_dir: options.workingDir,
      tty: true,
      client_id: options.clientId,
    }

    // Send Start message
    if (!session.stream) {
      throw new Error("Failed to create stream")
    }
    session.stream.write({ start })

    // Wait for Ready message
    const ready = await session.waitForReady()
    session._sessionId = ready.session_id || null

    return session
  }

  /**
   * Reconnect to an existing TTY session
   */
  static async reconnect(
    client: Client,
    options: TTYReconnectOptions,
  ): Promise<TTYSession> {
    const session = new TTYSession(client)

    // Get gRPC connection from client
    const connection = (client as any)._ctx._connection
    const grpcManager = connection.getGRPCManager()
    const grpcClient = grpcManager.getClient()
    const metadata = grpcManager.getMetadata()

    // Create bidirectional stream
    session.stream = grpcClient.Session(metadata)

    // Prepare Start message for reconnection
    const start: SessionStart = {
      container_id: "", // Will be validated by server
      exec_id: "",
      command: [],
      tty: true,
      session_id: options.sessionId,
      replay_from_sequence: options.replayFromSequence || 0,
      client_id: options.clientId,
    }

    // Send Start message
    if (!session.stream) {
      throw new Error("Failed to create stream")
    }
    session.stream.write({ start })

    // Wait for Ready message
    const ready = await session.waitForReady()
    session._sessionId = ready.session_id || options.sessionId

    return session
  }

  /**
   * Write data to stdin
   */
  async writeStdin(data: string | Buffer): Promise<void> {
    if (!this._ready) {
      throw new Error("Session not ready")
    }

    if (!this.stream) {
      throw new Error("Stream not initialized")
    }

    const buffer = typeof data === "string" ? Buffer.from(data) : data

    const request: SessionRequest = {
      stdin: buffer,
    }

    this.stream.write(request)
  }

  /**
   * Send EOF to stdin (closes stdin pipe)
   */
  async writeStdinEOF(): Promise<void> {
    if (!this._ready) {
      throw new Error("Session not ready")
    }

    if (!this.stream) {
      throw new Error("Stream not initialized")
    }

    const request: SessionRequest = {
      stdin: Buffer.alloc(0), // Empty buffer signals EOF
    }

    this.stream.write(request)
  }

  /**
   * Resize the terminal
   */
  async resize(width: number, height: number): Promise<void> {
    if (!this._ready) {
      throw new Error("Session not ready")
    }

    if (!this.stream) {
      throw new Error("Stream not initialized")
    }

    const request: SessionRequest = {
      resize: {
        width,
        height,
      },
    }

    this.stream.write(request)
  }

  /**
   * Get the session ID (for reconnection)
   */
  get sessionId(): string | null {
    return this._sessionId
  }

  /**
   * Get the last received sequence number
   */
  get lastSequence(): number {
    return this._lastSequence
  }

  /**
   * Get the exit code (null if not exited)
   */
  get exitCode(): number | null {
    return this._exitCode
  }

  /**
   * Check if session is ready
   */
  get ready(): boolean {
    return this._ready
  }

  /**
   * Wait for Ready message from server
   */
  private async waitForReady(): Promise<SessionReady> {
    if (!this.stream) {
      throw new Error("Stream not initialized")
    }

    return new Promise((resolve, reject) => {
      const handleData = (response: SessionResponse) => {
        if (response.ready) {
          this._ready = true
          this.stream?.removeListener("data", handleData)
          this.stream?.removeListener("error", handleError)
          resolve(response.ready)
        }
      }

      const handleError = (error: Error) => {
        this.stream?.removeListener("data", handleData)
        this.stream?.removeListener("error", handleError)
        reject(error)
      }

      if (!this.stream) {
        reject(new Error("Stream not initialized"))
        return
      }

      this.stream.on("data", handleData)
      this.stream.on("error", handleError)
    })
  }

  /**
   * Iterate over output chunks
   */
  async *[Symbol.asyncIterator](): AsyncIterableIterator<TTYOutput> {
    if (!this.stream) {
      throw new Error("Stream not initialized")
    }

    for await (const response of this.stream) {
      // Handle different response types
      if (response.stdout || response.stderr) {
        yield {
          stdout: response.stdout ? Buffer.from(response.stdout) : undefined,
          stderr: response.stderr ? Buffer.from(response.stderr) : undefined,
        }
      }

      if (response.exit !== undefined) {
        this._exitCode = response.exit
        yield { exitCode: response.exit }
        break
      }

      if (response.output_chunk) {
        const chunk = response.output_chunk
        this._lastSequence = chunk.sequence
        yield {
          stdout: chunk.stdout ? Buffer.from(chunk.stdout) : undefined,
          stderr: chunk.stderr ? Buffer.from(chunk.stderr) : undefined,
          sequence: chunk.sequence,
          isReplay: chunk.is_replay,
        }
      }
    }
  }

  /**
   * Close the session
   */
  async close(): Promise<void> {
    if (this.stream) {
      this.stream.end()
      this.stream = null
    }
  }
}
