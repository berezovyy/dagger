import { connect } from "@dagger.io/dagger"

/**
 * Example: Monitor container status without blocking
 *
 * This example shows how to query container status including resource usage metrics
 * like memory consumption and CPU usage. The status() method is non-blocking,
 * allowing you to poll container state while it runs.
 *
 * Usage:
 *   npx tsx examples/status-monitoring.ts
 */

async function main() {
  await connect(
    async (client) => {
      const container = client
        .container()
        .from("alpine:latest")
        .withExec(["sleep", "30"])

      // Start container in background
      const execPromise = container.sync()

      // Poll status while running
      console.log("Monitoring container status...\n")

      for (let i = 0; i < 10; i++) {
        const status = await container.status({ includeResourceUsage: true })

        console.log(`[${new Date().toISOString()}]`)
        console.log(`  Status: ${status.status}`)
        console.log(
          `  Memory: ${((status.resource_usage?.memory_bytes || 0) / 1024 / 1024).toFixed(2)} MB`
        )
        console.log(`  CPU: ${(status.resource_usage?.cpu_percent || 0).toFixed(2)}%\n`)

        await new Promise((resolve) => setTimeout(resolve, 3000))
      }

      await execPromise
      console.log("Container finished")
    },
    { LogOutput: process.stderr }
  )
}

main().catch((error) => {
  console.error("Error:", error)
  process.exit(1)
})
