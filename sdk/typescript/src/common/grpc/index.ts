/**
 * gRPC client infrastructure for TypeScript SDK
 *
 * This module provides gRPC client factory and connection management
 * for streaming and control operations that are difficult to implement
 * efficiently over GraphQL.
 *
 * @module grpc
 */

export { createExecClient, createAuthMetadata, callUnary } from "./client.js"
export type { ExecClient } from "./client.js"
export { GRPCConnectionManager } from "./connection.js"
