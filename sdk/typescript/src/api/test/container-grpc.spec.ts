/**
 * Integration tests for Container gRPC features
 *
 * These tests verify the real-time streaming and control features:
 * - streamLogs(): Real-time log streaming
 * - status(): Container status and resource usage
 * - pause()/resume(): Container lifecycle control
 * - signal(): Send signals to containers
 * - subscribeLifecycle(): Lifecycle event monitoring
 */

import assert from "assert"

import { connection } from "../../connect.js"
import { dag } from "../client.gen.js"

describe("Container gRPC Integration", function () {
  // Set longer timeout for integration tests
  this.timeout(120000)

  describe("streamLogs()", function () {
    it("should stream stdout from a simple command", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["echo", "Hello from streaming"])

        const logs: Buffer[] = []
        for await (const log of container.streamLogs()) {
          if (log.stdout) {
            logs.push(log.stdout)
          }
        }

        const output = Buffer.concat(logs).toString()
        assert.ok(
          output.includes("Hello from streaming"),
          "Expected log output to contain 'Hello from streaming'",
        )
      })
    })

    it("should stream multiple lines of output", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec([
            "sh",
            "-c",
            "for i in 1 2 3 4 5; do echo Line $i; done",
          ])

        const logs: Buffer[] = []
        for await (const log of container.streamLogs()) {
          if (log.stdout) {
            logs.push(log.stdout)
          }
        }

        const output = Buffer.concat(logs).toString()
        assert.ok(output.includes("Line 1"), "Expected output to contain 'Line 1'")
        assert.ok(output.includes("Line 5"), "Expected output to contain 'Line 5'")
      })
    })

    it("should stream stderr separately", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sh", "-c", "echo error message >&2"])

        let stdoutData = ""
        let stderrData = ""

        for await (const log of container.streamLogs()) {
          if (log.stdout) {
            stdoutData += log.stdout.toString()
          }
          if (log.stderr) {
            stderrData += log.stderr.toString()
          }
        }

        assert.ok(
          stderrData.includes("error message"),
          "Expected stderr to contain 'error message'",
        )
      })
    })

    it("should handle containers with no output", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["true"])

        let logCount = 0
        for await (const log of container.streamLogs()) {
          if (log.stdout || log.stderr) {
            logCount++
          }
        }

        // Should complete without error even with no output
        assert.ok(logCount >= 0)
      })
    })
  })

  describe("status()", function () {
    it("should query basic container status", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const status = await container.status()

        assert.ok(status.container_id, "Container ID should be defined")
        assert.ok(status.status, "Status should be defined")
        assert.ok(
          ["created", "running", "exited", "paused"].includes(status.status),
          `Status should be valid, got: ${status.status}`,
        )
      })
    })

    it("should include resource usage when requested", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sleep", "5"])

        // Start container in background
        const execPromise = container.sync()

        // Wait a moment for container to start
        await new Promise((resolve) => setTimeout(resolve, 1000))

        const status = await container.status({ includeResourceUsage: true })

        assert.ok(status.resource_usage, "Resource usage should be defined")
        assert.ok(
          status.resource_usage.memory_bytes !== undefined,
          "Memory bytes should be defined",
        )
        assert.ok(
          status.resource_usage.cpu_percent !== undefined,
          "CPU percent should be defined",
        )

        // Cleanup
        await container.signal("SIGKILL")
        await execPromise.catch(() => {
          /* ignore error */
        })
      })
    })

    it("should work for completed containers", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["echo", "test"])

        // Wait for container to complete
        await container.sync()

        const status = await container.status()
        assert.ok(status.container_id, "Container ID should be defined")
        assert.ok(status.status === "exited", "Status should be 'exited'")
      })
    })
  })

  describe("pause() and resume()", function () {
    it("should pause and resume a running container", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sleep", "60"])

        // Start container
        const execPromise = container.sync()

        // Wait for container to start running
        await new Promise((resolve) => setTimeout(resolve, 1000))

        // Pause
        await container.pause()
        const statusPaused = await container.status()
        assert.strictEqual(
          statusPaused.status,
          "paused",
          "Container should be paused",
        )

        // Resume
        await container.resume()
        const statusRunning = await container.status()
        assert.strictEqual(
          statusRunning.status,
          "running",
          "Container should be running after resume",
        )

        // Cleanup
        await container.signal("SIGKILL")
        await execPromise.catch(() => {
          /* ignore error */
        })
      })
    })

    it("should handle multiple pause/resume cycles", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sleep", "60"])

        const execPromise = container.sync()
        await new Promise((resolve) => setTimeout(resolve, 1000))

        // First cycle
        await container.pause()
        let status = await container.status()
        assert.strictEqual(status.status, "paused")

        await container.resume()
        status = await container.status()
        assert.strictEqual(status.status, "running")

        // Second cycle
        await container.pause()
        status = await container.status()
        assert.strictEqual(status.status, "paused")

        await container.resume()
        status = await container.status()
        assert.strictEqual(status.status, "running")

        // Cleanup
        await container.signal("SIGKILL")
        await execPromise.catch(() => {
          /* ignore error */
        })
      })
    })
  })

  describe("signal()", function () {
    it("should send SIGTERM to gracefully stop container", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec([
            "sh",
            "-c",
            "trap 'echo caught SIGTERM; exit 0' TERM; sleep 60",
          ])

        const execPromise = container.sync()
        await new Promise((resolve) => setTimeout(resolve, 1000))

        // Send SIGTERM
        await container.signal("SIGTERM")

        // Container should exit gracefully
        await execPromise

        const status = await container.status()
        assert.strictEqual(status.status, "exited", "Container should be exited")
      })
    })

    it("should send SIGKILL to forcefully stop container", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sleep", "60"])

        const execPromise = container.sync()
        await new Promise((resolve) => setTimeout(resolve, 1000))

        // Send SIGKILL
        await container.signal("SIGKILL")

        try {
          await execPromise
        } catch (err) {
          // Container was killed, this is expected
          assert.ok(err, "Expected error when container is killed")
        }

        const status = await container.status()
        assert.strictEqual(status.status, "exited", "Container should be exited")
      })
    })
  })

  describe("subscribeLifecycle()", function () {
    it("should receive started and exited events", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["echo", "test"])

        const events: string[] = []
        const eventPromise = (async () => {
          for await (const event of container.subscribeLifecycle()) {
            events.push(event.event_type)
            if (event.event_type === "exited") {
              break
            }
          }
        })()

        await container.sync()
        await eventPromise

        assert.ok(
          events.includes("started"),
          "Should receive 'started' event",
        )
        assert.ok(events.includes("exited"), "Should receive 'exited' event")
      })
    })

    it("should include exit code in exited event", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sh", "-c", "exit 42"])

        let exitCode: number | undefined
        const eventPromise = (async () => {
          for await (const event of container.subscribeLifecycle()) {
            if (event.event_type === "exited") {
              exitCode = event.exit_code
              break
            }
          }
        })()

        try {
          await container.sync()
        } catch (err) {
          // Container exited with non-zero code, this is expected
        }

        await eventPromise

        assert.strictEqual(
          exitCode,
          42,
          "Exit code should be 42 in exited event",
        )
      })
    })

    it("should include timestamp in events", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["echo", "test"])

        const timestamps: number[] = []
        const eventPromise = (async () => {
          for await (const event of container.subscribeLifecycle()) {
            timestamps.push(event.timestamp)
            if (event.event_type === "exited") {
              break
            }
          }
        })()

        await container.sync()
        await eventPromise

        assert.ok(timestamps.length > 0, "Should have timestamps")
        timestamps.forEach((ts) => {
          assert.ok(ts > 0, "Timestamp should be positive")
        })
      })
    })
  })

  describe("Combined Features", function () {
    it("should use logs and status together", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec([
            "sh",
            "-c",
            "for i in 1 2 3; do echo Line $i; sleep 1; done",
          ])

        // Stream logs
        const logsPromise = (async () => {
          const logs: Buffer[] = []
          for await (const log of container.streamLogs()) {
            if (log.stdout) {
              logs.push(log.stdout)
            }
          }
          return Buffer.concat(logs).toString()
        })()

        // Poll status
        const statusPromise = (async () => {
          const statuses: string[] = []
          for (let i = 0; i < 3; i++) {
            await new Promise((resolve) => setTimeout(resolve, 1000))
            const status = await container.status()
            statuses.push(status.status)
          }
          return statuses
        })()

        const [logOutput, statuses] = await Promise.all([
          logsPromise,
          statusPromise,
        ])

        assert.ok(
          logOutput.includes("Line 1"),
          "Logs should contain output",
        )
        assert.ok(statuses.length > 0, "Should have status data")
      })
    })

    it("should monitor lifecycle and stream logs simultaneously", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sh", "-c", "echo Starting; sleep 2; echo Done"])

        const logsPromise = (async () => {
          const logs: Buffer[] = []
          for await (const log of container.streamLogs()) {
            if (log.stdout) {
              logs.push(log.stdout)
            }
          }
          return Buffer.concat(logs).toString()
        })()

        const eventsPromise = (async () => {
          const events: string[] = []
          for await (const event of container.subscribeLifecycle()) {
            events.push(event.event_type)
            if (event.event_type === "exited") {
              break
            }
          }
          return events
        })()

        const [logOutput, events] = await Promise.all([
          logsPromise,
          eventsPromise,
        ])

        assert.ok(logOutput.includes("Starting"), "Should have log output")
        assert.ok(events.includes("started"), "Should have lifecycle events")
        assert.ok(events.includes("exited"), "Should have exited event")
      })
    })
  })

  describe("Error Handling", function () {
    it("should handle status query on non-existent container gracefully", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Query status before any execution
        const status = await container.status()

        // Should return a valid status even if not yet executed
        assert.ok(status.status, "Status should be defined")
      })
    })

    it("should handle streaming from failed container", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withExec(["sh", "-c", "echo Before error; exit 1"])

        const logs: Buffer[] = []
        try {
          for await (const log of container.streamLogs()) {
            if (log.stdout) {
              logs.push(log.stdout)
            }
          }
        } catch (err) {
          // Error is expected
          assert.ok(err, "Should receive error for failed container")
        }

        const output = Buffer.concat(logs).toString()
        assert.ok(
          output.includes("Before error"),
          "Should receive logs before error",
        )
      })
    })
  })
})
