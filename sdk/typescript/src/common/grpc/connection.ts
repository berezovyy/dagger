import type * as grpc from "@grpc/grpc-js"

import type { ExecClient } from "./client.js"
import { createExecClient, createAuthMetadata } from "./client.js"

/**
 * Manages gRPC connection lifecycle
 *
 * This class handles lazy initialization of the gRPC client and provides
 * methods to get the client and authentication metadata. The client is
 * created only when first accessed, minimizing overhead when gRPC features
 * are not used.
 *
 * @example
 * ```typescript
 * const manager = new GRPCConnectionManager("127.0.0.1", 12345, "session-token")
 * const client = manager.getClient()
 * const metadata = manager.getMetadata()
 * // ... use client ...
 * manager.close()
 * ```
 */
export class GRPCConnectionManager {
  private client: ExecClient | null = null
  private metadata: grpc.Metadata | null = null
  private host: string
  private port: number
  private token: string

  /**
   * Create a new GRPCConnectionManager
   *
   * @param host - The host to connect to (e.g., "127.0.0.1")
   * @param port - The port to connect to
   * @param token - Session token for authentication
   */
  constructor(host: string, port: number, token: string) {
    this.host = host
    this.port = port
    this.token = token
  }

  /**
   * Get or create gRPC client (lazy initialization)
   *
   * The client is created on first access and reused for subsequent calls.
   * This allows for efficient connection sharing across multiple operations.
   *
   * @returns ExecClient instance
   */
  getClient(): ExecClient {
    if (!this.client) {
      this.client = createExecClient(this.host, this.port, this.token)
    }
    return this.client
  }

  /**
   * Get authentication metadata
   *
   * Returns metadata with Basic Auth header for authenticating gRPC calls.
   * The metadata is created once and reused for efficiency.
   *
   * @returns gRPC Metadata with Authorization header
   */
  getMetadata(): grpc.Metadata {
    if (!this.metadata) {
      this.metadata = createAuthMetadata(this.token)
    }
    return this.metadata
  }

  /**
   * Close the gRPC connection
   *
   * Closes the gRPC client and cleans up resources. Should be called
   * when the connection is no longer needed (e.g., when the session ends).
   */
  close(): void {
    if (this.client) {
      // Close the client
      const anyClient = this.client as any
      if (anyClient.close) {
        anyClient.close()
      }
      this.client = null
      this.metadata = null
    }
  }
}
