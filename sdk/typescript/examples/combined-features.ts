import { connect } from "@dagger.io/dagger"

/**
 * Example: Use multiple features together
 *
 * This comprehensive example demonstrates how to use all container control features
 * simultaneously:
 * - Stream logs in real-time
 * - Monitor lifecycle events
 * - Poll container status and resource usage
 *
 * All operations run concurrently to provide complete visibility into container execution.
 * This pattern is useful for building monitoring dashboards or CI/CD tools.
 *
 * Usage:
 *   npx tsx examples/combined-features.ts
 */

async function main() {
  await connect(
    async (client) => {
      console.log("=== Dagger Container Control Demo ===\n")

      const container = client
        .container()
        .from("alpine:latest")
        .withExec([
          "sh",
          "-c",
          "echo 'Starting long task...'; " +
            "for i in $(seq 1 20); do " +
            "  echo Progress: $i/20; " +
            "  sleep 1; " +
            "done; " +
            "echo 'Task complete!'",
        ])

      // Stream logs in one async task
      const logsPromise = (async () => {
        console.log(">>> Streaming logs:\n")
        for await (const log of container.streamLogs()) {
          if (log.stdout) {
            process.stdout.write(`  ${log.stdout}`)
          }
        }
        console.log("\n>>> Log stream ended\n")
      })()

      // Monitor lifecycle in another task
      const eventsPromise = (async () => {
        console.log(">>> Monitoring lifecycle:\n")
        for await (const event of container.subscribeLifecycle()) {
          console.log(`  [EVENT] ${event.event_type}: ${event.message}`)
        }
        console.log("\n>>> Event stream ended\n")
      })()

      // Poll status periodically
      const statusPromise = (async () => {
        for (let i = 0; i < 5; i++) {
          await new Promise((resolve) => setTimeout(resolve, 4000))
          const status = await container.status({ includeResourceUsage: true })
          console.log(`\n>>> Status check ${i + 1}:`)
          console.log(`    State: ${status.status}`)
          console.log(
            `    Memory: ${((status.resource_usage?.memory_bytes || 0) / 1024 / 1024).toFixed(2)} MB`
          )
        }
      })()

      // Wait for all to complete
      await Promise.all([logsPromise, eventsPromise, statusPromise])

      console.log("\n=== Demo complete ===")
    },
    { LogOutput: process.stderr }
  )
}

main().catch((error) => {
  console.error("Error:", error)
  process.exit(1)
})
