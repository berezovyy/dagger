/**
 * Example demonstrating Container gRPC methods
 *
 * This example shows how to use the new gRPC-based methods for:
 * - Querying container status
 * - Controlling container execution (pause/resume/signal)
 * - Streaming logs in real-time
 * - Subscribing to lifecycle events
 */

import { connect } from "../src/index.js"

async function main() {
  await connect(
    async (client) => {
      console.log("=== Container gRPC Methods Demo ===\n")

      // Create a container
      const container = client
        .container()
        .from("alpine:latest")
        .withExec(["sh", "-c", "echo 'Starting...'; sleep 5; echo 'Done!'"])

      // Example 1: Query container status
      console.log("1. Querying container status...")
      const status = await container.status({ includeResourceUsage: true })
      console.log(`   Status: ${status.status}`)
      console.log(`   Container ID: ${status.container_id}`)
      if (status.resource_usage) {
        console.log(
          `   CPU: ${status.resource_usage.cpu_percent.toFixed(2)}%`,
        )
        console.log(
          `   Memory: ${(status.resource_usage.memory_bytes / 1024 / 1024).toFixed(2)} MB`,
        )
      }
      console.log()

      // Example 2: Stream logs in real-time
      console.log("2. Streaming logs...")
      for await (const chunk of container.streamLogs()) {
        if (chunk.stdout) {
          process.stdout.write(`   [stdout] ${chunk.stdout.toString()}`)
        }
        if (chunk.stderr) {
          process.stderr.write(`   [stderr] ${chunk.stderr.toString()}`)
        }
      }
      console.log()

      // Example 3: Subscribe to lifecycle events
      console.log("3. Subscribing to lifecycle events...")
      const eventContainer = client
        .container()
        .from("alpine:latest")
        .withExec(["echo", "lifecycle test"])

      for await (const event of eventContainer.subscribeLifecycle()) {
        console.log(
          `   [${event.event_type}] ${event.message} (status: ${event.status})`,
        )
        if (event.event_type === "exited") {
          console.log(`   Exit code: ${event.exit_code}`)
          break
        }
      }
      console.log()

      // Example 4: Control operations (pause/resume)
      console.log("4. Testing pause/resume...")
      const longRunning = client
        .container()
        .from("alpine:latest")
        .withExec(["sleep", "10"])

      // Start the container (non-blocking)
      const execPromise = longRunning.sync()

      // Wait a bit, then pause
      await new Promise((resolve) => setTimeout(resolve, 1000))
      console.log("   Pausing container...")
      await longRunning.pause()

      const pausedStatus = await longRunning.status()
      console.log(`   Container status after pause: ${pausedStatus.status}`)

      // Resume
      console.log("   Resuming container...")
      await longRunning.resume()

      const resumedStatus = await longRunning.status()
      console.log(`   Container status after resume: ${resumedStatus.status}`)

      // Wait for completion
      await execPromise
      console.log()

      // Example 5: Send signal
      console.log("5. Testing signal...")
      const signalContainer = client
        .container()
        .from("alpine:latest")
        .withExec(["sleep", "30"])

      // Start container
      const signalExec = signalContainer.sync()

      // Send SIGTERM after 1 second
      await new Promise((resolve) => setTimeout(resolve, 1000))
      console.log("   Sending SIGTERM...")
      await signalContainer.signal("SIGTERM")

      try {
        await signalExec
      } catch (e) {
        console.log(
          `   Container terminated with signal (expected): ${(e as Error).message}`,
        )
      }

      console.log("\n=== Demo Complete ===")
    },
    { LogOutput: process.stderr },
  )
}

main()
