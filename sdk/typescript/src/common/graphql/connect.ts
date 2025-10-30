import { GraphQLClient } from "graphql-request"

import { ConnectOpts } from "../../connectOpts.js"
import { createGQLClient } from "./client.js"
import { globalConnection } from "./connection.js"

/**
 * Session information returned from the engine
 */
export interface SessionInfo {
  host: string
  port: number
  token: string
}

/**
 * Execute the callback with a GraphQL client connected to the Dagger engine.
 * It automatically provisions the engine if needed and sets up the gRPC connection.
 */
export async function withGQLClient<T>(
  connectOpts: ConnectOpts,
  cb: (gqlClient: GraphQLClient) => Promise<T>,
): Promise<T> {
  if (process.env["DAGGER_SESSION_PORT"]) {
    const port = process.env["DAGGER_SESSION_PORT"]
    if (!process.env["DAGGER_SESSION_TOKEN"]) {
      throw new Error(
        "DAGGER_SESSION_TOKEN must be set if DAGGER_SESSION_PORT is set",
      )
    }

    const token = process.env["DAGGER_SESSION_TOKEN"]
    const portNum = Number(port)

    // Set session info for gRPC connection
    globalConnection.setSessionInfo("127.0.0.1", portNum, token)

    return await cb(createGQLClient(portNum, token))
  }

  try {
    const provisioning = await import("../../provisioning/index.js")

    return await provisioning.withEngineSession(connectOpts, cb)
  } catch (e) {
    throw new Error(
      `failed to execute function with automatic provisioning: ${e}`,
    )
  }
}
