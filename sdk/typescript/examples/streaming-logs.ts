import { connect } from "@dagger.io/dagger"

/**
 * Example: Stream container logs in real-time
 *
 * This example demonstrates how to stream stdout and stderr from a running container
 * in real-time using the streamLogs() method. This is useful for monitoring long-running
 * processes or debugging container execution.
 *
 * Usage:
 *   npx tsx examples/streaming-logs.ts
 */

async function main() {
  await connect(
    async (client) => {
      console.log("Building and streaming logs from Alpine container...\n")

      const container = client
        .container()
        .from("alpine:latest")
        .withExec([
          "sh",
          "-c",
          "for i in $(seq 1 10); do echo Line $i; sleep 1; done",
        ])

      // Stream logs in real-time
      console.log("=== Streaming stdout/stderr ===")
      for await (const log of container.streamLogs()) {
        if (log.stdout) {
          process.stdout.write(log.stdout)
        }
        if (log.stderr) {
          process.stderr.write(log.stderr)
        }
      }

      console.log("\n=== Stream complete ===")
    },
    { LogOutput: process.stderr }
  )
}

main().catch((error) => {
  console.error("Error:", error)
  process.exit(1)
})
