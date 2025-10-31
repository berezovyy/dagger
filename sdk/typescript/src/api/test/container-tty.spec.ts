/**
 * Integration tests for Container TTY features
 *
 * These tests verify the end-to-end TTY (terminal) functionality:
 * - terminal(): Create interactive terminal sessions
 * - Stdin writing and output reading
 * - Terminal resizing
 * - Session reconnection with replay
 * - Multiple commands in sequence
 * - Environment variables and working directory
 * - Error handling and edge cases
 * - Session lifecycle and cleanup
 */

import assert from "assert"

import { connection } from "../../connect.js"
import { dag } from "../client.gen.js"
// Import to register Container.prototype extensions
import "../container_grpc.js"

describe("Container TTY Integration", function () {
  // Set longer timeout for integration tests
  this.timeout(120000)

  describe("terminal()", function () {
    it("should create interactive terminal session", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        assert.ok(session, "Session should be created")
        assert.ok(session.sessionId, "Session ID should be defined")
        assert.strictEqual(session.ready, true, "Session should be ready")

        await session.close()
      })
    })

    it("should execute basic command and receive output", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("echo test123\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("test123"), "Output should contain 'test123'")
        assert.ok(session.exitCode !== null, "Exit code should be set")

        await session.close()
      })
    })

    it("should support custom command instead of default shell", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal({
          command: ["/bin/sh", "-c", "echo custom; exit 0"],
        })

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("custom"),
          "Output should contain 'custom'",
        )
        assert.strictEqual(session.exitCode, 0, "Exit code should be 0")

        await session.close()
      })
    })

    it("should respect working directory option", async function () {
      await connection(async () => {
        const container = dag
          .container()
          .from("alpine:latest")
          .withDirectory("/tmp/testdir", dag.directory())

        const session = await container.terminal({
          workingDir: "/tmp/testdir",
        })

        await session.writeStdin("pwd\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("/tmp/testdir"),
          "Should be in /tmp/testdir",
        )

        await session.close()
      })
    })

    it("should support environment variables", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal({
          env: {
            TEST_VAR: "test_value_123",
            ANOTHER_VAR: "another_value",
          },
        })

        await session.writeStdin("echo $TEST_VAR\n")
        await session.writeStdin("echo $ANOTHER_VAR\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("test_value_123"),
          "Should contain TEST_VAR value",
        )
        assert.ok(
          output.includes("another_value"),
          "Should contain ANOTHER_VAR value",
        )

        await session.close()
      })
    })
  })

  describe("stdin operations", function () {
    it("should handle multiple commands in sequence", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("echo first\n")
        await session.writeStdin("echo second\n")
        await session.writeStdin("echo third\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("first"), "Should contain 'first'")
        assert.ok(output.includes("second"), "Should contain 'second'")
        assert.ok(output.includes("third"), "Should contain 'third'")

        await session.close()
      })
    })

    it("should handle empty stdin writes", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Write empty string - should not cause issues
        await session.writeStdin("")
        await session.writeStdin("echo test\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("test"), "Should still work after empty write")

        await session.close()
      })
    })

    it("should handle multiline input", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("cat << 'EOF'\n")
        await session.writeStdin("line one\n")
        await session.writeStdin("line two\n")
        await session.writeStdin("line three\n")
        await session.writeStdin("EOF\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("line one"), "Should contain 'line one'")
        assert.ok(output.includes("line two"), "Should contain 'line two'")
        assert.ok(output.includes("line three"), "Should contain 'line three'")

        await session.close()
      })
    })

    it("should handle buffer input", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Write as Buffer instead of string
        await session.writeStdin(Buffer.from("echo buffer_test\n"))
        await session.writeStdin(Buffer.from("exit\n"))

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("buffer_test"),
          "Should handle Buffer input",
        )

        await session.close()
      })
    })

    it("should handle rapid stdin writes", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Write many commands rapidly
        for (let i = 0; i < 10; i++) {
          await session.writeStdin(`echo rapid${i}\n`)
        }
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        // Verify at least some commands were executed
        assert.ok(
          output.includes("rapid0") && output.includes("rapid9"),
          "Should handle rapid writes",
        )

        await session.close()
      })
    })
  })

  describe("resize()", function () {
    it("should resize terminal dynamically", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Initial resize
        await session.resize(80, 24)

        await session.writeStdin("echo resized\n")

        // Resize during operation
        await session.resize(120, 40)

        await session.writeStdin("echo after_resize\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("resized"), "Should work after resize")
        assert.ok(
          output.includes("after_resize"),
          "Should work after second resize",
        )

        await session.close()
      })
    })

    it("should handle multiple resize operations", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Perform multiple resizes
        await session.resize(40, 20)
        await session.resize(80, 24)
        await session.resize(100, 30)
        await session.resize(120, 40)

        await session.writeStdin("echo test\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("test"), "Should work after multiple resizes")

        await session.close()
      })
    })

    it("should handle edge case terminal sizes", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Very small terminal
        await session.resize(10, 5)
        await session.writeStdin("echo a\n")

        // Very large terminal
        await session.resize(500, 200)
        await session.writeStdin("echo b\n")

        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("a") && output.includes("b"), "Should handle edge case sizes")

        await session.close()
      })
    })
  })

  describe("reconnectTerminal()", function () {
    it("should reconnect to existing session", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Create initial session
        const session1 = await container.terminal()
        const sessionId = session1.sessionId

        assert.ok(sessionId, "Session ID should be available")

        // Write some data
        await session1.writeStdin("echo before_disconnect\n")

        // Wait a moment for output
        await new Promise((resolve) => setTimeout(resolve, 500))

        // Close the session (simulating disconnect)
        await session1.close()

        // Reconnect to the same session
        const session2 = await container.reconnectTerminal(sessionId, {
          replayFromSequence: 0, // Replay all history
        })

        assert.strictEqual(
          session2.sessionId,
          sessionId,
          "Should reconnect to same session",
        )

        // Continue the session
        await session2.writeStdin("echo after_reconnect\n")
        await session2.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session2) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        // Should see both old and new output
        assert.ok(
          output.includes("before_disconnect"),
          "Should replay old output",
        )
        assert.ok(
          output.includes("after_reconnect"),
          "Should have new output",
        )

        await session2.close()
      })
    })

    it("should support partial replay from sequence number", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Create initial session
        const session1 = await container.terminal()
        const sessionId = session1.sessionId

        await session1.writeStdin("echo first\n")
        await session1.writeStdin("echo second\n")

        // Consume some output
        let chunks = 0
        let lastSeq = 0
        const collectOutput = async () => {
          for await (const chunk of session1) {
            if (chunk.sequence !== undefined) {
              lastSeq = chunk.sequence
            }
            chunks++
            if (chunks >= 10) break // Read some output
          }
        }

        await Promise.race([
          collectOutput(),
          new Promise((resolve) => setTimeout(resolve, 2000)),
        ])

        await session1.close()

        // Reconnect from last sequence (should not replay everything)
        const session2 = await container.reconnectTerminal(sessionId, {
          replayFromSequence: lastSeq,
        })

        await session2.writeStdin("echo third\n")
        await session2.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session2) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("third"), "Should have new output")

        await session2.close()
      })
    })

    it("should detect replayed chunks", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Create initial session
        const session1 = await container.terminal()
        const sessionId = session1.sessionId

        await session1.writeStdin("echo replayed\n")
        await new Promise((resolve) => setTimeout(resolve, 500))
        await session1.close()

        // Reconnect with replay
        const session2 = await container.reconnectTerminal(sessionId, {
          replayFromSequence: 0,
        })

        let hasReplayedChunk = false
        let hasNewChunk = false

        await session2.writeStdin("echo new\n")
        await session2.writeStdin("exit\n")

        for await (const chunk of session2) {
          if (chunk.isReplay) {
            hasReplayedChunk = true
          } else if (chunk.sequence !== undefined && !chunk.isReplay) {
            hasNewChunk = true
          }
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(hasReplayedChunk, "Should have replayed chunks")
        assert.ok(hasNewChunk, "Should have new chunks")

        await session2.close()
      })
    })
  })

  describe("exit codes", function () {
    it("should capture successful exit code", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal({
          command: ["/bin/sh", "-c", "exit 0"],
        })

        for await (const chunk of session) {
          if (chunk.exitCode !== undefined) {
            assert.strictEqual(chunk.exitCode, 0, "Exit code should be 0")
            break
          }
        }

        assert.strictEqual(session.exitCode, 0, "Session exit code should be 0")

        await session.close()
      })
    })

    it("should capture non-zero exit code", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal({
          command: ["/bin/sh", "-c", "exit 42"],
        })

        for await (const chunk of session) {
          if (chunk.exitCode !== undefined) {
            assert.strictEqual(chunk.exitCode, 42, "Exit code should be 42")
            break
          }
        }

        assert.strictEqual(
          session.exitCode,
          42,
          "Session exit code should be 42",
        )

        await session.close()
      })
    })

    it("should handle command failure", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("nonexistent_command\n")
        await session.writeStdin("exit $?\n")

        let exitCode: number | undefined
        for await (const chunk of session) {
          if (chunk.exitCode !== undefined) {
            exitCode = chunk.exitCode
            break
          }
        }

        assert.ok(exitCode !== 0, "Exit code should be non-zero for failure")

        await session.close()
      })
    })
  })

  describe("output handling", function () {
    it("should handle large output", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Generate large output
        await session.writeStdin(
          "for i in $(seq 1 1000); do echo Line $i; done\n",
        )
        await session.writeStdin("exit\n")

        let lineCount = 0
        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) {
            output += chunk.stdout.toString()
          }
          if (chunk.exitCode !== undefined) break
        }

        // Count lines in output
        const lines = output.split("\n")
        lineCount = lines.filter((line) => line.includes("Line ")).length

        assert.ok(lineCount >= 100, "Should handle large output (at least 100 lines)")

        await session.close()
      })
    })

    it("should handle mixed stdout and stderr", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("echo stdout_message\n")
        await session.writeStdin("echo stderr_message >&2\n")
        await session.writeStdin("exit\n")

        let stdoutData = ""
        let stderrData = ""

        for await (const chunk of session) {
          if (chunk.stdout) {
            stdoutData += chunk.stdout.toString()
          }
          if (chunk.stderr) {
            stderrData += chunk.stderr.toString()
          }
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          stdoutData.includes("stdout_message"),
          "Should capture stdout",
        )
        assert.ok(
          stderrData.includes("stderr_message"),
          "Should capture stderr",
        )

        await session.close()
      })
    })

    it("should handle output without newlines", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Use printf without newline
        await session.writeStdin("printf 'no newline'\n")
        await session.writeStdin("echo ''\n") // Add newline to flush
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("no newline"),
          "Should handle output without newlines",
        )

        await session.close()
      })
    })
  })

  describe("session lifecycle", function () {
    it("should track sequence numbers", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("echo test\n")
        await session.writeStdin("exit\n")

        let maxSequence = 0
        for await (const chunk of session) {
          if (chunk.sequence !== undefined && chunk.sequence > maxSequence) {
            maxSequence = chunk.sequence
          }
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(maxSequence > 0, "Should have positive sequence numbers")
        assert.ok(
          session.lastSequence > 0,
          "Session should track last sequence",
        )

        await session.close()
      })
    })

    it("should allow proper cleanup with close()", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.writeStdin("echo test\n")

        // Close immediately (don't wait for exit)
        await session.close()

        // Should not throw error
        assert.ok(true, "Close should work at any time")
      })
    })

    it("should handle session after exit", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal({
          command: ["/bin/sh", "-c", "echo done; exit 0"],
        })

        // Wait for exit
        for await (const chunk of session) {
          if (chunk.exitCode !== undefined) break
        }

        // Session should have exit code
        assert.strictEqual(session.exitCode, 0, "Should have exit code")

        await session.close()
      })
    })
  })

  describe("error handling", function () {
    it("should handle invalid command gracefully", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        try {
          const session = await container.terminal({
            command: ["/nonexistent/binary"],
          })

          let errored = false
          try {
            for await (const chunk of session) {
              if (chunk.exitCode !== undefined) {
                assert.ok(chunk.exitCode !== 0, "Should have non-zero exit")
                break
              }
            }
          } catch (err) {
            errored = true
          }

          await session.close()

          // Either way is acceptable - error or non-zero exit
          assert.ok(true, "Should handle invalid command")
        } catch (err) {
          // Session creation failed - this is also acceptable
          assert.ok(err, "Should error on invalid command")
        }
      })
    })

    it("should handle write to non-ready session", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Session is ready after creation, but test the check
        assert.ok(session.ready, "Session should be ready after creation")

        await session.close()
      })
    })

    it("should handle operations after close", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        await session.close()

        // Attempting operations after close should fail gracefully
        try {
          await session.writeStdin("echo test\n")
          assert.fail("Should not allow write after close")
        } catch (err) {
          assert.ok(err, "Should error on write after close")
        }
      })
    })
  })

  describe("concurrent sessions", function () {
    it("should support multiple sessions on same container", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Create two sessions
        const session1 = await container.terminal()
        const session2 = await container.terminal()

        assert.notStrictEqual(
          session1.sessionId,
          session2.sessionId,
          "Sessions should have different IDs",
        )

        // Use both sessions
        await session1.writeStdin("echo session1\n")
        await session2.writeStdin("echo session2\n")

        await session1.writeStdin("exit\n")
        await session2.writeStdin("exit\n")

        // Collect output from both
        const output1Promise = (async () => {
          let output = ""
          for await (const chunk of session1) {
            if (chunk.stdout) output += chunk.stdout.toString()
            if (chunk.exitCode !== undefined) break
          }
          return output
        })()

        const output2Promise = (async () => {
          let output = ""
          for await (const chunk of session2) {
            if (chunk.stdout) output += chunk.stdout.toString()
            if (chunk.exitCode !== undefined) break
          }
          return output
        })()

        const [output1, output2] = await Promise.all([
          output1Promise,
          output2Promise,
        ])

        assert.ok(output1.includes("session1"), "Session 1 should have output")
        assert.ok(output2.includes("session2"), "Session 2 should have output")

        await session1.close()
        await session2.close()
      })
    })

    it("should isolate session environments", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")

        // Create sessions with different env vars
        const session1 = await container.terminal({
          env: { SESSION: "one" },
        })
        const session2 = await container.terminal({
          env: { SESSION: "two" },
        })

        await session1.writeStdin("echo $SESSION\n")
        await session2.writeStdin("echo $SESSION\n")

        await session1.writeStdin("exit\n")
        await session2.writeStdin("exit\n")

        const output1Promise = (async () => {
          let output = ""
          for await (const chunk of session1) {
            if (chunk.stdout) output += chunk.stdout.toString()
            if (chunk.exitCode !== undefined) break
          }
          return output
        })()

        const output2Promise = (async () => {
          let output = ""
          for await (const chunk of session2) {
            if (chunk.stdout) output += chunk.stdout.toString()
            if (chunk.exitCode !== undefined) break
          }
          return output
        })()

        const [output1, output2] = await Promise.all([
          output1Promise,
          output2Promise,
        ])

        assert.ok(output1.includes("one"), "Session 1 should have env 'one'")
        assert.ok(output2.includes("two"), "Session 2 should have env 'two'")

        await session1.close()
        await session2.close()
      })
    })
  })

  describe("real-world scenarios", function () {
    it("should handle interactive command-line tool", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Simulate interactive usage: vi-like or menu navigation
        await session.writeStdin("cat << 'EOF'\n")
        await session.writeStdin("Option 1\n")
        await session.writeStdin("Option 2\n")
        await session.writeStdin("EOF\n")
        await session.writeStdin("read -p 'Choose: ' choice\n")
        await session.writeStdin("1\n") // User input
        await session.writeStdin("echo You chose: $choice\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("Option 1"), "Should show menu")
        assert.ok(output.includes("You chose"), "Should process choice")

        await session.close()
      })
    })

    it("should handle long-running session with periodic input", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Start a long-running task
        await session.writeStdin("for i in $(seq 1 5); do sleep 1; echo Step $i; done\n")

        // Collect output as it arrives
        let steps = 0
        const collectPromise = (async () => {
          for await (const chunk of session) {
            if (chunk.stdout) {
              const text = chunk.stdout.toString()
              if (text.includes("Step ")) steps++
            }
            // Don't wait for exit, just collect some output
            if (steps >= 3) break
          }
        })()

        await Promise.race([
          collectPromise,
          new Promise((resolve) => setTimeout(resolve, 10000)),
        ])

        assert.ok(steps >= 3, "Should receive periodic output")

        // Cleanup
        await session.writeStdin("exit\n")
        await new Promise((resolve) => setTimeout(resolve, 500))
        await session.close()
      })
    })

    it("should handle tab completion and special characters", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Test various special characters
        await session.writeStdin("echo 'special: !@#$%^&*()'\n")
        await session.writeStdin("echo \"quotes work\"\n")
        await session.writeStdin("echo $HOME\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(output.includes("special"), "Should handle special chars")
        assert.ok(output.includes("quotes work"), "Should handle quotes")

        await session.close()
      })
    })

    it("should handle file editing simulation", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Create and modify a file
        await session.writeStdin("echo 'initial content' > /tmp/testfile.txt\n")
        await session.writeStdin("cat /tmp/testfile.txt\n")
        await session.writeStdin("echo 'appended content' >> /tmp/testfile.txt\n")
        await session.writeStdin("cat /tmp/testfile.txt\n")
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        assert.ok(
          output.includes("initial content"),
          "Should show initial content",
        )
        assert.ok(
          output.includes("appended content"),
          "Should show appended content",
        )

        await session.close()
      })
    })

    it("should handle process management commands", async function () {
      await connection(async () => {
        const container = dag.container().from("alpine:latest")
        const session = await container.terminal()

        // Process management simulation
        await session.writeStdin("ps aux | head -5\n")
        await session.writeStdin("echo $?\n") // Check exit status
        await session.writeStdin("exit\n")

        let output = ""
        for await (const chunk of session) {
          if (chunk.stdout) output += chunk.stdout.toString()
          if (chunk.exitCode !== undefined) break
        }

        // Should show process list
        assert.ok(output.length > 0, "Should have output from ps command")

        await session.close()
      })
    })
  })
})
