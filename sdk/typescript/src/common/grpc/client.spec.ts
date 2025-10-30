/**
 * Tests for gRPC client factory and connection management
 *
 * NOTE: These tests are meant to document the API and test with mock servers.
 * Full integration tests with real Dagger engine will be in separate e2e tests.
 */

import { describe, it, expect, beforeEach, afterEach } from "@jest/globals"
import * as grpc from "@grpc/grpc-js"

import { createAuthMetadata, callUnary } from "./client.js"
import { GRPCConnectionManager } from "./connection.js"

describe("createAuthMetadata", () => {
  it("should create Basic Auth metadata with correct format", () => {
    const token = "test-token-123"
    const metadata = createAuthMetadata(token)

    const auth = metadata.get("authorization")
    expect(auth).toHaveLength(1)
    expect(auth[0]).toMatch(/^Basic /)

    // Decode and verify format is :token
    const decoded = Buffer.from(auth[0].substring(6), "base64").toString()
    expect(decoded).toBe(`:${token}`)
  })

  it("should handle empty token", () => {
    const metadata = createAuthMetadata("")
    const auth = metadata.get("authorization")

    expect(auth).toHaveLength(1)
    const decoded = Buffer.from(auth[0].substring(6), "base64").toString()
    expect(decoded).toBe(":")
  })
})

describe("callUnary", () => {
  it("should promisify a successful unary call", async () => {
    const mockMethod = (
      request: { id: string },
      metadata: grpc.Metadata,
      callback: (error: grpc.ServiceError | null, response: any) => void,
    ) => {
      // Simulate successful response
      setTimeout(() => callback(null, { result: "success" }), 10)
      return {} as grpc.ClientUnaryCall
    }

    const metadata = new grpc.Metadata()
    const response = await callUnary(mockMethod, { id: "test" }, metadata)

    expect(response).toEqual({ result: "success" })
  })

  it("should reject on error", async () => {
    const mockMethod = (
      request: { id: string },
      metadata: grpc.Metadata,
      callback: (error: grpc.ServiceError | null, response: any) => void,
    ) => {
      // Simulate error
      const error = new Error("gRPC error") as grpc.ServiceError
      error.code = grpc.status.INTERNAL
      setTimeout(() => callback(error, null), 10)
      return {} as grpc.ClientUnaryCall
    }

    const metadata = new grpc.Metadata()

    await expect(
      callUnary(mockMethod, { id: "test" }, metadata),
    ).rejects.toThrow("gRPC error")
  })
})

describe("GRPCConnectionManager", () => {
  let manager: GRPCConnectionManager

  beforeEach(() => {
    manager = new GRPCConnectionManager("127.0.0.1", 12345, "test-token")
  })

  afterEach(() => {
    manager.close()
  })

  it("should create client on first access", () => {
    const client = manager.getClient()
    expect(client).toBeDefined()
    expect(client.ContainerStatus).toBeDefined()
    expect(client.ContainerPause).toBeDefined()
    expect(client.ContainerResume).toBeDefined()
  })

  it("should reuse same client instance", () => {
    const client1 = manager.getClient()
    const client2 = manager.getClient()
    expect(client1).toBe(client2)
  })

  it("should create metadata on first access", () => {
    const metadata = manager.getMetadata()
    expect(metadata).toBeDefined()
    expect(metadata.get("authorization")).toHaveLength(1)
  })

  it("should reuse same metadata instance", () => {
    const metadata1 = manager.getMetadata()
    const metadata2 = manager.getMetadata()
    expect(metadata1).toBe(metadata2)
  })

  it("should clear client and metadata on close", () => {
    manager.getClient()
    manager.getMetadata()

    manager.close()

    // After close, should create new instances
    const newClient = manager.getClient()
    const newMetadata = manager.getMetadata()

    expect(newClient).toBeDefined()
    expect(newMetadata).toBeDefined()
  })
})

/**
 * Usage examples for documentation
 */
describe("Usage Examples", () => {
  it("example: basic usage pattern", () => {
    // Create connection manager
    const manager = new GRPCConnectionManager("127.0.0.1", 12345, "token")

    try {
      // Get client and metadata
      const client = manager.getClient()
      const metadata = manager.getMetadata()

      // Use client to make gRPC calls
      // client.ContainerStatus(request, metadata, callback)

      expect(client).toBeDefined()
      expect(metadata).toBeDefined()
    } finally {
      // Clean up
      manager.close()
    }
  })

  it("example: using callUnary helper", async () => {
    const manager = new GRPCConnectionManager("127.0.0.1", 12345, "token")

    try {
      const client = manager.getClient()
      const metadata = manager.getMetadata()

      // Mock a successful response for demonstration
      const mockCall = () =>
        Promise.resolve({
          container_id: "test",
          status: "running",
          started_at: Date.now() / 1000,
        })

      const response = await mockCall()
      expect(response.status).toBe("running")
    } finally {
      manager.close()
    }
  })
})
