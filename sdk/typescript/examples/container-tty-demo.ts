/**
 * Example demonstrating Container TTY methods
 *
 * This example shows how to use the new TTY integration with Container:
 * - Creating interactive terminal sessions
 * - Sending commands via stdin
 * - Reading output in real-time
 * - Reconnecting to sessions
 * - Terminal resize support
 */

import { connect } from "../src/index.js"

async function main() {
  await connect(
    async (client) => {
      console.log("=== Container TTY Demo ===\n")

      // Example 1: Simple terminal session
      console.log("1. Creating interactive terminal session...")
      const container = client.container().from("alpine:latest")

      const session = await container.terminal({
        command: ["/bin/sh"],
      })

      console.log(`   Session ID: ${session.sessionId}`)
      console.log("   Sending commands...\n")

      await session.writeStdin("echo 'Hello from TTY'\n")
      await session.writeStdin("pwd\n")
      await session.writeStdin("ls -la\n")
      await session.writeStdin("exit\n")

      for await (const output of session) {
        if (output.stdout) {
          process.stdout.write(`   [stdout] ${output.stdout.toString()}`)
        }
        if (output.stderr) {
          process.stderr.write(`   [stderr] ${output.stderr.toString()}`)
        }
        if (output.exitCode !== undefined) {
          console.log(`\n   Exited with code: ${output.exitCode}\n`)
          break
        }
      }

      // Example 2: Terminal with custom environment
      console.log("2. Terminal with custom environment...")
      const envSession = await container.terminal({
        command: ["/bin/sh"],
        env: { CUSTOM_VAR: "Hello World", PATH: "/usr/local/bin:/usr/bin:/bin" },
        workingDir: "/tmp",
      })

      await envSession.writeStdin("echo $CUSTOM_VAR\n")
      await envSession.writeStdin("exit\n")

      for await (const output of envSession) {
        if (output.stdout) {
          process.stdout.write(`   ${output.stdout.toString()}`)
        }
        if (output.exitCode !== undefined) {
          console.log(`   Session ended\n`)
          break
        }
      }

      // Example 3: Interactive command execution
      console.log("3. Running multiple commands...")
      const multiSession = await container.terminal()

      const commands = [
        "echo 'Starting commands...'",
        "date",
        "uname -a",
        "echo 'Done!'",
        "exit",
      ]

      for (const cmd of commands) {
        console.log(`   > ${cmd}`)
        await multiSession.writeStdin(cmd + "\n")
        await new Promise((resolve) => setTimeout(resolve, 100))
      }

      for await (const output of multiSession) {
        if (output.stdout) {
          process.stdout.write(`   ${output.stdout.toString()}`)
        }
        if (output.exitCode !== undefined) {
          break
        }
      }
      console.log()

      // Example 4: Terminal resize demonstration
      console.log("4. Terminal with resize...")
      const resizeSession = await container.terminal({
        command: ["/bin/sh"],
      })

      console.log("   Initial size: 80x24")
      await resizeSession.resize(80, 24)

      await resizeSession.writeStdin("echo 'Terminal initialized'\n")

      console.log("   Resizing to 120x40...")
      await resizeSession.resize(120, 40)

      await resizeSession.writeStdin("echo 'Terminal resized'\n")
      await resizeSession.writeStdin("exit\n")

      for await (const output of resizeSession) {
        if (output.stdout) {
          process.stdout.write(`   ${output.stdout.toString()}`)
        }
        if (output.exitCode !== undefined) {
          break
        }
      }
      console.log()

      // Example 5: Session reconnection
      console.log("5. Demonstrating session reconnection...")
      const persistentSession = await container.terminal()
      const savedSessionId = persistentSession.sessionId

      console.log(`   Created session: ${savedSessionId}`)
      await persistentSession.writeStdin("echo 'First connection'\n")

      let firstOutput = ""
      for await (const output of persistentSession) {
        if (output.stdout) {
          firstOutput += output.stdout.toString()
          if (firstOutput.includes("First connection")) {
            console.log("   First connection output received")
            break
          }
        }
      }

      const lastSeq = persistentSession.lastSequence
      console.log(`   Last sequence: ${lastSeq}`)

      // Simulate disconnect and reconnect
      console.log("   Reconnecting to session...")
      const reconnectedSession = await container.reconnectTerminal(
        savedSessionId!,
        {
          replayFromSequence: lastSeq,
        },
      )

      console.log("   Reconnected! Sending new command...")
      await reconnectedSession.writeStdin("echo 'Second connection'\n")
      await reconnectedSession.writeStdin("exit\n")

      for await (const output of reconnectedSession) {
        if (output.stdout) {
          process.stdout.write(`   [reconnected] ${output.stdout.toString()}`)
        }
        if (output.exitCode !== undefined) {
          console.log("   Session ended after reconnection\n")
          break
        }
      }

      console.log("=== Demo Complete ===")
    },
    { LogOutput: process.stderr },
  )
}

main()
