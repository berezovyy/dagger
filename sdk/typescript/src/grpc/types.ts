/**
 * TypeScript types for gRPC Exec service
 * Based on engine/session/exec/exec.proto
 *
 * These types provide a clean TypeScript interface for the proto messages,
 * making development easier with proper IDE support and type checking.
 */

/**
 * Request to query container status
 */
export interface ContainerStatusRequest {
  container_id: string
  include_resource_usage?: boolean
}

/**
 * Response containing container status information
 */
export interface ContainerStatusResponse {
  container_id: string
  /** Status: "created" | "running" | "paused" | "stopped" | "exited" | "unknown" */
  status: string
  /** Exit code (only present if container exited) */
  exit_code?: number
  /** Unix timestamp when container started */
  started_at: number
  /** Unix timestamp when container finished (only if finished) */
  finished_at?: number
  /** Resource usage statistics (only if requested) */
  resource_usage?: ResourceUsage
}

/**
 * Container resource usage statistics
 */
export interface ResourceUsage {
  /** CPU usage percentage */
  cpu_percent: number
  /** Memory usage in bytes */
  memory_bytes: number
  /** Memory limit in bytes */
  memory_limit: number
  /** I/O read bytes */
  io_read_bytes: number
  /** I/O write bytes */
  io_write_bytes: number
}

/**
 * Request to control a container (pause/resume)
 */
export interface ContainerControlRequest {
  container_id: string
}

/**
 * Request to send a signal to a container
 */
export interface ContainerSignalRequest {
  container_id: string
  /** Signal name (e.g., "SIGTERM", "SIGKILL", "SIGINT", "SIGHUP") */
  signal: string
}

/**
 * Response from container control operations
 */
export interface ContainerControlResponse {
  /** Whether the operation succeeded */
  success: boolean
  /** Error message or success confirmation */
  message: string
}

/**
 * Request to subscribe to container lifecycle events
 */
export interface ContainerLifecycleRequest {
  /** Optional: filter by specific container ID. Empty = subscribe to all containers */
  container_id?: string
}

/**
 * Container lifecycle event
 */
export interface ContainerLifecycleEvent {
  container_id: string
  /** Event type: "registered" | "started" | "paused" | "resumed" | "exited" | "unregistered" */
  event_type: string
  /** Current status after event */
  status: string
  /** Unix timestamp */
  timestamp: number
  /** Exit code (for "exited" events, 0 if not applicable) */
  exit_code?: number
  /** Human-readable description */
  message: string
}

/**
 * Request message for bidirectional session stream
 */
export interface SessionRequest {
  /** Start a new session */
  start?: SessionStart
  /** Send stdin data */
  stdin?: Buffer
  /** Resize the terminal */
  resize?: SessionResize
}

/**
 * Response message from session stream
 */
export interface SessionResponse {
  /** Stdout data */
  stdout?: Buffer
  /** Stderr data */
  stderr?: Buffer
  /** Exit code (when process exits) */
  exit?: number
  /** Ready signal (session initialized) */
  ready?: SessionReady
}

/**
 * Start a new exec session
 */
export interface SessionStart {
  container_id: string
  /** Command and arguments to execute */
  command: string[]
  /** Environment variables */
  env?: { [key: string]: string }
  /** Working directory */
  working_dir?: string
  /** Whether to allocate a TTY */
  tty?: boolean
}

/**
 * Resize the terminal (for TTY sessions)
 */
export interface SessionResize {
  /** Terminal width in columns */
  width: number
  /** Terminal height in rows */
  height: number
}

/**
 * Empty message indicating session is ready
 */
export interface SessionReady {
  // Empty message
}
