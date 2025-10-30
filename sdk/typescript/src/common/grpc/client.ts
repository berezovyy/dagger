import * as grpc from "@grpc/grpc-js"
import * as protoLoader from "@grpc/proto-loader"
import * as path from "path"
import { fileURLToPath } from "url"

// Import our manual type definitions
import type {
  ContainerStatusRequest,
  ContainerStatusResponse,
  ContainerControlRequest,
  ContainerControlResponse,
  ContainerSignalRequest,
  ContainerLifecycleRequest,
  ContainerLifecycleEvent,
  SessionRequest,
  SessionResponse,
} from "../../grpc/types.js"

/**
 * gRPC client for Exec service
 *
 * This interface defines the methods available on the Exec service client.
 * Each method corresponds to a gRPC RPC defined in exec.proto.
 */
export interface ExecClient {
  /**
   * Query the current status of a container (non-blocking)
   */
  ContainerStatus(
    request: ContainerStatusRequest,
    metadata: grpc.Metadata,
    callback: (
      error: grpc.ServiceError | null,
      response: ContainerStatusResponse,
    ) => void,
  ): grpc.ClientUnaryCall

  /**
   * Pause a running container
   */
  ContainerPause(
    request: ContainerControlRequest,
    metadata: grpc.Metadata,
    callback: (
      error: grpc.ServiceError | null,
      response: ContainerControlResponse,
    ) => void,
  ): grpc.ClientUnaryCall

  /**
   * Resume a paused container
   */
  ContainerResume(
    request: ContainerControlRequest,
    metadata: grpc.Metadata,
    callback: (
      error: grpc.ServiceError | null,
      response: ContainerControlResponse,
    ) => void,
  ): grpc.ClientUnaryCall

  /**
   * Send a signal to a container
   */
  ContainerSignal(
    request: ContainerSignalRequest,
    metadata: grpc.Metadata,
    callback: (
      error: grpc.ServiceError | null,
      response: ContainerControlResponse,
    ) => void,
  ): grpc.ClientUnaryCall

  /**
   * Subscribe to container lifecycle events
   */
  ContainerLifecycle(
    request: ContainerLifecycleRequest,
    metadata: grpc.Metadata,
  ): grpc.ClientReadableStream<ContainerLifecycleEvent>

  /**
   * Bidirectional streaming session for exec operations
   */
  Session(
    metadata: grpc.Metadata,
  ): grpc.ClientDuplexStream<SessionRequest, SessionResponse>
}

/**
 * Load proto definitions and create gRPC client
 *
 * Creates a gRPC client for the Exec service by loading the proto file
 * and connecting to the specified host and port.
 *
 * @param host - The host to connect to (e.g., "127.0.0.1")
 * @param port - The port to connect to
 * @param token - Session token for authentication (unused in client creation, used in metadata)
 * @returns An ExecClient instance
 */
export function createExecClient(
  host: string,
  port: number,
  token: string,
): ExecClient {
  // Load proto file - resolve path relative to compiled output
  const currentFilePath = fileURLToPath(import.meta.url)
  const PROTO_PATH = path.join(
    path.dirname(currentFilePath),
    "../../../proto/exec.proto",
  )

  const packageDefinition = protoLoader.loadSync(PROTO_PATH, {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true,
  })

  const protoDescriptor = grpc.loadPackageDefinition(packageDefinition) as any
  const execProto = protoDescriptor.dagger.exec

  // Create client with address
  const address = `${host}:${port}`
  const client = new execProto.Exec(
    address,
    grpc.credentials.createInsecure(), // Local connection - no TLS
  ) as ExecClient

  return client
}

/**
 * Create metadata with authentication
 *
 * Generates gRPC metadata with Basic Auth header using the session token.
 * The token is base64-encoded in the format `:token` (empty username).
 *
 * @param token - Session token for authentication
 * @returns gRPC Metadata with Authorization header
 */
export function createAuthMetadata(token: string): grpc.Metadata {
  const metadata = new grpc.Metadata()

  // Basic auth with session token (format: `:token`)
  const auth = Buffer.from(`:${token}`).toString("base64")
  metadata.add("authorization", `Basic ${auth}`)

  return metadata
}

/**
 * Helper to promisify unary gRPC calls
 *
 * Converts callback-based unary gRPC methods to Promise-based API
 * for easier use with async/await.
 *
 * @param method - The gRPC method to call
 * @param request - The request object
 * @param metadata - gRPC metadata (auth, etc.)
 * @returns Promise that resolves with the response
 */
export function callUnary<TRequest, TResponse>(
  method: (
    request: TRequest,
    metadata: grpc.Metadata,
    callback: (error: grpc.ServiceError | null, response: TResponse) => void,
  ) => grpc.ClientUnaryCall,
  request: TRequest,
  metadata: grpc.Metadata,
): Promise<TResponse> {
  return new Promise((resolve, reject) => {
    method(request, metadata, (error, response) => {
      if (error) {
        reject(error)
      } else {
        resolve(response)
      }
    })
  })
}
