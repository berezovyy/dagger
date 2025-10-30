import { connect } from "@dagger.io/dagger"

/**
 * Example: Subscribe to container lifecycle events
 *
 * This example demonstrates how to subscribe to container lifecycle events
 * in real-time. Events include:
 * - started: Container has started executing
 * - exited: Container has finished (includes exit code)
 * - error: An error occurred during execution
 *
 * Each event includes a timestamp, status, and optional message.
 *
 * Usage:
 *   npx tsx examples/lifecycle-events.ts
 */

async function main() {
  await connect(
    async (client) => {
      const container = client
        .container()
        .from("alpine:latest")
        .withExec(["sh", "-c", "echo Starting; sleep 10; echo Done"])

      // Subscribe to lifecycle events
      const eventPromise = (async () => {
        console.log("=== Lifecycle Events ===\n")

        for await (const event of container.subscribeLifecycle()) {
          console.log(`[${new Date(event.timestamp * 1000).toISOString()}]`)
          console.log(`  Event: ${event.event_type}`)
          console.log(`  Status: ${event.status}`)
          console.log(`  Message: ${event.message}`)
          if (event.exit_code !== undefined) {
            console.log(`  Exit Code: ${event.exit_code}`)
          }
          console.log()
        }
      })()

      // Run container
      console.log("Starting container execution...\n")
      await container.sync()

      // Wait for events to complete
      await eventPromise

      console.log("=== All events received ===")
    },
    { LogOutput: process.stderr }
  )
}

main().catch((error) => {
  console.error("Error:", error)
  process.exit(1)
})
