# implementation summary

Summary of recent development work.

## gRPC container streaming

Real-time streaming of container execution output via gRPC.

### new files

**engine**
- `engine/session/exec/exec.go` - gRPC service for streaming
- `engine/session/exec/exec.proto` - protocol definitions
- `engine/session/exec/exec.pb.go` - generated protobuf code
- `engine/session/exec/control.go` - container control operations
- `engine/session/exec/*_test.go` - unit tests
- `engine/session/exec/README.md` - documentation
- `engine/buildkit/teewriter.go` - dual output writer (file + stream)
- `engine/buildkit/state_registry.go` - container state tracking
- `engine/buildkit/state_registry_test.go`

**typescript sdk**
- `sdk/typescript/src/common/grpc/connection.ts` - client connection manager
- `sdk/typescript/src/api/container_grpc.*` - generated client stubs
- `sdk/typescript/proto/` - proto definitions

### modified files

**engine**
- `engine/buildkit/executor.go` - state registry integration
- `engine/buildkit/executor_spec.go` - ExecAttachable and TeeWriter integration

**typescript sdk**
- `sdk/typescript/src/common/graphql/connection.ts` - session info storage
- `sdk/typescript/src/common/graphql/connect.ts` - session info setup
- `sdk/typescript/src/index.ts` - export gRPC types
- `sdk/typescript/src/provisioning/*.ts` - session token handling
- `sdk/typescript/package.json` - add @grpc/grpc-js dependency

### features

- stdout/stderr streams to clients as produced
- TeeWriter provides dual output: file (persistent) + stream (realtime)
- non-blocking design - slow clients don't block container execution
- multi-client support
- container lifecycle events subscription
- non-blocking status queries
- optional resource metrics

### architecture

```
Container Execution
    |
TeeWriter
    ├─> File (stdout/stderr files)
    └─> Stream (gRPC channels) -> Clients
```

### current limitations

Session manager integration incomplete:
- TeeWriter works (dual output functional)
- ExecAttachable receives data
- Clients cannot yet discover/connect (requires session manager work)

TTY support not yet implemented (stdin forwarding, resize).

### commits

- bda8b0d64 feat(llm): implement Claude Code Max CLI integration
- e3243ccf4 feat: Add Claude Code Max subscription integration (experimental)

## llm error handling documentation

Comprehensive error handling strategy documentation for LLM streaming operations.

### files

Located in `core/docs/`:

- `ERROR_HANDLING_README.md` - overview and navigation
- `llm_error_handling_index.md` - package guide
- `STREAMING_ERROR_HANDLING_STRATEGY.md` - complete specification (87KB)
- `llm_error_handling_quick_reference.md` - quick lookup

### content

**error categories**
- retryable (transient): rate limits, timeouts, service unavailable
- permanent: invalid credentials, forbidden, not found
- context-related: user cancellation, timeouts
- resource issues: out of memory, too many files

**retry policy**
- initial delay: 1 second
- max delay: 30 seconds
- max elapsed time: 2 minutes
- exponential backoff with jitter

**fallback mechanisms**
- streaming to buffered mode (on memory exhaustion)
- provider switching (if primary unavailable)
- partial result preservation (on interruption)

### related code

- `core/llm.go` - main loop with retry logic
- `core/llm_anthropic.go` - Anthropic client
- `core/llm_openai.go` - OpenAI client
- `core/llm_google.go` - Google Gemini client
- `core/llm_claude_code.go` - Claude Code CLI client

## next steps

### gRPC streaming

- session manager integration (expose ExecAttachable, client discovery)
- typescript sdk high-level API implementation
- TTY support (stdin, resize)
- integration tests

### llm error handling

Follow implementation phases in documentation:
- add error classification helpers
- enhance retry loop and error messages
- add memory fallback and CLI error handling
- comprehensive testing

## git status

**modified (not committed)**
- engine: executor.go, executor_spec.go
- typescript sdk: package.json, connection files, provisioning files, yarn.lock

**untracked (not committed)**
- all files in `engine/session/exec/`
- `engine/buildkit/teewriter.go`
- `engine/buildkit/state_registry.*`
- typescript gRPC files
- documentation files
