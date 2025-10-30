import { connect } from "@dagger.io/dagger"

/**
 * Example: Pause, resume, and signal containers
 *
 * This example demonstrates container lifecycle control operations:
 * - pause(): Temporarily suspend a running container
 * - resume(): Resume a paused container
 * - signal(): Send Unix signals (SIGTERM, SIGKILL, etc.) to the container
 *
 * These operations are useful for resource management and graceful shutdown.
 *
 * Usage:
 *   npx tsx examples/container-control.ts
 */

async function main() {
  await connect(
    async (client) => {
      const container = client
        .container()
        .from("alpine:latest")
        .withExec(["sh", "-c", "while true; do date; sleep 2; done"])

      // Start container
      console.log("Starting container...")
      const execPromise = container.sync()

      // Let it run for a bit
      await new Promise((resolve) => setTimeout(resolve, 5000))

      // Pause container
      console.log("\nPausing container...")
      await container.pause()

      // Verify paused
      const statusPaused = await container.status()
      console.log(`Status after pause: ${statusPaused.status}`)

      await new Promise((resolve) => setTimeout(resolve, 3000))

      // Resume container
      console.log("\nResuming container...")
      await container.resume()

      // Verify running
      const statusRunning = await container.status()
      console.log(`Status after resume: ${statusRunning.status}`)

      await new Promise((resolve) => setTimeout(resolve, 5000))

      // Send SIGTERM for graceful shutdown
      console.log("\nSending SIGTERM for graceful shutdown...")
      await container.signal("SIGTERM")

      try {
        await execPromise
      } catch (err) {
        console.log("Container terminated as expected")
      }

      console.log("\n=== Control demo complete ===")
    },
    { LogOutput: process.stderr }
  )
}

main().catch((error) => {
  console.error("Error:", error)
  process.exit(1)
})
