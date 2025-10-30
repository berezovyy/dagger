import { GraphQLClient } from "graphql-request"

import { GRPCConnectionManager } from "../grpc/connection.js"

/**
 * Wraps the GraphQL client to allow lazy initialization and setting
 * the GQL client of the global Dagger client instance (`dag`).
 *
 * Also manages the gRPC connection for streaming and control operations.
 */
export class Connection {
  private _grpcManager?: GRPCConnectionManager

  constructor(
    private _gqlClient?: GraphQLClient,
    private _sessionInfo?: { host: string; port: number; token: string },
  ) {}

  resetClient() {
    this._gqlClient = undefined
    if (this._grpcManager) {
      this._grpcManager.close()
      this._grpcManager = undefined
    }
    this._sessionInfo = undefined
  }

  setGQLClient(gqlClient: GraphQLClient) {
    this._gqlClient = gqlClient
  }

  getGQLClient(): GraphQLClient {
    if (!this._gqlClient) {
      throw new Error("GraphQL client is not set")
    }

    return this._gqlClient
  }

  /**
   * Set session information for gRPC connection
   *
   * This must be called after session provisioning to enable gRPC features.
   *
   * @param host - The host to connect to (e.g., "127.0.0.1")
   * @param port - The port to connect to
   * @param token - Session token for authentication
   */
  setSessionInfo(host: string, port: number, token: string) {
    this._sessionInfo = { host, port, token }
  }

  /**
   * Get gRPC connection manager (lazy initialization)
   *
   * Creates a gRPC connection manager on first access using the session
   * information. The manager handles connection lifecycle and authentication.
   *
   * If this connection doesn't have session info, it will try to get it from
   * the global connection (for cases where connect() API is used).
   *
   * @returns GRPCConnectionManager instance
   * @throws Error if session info has not been set
   */
  getGRPCManager(): GRPCConnectionManager {
    if (!this._grpcManager) {
      // Try to get session info from this instance first
      let sessionInfo = this._sessionInfo

      // If not set, try to get from global connection (for connect() API)
      if (!sessionInfo && this !== globalConnection) {
        sessionInfo = globalConnection._sessionInfo
      }

      if (!sessionInfo) {
        throw new Error(
          "Session info not set. Cannot create gRPC connection manager.",
        )
      }

      this._grpcManager = new GRPCConnectionManager(
        sessionInfo.host,
        sessionInfo.port,
        sessionInfo.token,
      )
    }
    return this._grpcManager
  }

  /**
   * Get session token for authentication
   *
   * @returns Session token
   * @throws Error if session info has not been set
   */
  getSessionToken(): string {
    // Try to get session info from this instance first
    let sessionInfo = this._sessionInfo

    // If not set, try to get from global connection (for connect() API)
    if (!sessionInfo && this !== globalConnection) {
      sessionInfo = globalConnection._sessionInfo
    }

    if (!sessionInfo) {
      throw new Error("Session info not set")
    }
    return sessionInfo.token
  }
}

export const globalConnection = new Connection()
