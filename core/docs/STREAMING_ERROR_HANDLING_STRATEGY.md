# Comprehensive Error Handling Strategy for Streaming Operations

## Executive Summary

This document outlines a production-ready error handling strategy for streaming operations in the Dagger LLM subsystem. The strategy addresses network failures, container errors, permission issues, resource exhaustion, and graceful degradation with clear recovery paths.

---

## 1. Error Taxonomy

### 1.1 Network Errors

#### Transient Network Errors (Retryable)
- **Rate Limiting**: HTTP 429, "rate_limit" messages
  - Severity: Medium
  - Retry Strategy: Exponential backoff with jitter
  - Max Retries: 3-5 attempts
  
- **Timeout/Deadline Exceeded**: Context deadline exceeded, read/write timeouts
  - Severity: Medium
  - Retry Strategy: Exponential backoff
  - Max Retries: 2-3 attempts
  - Initial Delay: 1 second
  - Max Delay: 30 seconds

- **Temporary Network Failures**: Connection reset, temporary failures
  - Severity: Medium
  - Retry Strategy: Exponential backoff
  - Max Retries: 3 attempts

- **Server Unavailable**: HTTP 503, "Service Unavailable", "Overloaded"
  - Severity: Medium
  - Retry Strategy: Exponential backoff with longer delays
  - Max Retries: 5 attempts
  - Max Elapsed Time: 2 minutes

#### Permanent Network Errors (Non-Retryable)
- **Connection Refused**: Cannot establish connection
  - Severity: High
  - Action: Fail fast, attempt fallback
  - User Message: "Unable to connect to LLM service. Check network connectivity."

- **DNS Resolution Failure**: Host unreachable
  - Severity: High
  - Action: Fail fast
  - User Message: "Cannot resolve API endpoint. Check base URL configuration."

- **Protocol Errors**: Malformed HTTP, invalid headers
  - Severity: High
  - Action: Fail fast
  - User Message: "Invalid API protocol. Check endpoint configuration."

### 1.2 Authentication/Authorization Errors

#### Credential Issues (Non-Retryable)
- **Invalid API Key**: 401 Unauthorized, "invalid api key"
  - Severity: High
  - Action: Fail immediately
  - User Message: "Invalid API key. Please verify credentials in configuration."
  - Fallback: None (requires user intervention)

- **Missing Credentials**: No API key configured
  - Severity: High
  - Action: Fail at initialization
  - User Message: "LLM provider credentials not configured. Set environment variables."

- **Credential Rotation**: Expired tokens, revoked access
  - Severity: High
  - Action: Fail immediately
  - User Message: "Access denied. Your credentials may have expired. Please refresh."

#### Permission Errors (Non-Retryable)
- **Insufficient Quota**: Account limits exceeded
  - Severity: High
  - Action: Fail immediately
  - User Message: "API quota exceeded. Upgrade your plan or wait for reset."

- **Model Access Denied**: Model not available for this account
  - Severity: High
  - Action: Fail immediately
  - User Message: "Model not available. Check account permissions or try a different model."

### 1.3 Container/Resource Errors

#### Resource Exhaustion (Retryable with fallback)
- **Out of Memory**: Memory allocation failed
  - Severity: High
  - Retry Strategy: None (fallback to buffered mode)
  - Cleanup: Flush stream buffer, release pending data
  - User Message: "Stream processing requires buffered mode. Switching..."

- **Too Many Open Files**: Resource limit reached
  - Severity: Medium
  - Retry Strategy: Close idle streams, retry with backoff
  - Max Retries: 2 attempts
  - Cleanup: Close stream, release file descriptors

- **CPU/Throttling**: System under load
  - Severity: Medium
  - Retry Strategy: Backoff with longer delays
  - Cleanup: Release processing resources

#### Container Communication Errors (Retryable)
- **Docker Socket Timeout**: Container not responding
  - Severity: Medium
  - Retry Strategy: Exponential backoff
  - Max Retries: 3 attempts
  - Cleanup: Close socket, reset connection pool

### 1.4 LLM Provider-Specific Errors

#### Anthropic API Errors
- **Rate Limit** (HTTP 429): `rate_limit_error`
  - Retryable: Yes (exponential backoff)
  - Retry Strategy: 3-5 attempts with 1s-30s backoff
  
- **Overloaded** (HTTP 529): `overloaded_error`
  - Retryable: Yes (exponential backoff)
  - Retry Strategy: 3-5 attempts with 1s-30s backoff

- **Invalid Request** (HTTP 400): Malformed tool schema, empty content
  - Retryable: No (permanent)
  - Workaround: Empty content → space character override

#### OpenAI API Errors
- **Internal Retry Logic**: Client handles retries automatically
  - Custom Retry: Not needed (delegated to SDK)
  - IsRetryable: Always returns false

#### Google Gemini API Errors
- **Service Unavailable** (HTTP 503)
  - Retryable: Yes (exponential backoff)
  - Retry Strategy: 3-5 attempts

- **Rate Limit** (HTTP 429)
  - Retryable: Yes (exponential backoff)
  - Retry Strategy: 3-5 attempts

#### Claude Code CLI Errors
- **CLI Not Found**: Claude command not in PATH
  - Retryable: No
  - User Message: "Claude Code CLI not found. Install with: npm install -g @anthropic-ai/claude-code"

- **Connection Refused**: CLI can't connect to API
  - Retryable: Yes
  - Retry Strategy: Exponential backoff

### 1.5 Stream-Specific Errors

#### Streaming Protocol Errors (Retryable)
- **Partial Message**: Incomplete chunk received
  - Severity: High
  - Action: Retry entire request
  - Cleanup: Discard partial state

- **Invalid Event Format**: Malformed SSE event
  - Severity: High
  - Action: Fail stream, retry request
  - Cleanup: Release event accumulator

- **Stream Closed Unexpectedly**: Connection dropped mid-stream
  - Severity: Medium
  - Retry Strategy: Exponential backoff
  - Max Retries: 2-3 attempts
  - Cleanup: Close stream, reset state

#### Data Processing Errors (Non-Retryable)
- **Invalid JSON in Tool Args**: Malformed function arguments
  - Severity: High
  - Action: Return wrapped error with context
  - User Message: "Tool invocation failed: invalid arguments"

- **Tool Call ID Mismatch**: Referenced tool not found
  - Severity: High
  - Action: Return error to LLM
  - User Message: "Tool not found: [name]. Available tools: [list]"

### 1.6 Context/Cancellation Errors

#### Context Cancellation (Non-Retryable)
- **User Interruption**: Context.Cancel()
  - Severity: Low (expected)
  - Action: Graceful shutdown
  - Cleanup: Close stream, preserve partial results
  - Behavior: Allow use of partial LLM response

- **Timeout**: Context deadline exceeded
  - Severity: Medium
  - Action: Graceful shutdown
  - Cleanup: Close stream, preserve partial results
  - Fallback: Use accumulated response if meaningful

---

## 2. Handling Strategy for Each Error Type

### 2.1 Classification Decision Tree

```
Error occurs
├─ Is it context cancellation/timeout?
│  └─ Graceful shutdown, preserve state
├─ Can we determine error type?
│  ├─ Network error?
│  │  ├─ Transient? → Retry with backoff
│  │  └─ Permanent? → Fail fast
│  ├─ Auth error? → Fail fast
│  ├─ Resource error?
│  │  ├─ Memory? → Switch to buffered mode
│  │  └─ File descriptors? → Close + retry
│  ├─ Stream protocol error?
│  │  └─ Retry full request
│  └─ Data error? → Return wrapped error
└─ Unknown error? → Log, fail with context
```

### 2.2 Error Handling by Operation Phase

#### Phase 1: Connection Establishment
```
Events:
  - Endpoint resolution
  - TLS handshake
  - API authentication
  - Stream subscription
  
Error Handling:
  ├─ DNS failure → Fail fast
  ├─ TLS error → Fail fast
  ├─ Auth error → Fail fast
  ├─ Timeout → Retry with backoff
  └─ Connection refused → Retry or fallback
```

#### Phase 2: Stream Initialization
```
Events:
  - Streaming protocol setup
  - Initial handshake
  - Stream ready
  
Error Handling:
  ├─ Protocol error → Fail fast
  ├─ Timeout → Retry with backoff
  └─ Connection error → Retry with backoff
```

#### Phase 3: Data Streaming
```
Events:
  - Receiving chunks
  - Accumulating content
  - Processing events
  
Error Handling:
  ├─ Malformed event → Retry full request
  ├─ Connection drop → Retry with backoff
  ├─ Data corruption → Retry full request
  ├─ Memory pressure → Switch to buffered
  └─ Processing error → Fail gracefully
```

#### Phase 4: Stream Finalization
```
Events:
  - Final message received
  - Stream closed
  - Response accumulated
  
Error Handling:
  ├─ Incomplete accumulation → Retry
  ├─ Close error → Ignore (best effort)
  └─ JSON parsing error → Return wrapped error
```

### 2.3 Provider-Specific Strategies

#### Anthropic Client Strategy

```go
// Retry Decision
func (c *AnthropicClient) IsRetryable(err error) bool {
    retryablePatterns := []string{
        "rate_limit_error",
        "overloaded_error",
        "Internal server error",
    }
    msg := err.Error()
    for _, pattern := range retryablePatterns {
        if strings.Contains(msg, pattern) {
            return true
        }
    }
    return false
}

// Stream Error Handling
stream := c.client.Messages.NewStreaming(ctx, params)
defer stream.Close()

if err := stream.Err(); err != nil {
    // Non-retryable: permanent failure
    return nil, fmt.Errorf("stream setup failed: %w", err)
}

acc := new(anthropic.Message)
for stream.Next() {
    // Process events, collect usage metrics
}

// Check final stream error
if err := stream.Err(); err != nil {
    // Retryable at parent level
    return nil, err
}
```

#### OpenAI Client Strategy

```go
// OpenAI handles retries internally
func (c *OpenAIClient) IsRetryable(err error) bool {
    // Delegate to SDK
    return false
}

// Stream with fallback to non-streaming
if len(tools) > 0 && c.disableStreaming {
    // Fall back to buffered mode
    return c.queryWithoutStreaming(ctx, params, ...)
} else {
    // Try streaming first
    return c.queryWithStreaming(ctx, params, ...)
}

// Streaming implementation
stream := c.client.Chat.Completions.NewStreaming(ctx, params)

if stream.Err() != nil {
    // Early detection before iteration
    return nil, stream.Err()
}
defer stream.Close()

acc := new(openai.ChatCompletionAccumulator)
for stream.Next() {
    res := stream.Current()
    acc.AddChunk(res)
}

if stream.Err() != nil {
    // Final error check
    return nil, stream.Err()
}
```

#### Google Gemini Client Strategy

```go
// Detect retryable HTTP errors
func (c *GenaiClient) IsRetryable(err error) bool {
    var apiErr genai.APIError
    if !errors.As(err, &apiErr) {
        return false
    }
    switch apiErr.Code {
    case http.StatusServiceUnavailable, http.StatusTooManyRequests:
        return true
    default:
        return false
    }
}

// Stream error handling with APIError wrapper
for res, err := range stream {
    if err != nil {
        if apiErr, ok := err.(*apierror.APIError); ok {
            err = fmt.Errorf("google API error: %w", apiErr.Unwrap())
        }
        return content, toolCalls, tokenUsage, err
    }
    // Process response
}
```

#### Claude Code CLI Strategy

```go
// Retryable errors
func (c *ClaudeCodeClient) IsRetryable(err error) bool {
    retryableErrors := []string{
        "rate_limit", "timeout", "overloaded",
        "Internal server error", "503", "429",
        "connection refused", "deadline exceeded",
        "temporary failure",
    }
    // Check all patterns
}

// CLI-specific error handling
output, err := cmd.CombinedOutput()
if err != nil {
    if errors.Is(err, exec.ErrNotFound) {
        // CLI not installed
        return "", fmt.Errorf("claude CLI not found: install with npm install -g @anthropic-ai/claude-code")
    }
    return "", fmt.Errorf("CLI execution failed: %w\nOutput: %s", err, output)
}

// Check response errors
var resp claudeCodeResponse
if err := json.Unmarshal(output); err != nil {
    // Malformed JSON - fail fast
    return "", fmt.Errorf("invalid response: %w\nOutput: %s", err, output)
}

if resp.IsError {
    // Check if retryable via message content
    return "", fmt.Errorf("CLI error: %s", resp.Result)
}
```

---

## 3. Retry Policies

### 3.1 Exponential Backoff Configuration

```go
type RetryPolicy struct {
    InitialInterval time.Duration  // 1 second
    MaxInterval     time.Duration  // 30 seconds
    MaxElapsedTime  time.Duration  // 2 minutes
    Multiplier      float64        // 2.0 (double each time)
    Jitter          float64        // 0.1 (10% randomness)
}

// Current implementation (llm.go)
b := backoff.NewExponentialBackOff()
b.InitialInterval = 1 * time.Second
b.MaxInterval = 30 * time.Second
b.MaxElapsedTime = 2 * time.Minute
```

### 3.2 Retry Decision Matrix

| Error Type | Retryable | Max Attempts | Initial Delay | Max Delay | Max Time |
|---|---|---|---|---|---|
| Rate Limit (429) | Yes | 5 | 1s | 30s | 2m |
| Service Unavailable (503) | Yes | 5 | 1s | 30s | 2m |
| Timeout | Yes | 3 | 1s | 30s | 2m |
| Temporary Network Failure | Yes | 3 | 1s | 30s | 2m |
| Invalid API Key | No | 0 | - | - | - |
| Malformed Request | No | 0 | - | - | - |
| Model Finished (stop reason) | No* | 0 | - | - | - |
| Memory Exhausted | No* | 0 | - | - | - |

*Can trigger fallback mechanisms instead of retry

### 3.3 Retry Loop Implementation Pattern

```go
// General pattern used in llm.go
b := backoff.NewExponentialBackOff()
b.InitialInterval = 1 * time.Second
b.MaxInterval = 30 * time.Second
b.MaxElapsedTime = 2 * time.Minute

err := backoff.Retry(func() error {
    ctx, span := Tracer(ctx).Start(ctx, "LLM query")
    res, sendErr := client.SendQuery(ctx, messages, tools)
    telemetry.End(span, func() error { return sendErr })
    
    if sendErr != nil {
        // Model explicitly finished - don't retry
        var finished *ModelFinishedError
        if errors.As(sendErr, &finished) {
            return backoff.Permanent(sendErr)
        }
        
        // Provider says non-retryable - don't retry
        if !client.IsRetryable(sendErr) {
            return backoff.Permanent(sendErr)
        }
        
        // Retryable error - backoff will retry
        return sendErr
    }
    
    return nil // Success
}, backoff.WithContext(b, ctx))

if err != nil {
    return fmt.Errorf("not retrying: %w", err)
}
```

### 3.4 Per-Provider Retry Policies

#### Anthropic
- Detects: `rate_limit_error`, `overloaded_error`, `Internal server error`
- Uses: Parent exponential backoff (1s-30s, 2m max)
- Streams: Checked at stream.Err() points

#### OpenAI
- Detects: Handled internally by SDK
- Uses: SDK's built-in retry logic
- Custom: IsRetryable() returns false
- Fallback: Non-streaming mode for tool calls

#### Google Gemini
- Detects: HTTP 503, 429 via APIError.Code
- Uses: Parent exponential backoff
- Wrapping: APIError → formatted error message

#### Claude Code CLI
- Detects: Message pattern matching (rate_limit, timeout, etc.)
- Uses: Parent exponential backoff
- Special: exec.ErrNotFound → installation instruction

---

## 4. Fallback Mechanisms

### 4.1 Streaming → Buffered Mode Fallback

**Trigger Conditions:**
1. Memory pressure detected (OOM, allocation failure)
2. Stream protocol error requiring full retry
3. User explicitly requests buffered mode
4. OpenAI with tools (configured behavior)

**Implementation:**

```go
// OpenAI example
if len(tools) > 0 && c.disableStreaming {
    // Fall back to buffered non-streaming mode
    chatCompletion, err = c.queryWithoutStreaming(ctx, params, ...)
} else {
    // Try streaming first
    chatCompletion, err = c.queryWithStreaming(ctx, params, ...)
}

// General pattern for memory exhaustion
func (client *StreamingClient) SendQuery(ctx context.Context, ...) (*Response, error) {
    // Try streaming
    res, err := client.streamingQuery(ctx, ...)
    
    // Detect OOM and fall back
    if isOutOfMemory(err) {
        log.Warn("memory exhausted in streaming, switching to buffered mode")
        // Force buffered mode
        return client.bufferedQuery(ctx, ...)
    }
    
    return res, err
}
```

**Buffered Mode Characteristics:**
- Load entire response into memory
- Process after stream completes
- No early error detection
- Simpler accumulation logic
- Higher memory peak but no streaming overhead

### 4.2 Provider Fallback Chain

**Model Not Available:**
```
Requested Model
├─ Available? → Use it
└─ Not Available?
   ├─ Fallback Provider Configured?
   │  ├─ Yes → Switch provider, retry
   │  └─ No → Try default model
   └─ No default model?
      └─ Fail with helpful message
```

**Example Implementation:**

```go
// In router.go
func (r *LLMRouter) Route(model string) (*LLMEndpoint, error) {
    if model == "" {
        model = r.DefaultModel()
    }
    
    switch {
    case r.isAnthropicModel(model):
        endpoint = r.routeAnthropicModel()
    case r.isOpenAIModel(model):
        endpoint = r.routeOpenAIModel()
    case r.isGoogleModel(model):
        endpoint, err = r.routeGoogleModel()
        if err != nil {
            // Provider error - could fallback here
            return nil, fmt.Errorf("google provider failed: %w", err)
        }
    default:
        // Default to OpenAI compatible
        endpoint = r.routeOtherModel()
    }
    
    return endpoint, nil
}
```

### 4.3 Partial Result Preservation

**When Streaming Interrupted:**

```go
// Allow use of partial responses on interruption
func (llm *LLM) Sync(ctx context.Context) error {
    if err := llm.allowed(ctx); err != nil {
        return err
    }
    
    llm.once.Do(func() {
        err := llm.loop(ctx)
        
        // If context was cancelled/timeout, preserve partial results
        if err != nil && ctx.Err() == nil {
            // Actual error - not just interruption
            llm.err = err
        }
        // ctx.Err() != nil → treat as success, results are usable
    })
    
    return llm.err
}
```

**Use Cases:**
- Partial LLM response (useful for analysis)
- Tool calls made before interruption (preserve work)
- Last reply accessible via `llm.LastReply()`
- History accessible via `llm.History()`

### 4.4 Interactive Fallback (Auto-Interjection)

**When Model Finishes Without Tools:**

```go
func (llm *LLM) autoInterject(ctx context.Context) (bool, error) {
    if llm.mcp.IsDone() {
        return false, nil  // No interjection needed
    }
    
    query, err := CurrentQuery(ctx)
    if err != nil {
        return false, err
    }
    
    bk, err := query.Buildkit(ctx)
    if err != nil {
        return false, err
    }
    
    if !bk.Opts.Interactive {
        return false, nil  // Not interactive mode
    }
    
    // Prompt user for continuation
    if err := llm.Interject(ctx); err != nil {
        return false, err
    }
    
    return true, nil  // Interjection provided, continue loop
}
```

---

## 5. User-Facing Error Messages

### 5.1 Error Message Guidelines

**Principles:**
1. **Be specific**: Name the problem clearly
2. **Be actionable**: Suggest resolution steps
3. **Preserve context**: Include relevant details
4. **Be concise**: Don't overwhelm with information
5. **Avoid jargon**: Use understandable language

### 5.2 Error Message Catalog

#### Network Errors

**Rate Limiting**
```
Title: "Rate limited by LLM provider"
Message: "The API is responding too quickly. Retrying with backoff...
         (Attempt {current}/{max})"
Resolution: "Wait a moment and try again. If persistent, upgrade your plan."
```

**Connection Timeout**
```
Title: "Connection timeout"
Message: "Could not reach the LLM API within {timeout}s.
         Retrying with backoff... (Attempt {current}/{max})"
Resolution: "Check your network connection and ensure the endpoint URL is accessible."
```

**Service Unavailable**
```
Title: "LLM service temporarily unavailable"
Message: "The service returned HTTP 503. Retrying with backoff...
         (Attempt {current}/{max})"
Resolution: "The service may be undergoing maintenance. Try again later."
```

**Connection Refused**
```
Title: "Cannot connect to LLM service"
Message: "Connection refused to {endpoint}. The service may be offline or unreachable."
Resolution: "1. Verify the endpoint URL is correct
          2. Ensure network connectivity
          3. Check firewall rules
          4. Verify the service is running"
```

#### Authentication Errors

**Invalid API Key**
```
Title: "Invalid API credentials"
Message: "The API key for {provider} is invalid or expired."
Resolution: "1. Check your API key in environment variables
          2. Verify the key hasn't expired
          3. Generate a new key from {provider_portal}
          4. Update the ANTHROPIC_API_KEY (or equivalent) variable"
```

**Missing Credentials**
```
Title: "No credentials configured"
Message: "No API key found for any configured LLM provider."
Resolution: "Configure at least one provider:
          - export ANTHROPIC_API_KEY=sk-...
          - export OPENAI_API_KEY=sk-...
          - export GEMINI_API_KEY=..."
```

#### Resource Errors

**Out of Memory**
```
Title: "Memory exhausted (switching to buffered mode)"
Message: "Streaming mode ran out of memory. Automatically switching to buffered mode."
Resolution: "1. The operation will complete but may be slower
          2. If it fails, reduce input size or increase available memory
          3. Consider upgrading system resources"
```

**Too Many Open Files**
```
Title: "Resource limit exceeded"
Message: "Too many open file descriptors. Closing idle streams and retrying..."
Resolution: "1. The operation will be retried automatically
          2. If it persists, increase system file descriptor limit
          3. Check for resource leaks in your setup"
```

#### LLM-Specific Errors

**Model Not Found**
```
Title: "Model unavailable"
Message: "The model '{model}' is not available for your {provider} account."
Resolution: "1. Check available models: https://{provider}/models
          2. Your account may need to be upgraded
          3. Try a different model: {available_models}
          4. Contact {provider} support"
```

**Malformed Tool Call**
```
Title: "Tool invocation failed"
Message: "Tool '{tool_name}' failed: {error_detail}"
Resolution: "1. Check the tool definition and schema
          2. Verify the arguments match the expected types
          3. Review the LLM's tool call output
          4. Add input validation to tools if needed"
```

**Claude Code CLI Not Found**
```
Title: "Claude Code CLI not installed"
Message: "The 'claude' command was not found in PATH."
Resolution: "Install Claude Code CLI with:
          npm install -g @anthropic-ai/claude-code
          
         Verify installation with:
          claude --version"
```

#### Context/Cancellation Errors

**User Interrupted**
```
Title: "Operation interrupted"
Message: "The LLM operation was cancelled by user."
Details: "Partial results are available and may be useful:
        - Last reply: {last_reply}
        - Tool calls executed: {count}
        - History available via llm.history()"
Resolution: "You can access partial results or provide a new prompt to continue."
```

**Timeout**
```
Title: "Operation timeout"
Message: "The LLM operation exceeded the {timeout} timeout."
Details: "Partial results are available and may be useful:
        - Content accumulated: {bytes} bytes
        - Tool calls executed: {count}"
Resolution: "1. Increase timeout if the model needs more time
          2. Simplify the task
          3. Or use the partial results obtained"
```

### 5.3 Structured Error Wrapping

```go
// Custom error types for structured handling
type StreamError struct {
    Phase          string  // connection, streaming, finalization
    Provider       string  // anthropic, openai, google
    UnderlyingErr  error   // original error
    Retryable      bool    // is this retryable?
    UserMessage    string  // user-facing message
    Resolution     string  // suggested resolution steps
    RetryCount     int     // attempted retries
    ContextTimeoutOccurred bool
}

func (e *StreamError) Error() string {
    var sb strings.Builder
    fmt.Fprintf(&sb, "[%s] %s", e.Phase, e.UserMessage)
    if e.RetryCount > 0 {
        fmt.Fprintf(&sb, " (attempt %d)", e.RetryCount)
    }
    if e.Resolution != "" {
        fmt.Fprintf(&sb, "\n%s", e.Resolution)
    }
    if e.UnderlyingErr != nil && !isUserVisibleError(e.UnderlyingErr) {
        fmt.Fprintf(&sb, "\nDebug: %v", e.UnderlyingErr)
    }
    return sb.String()
}

// Example usage
return &StreamError{
    Phase:       "streaming",
    Provider:    "anthropic",
    UnderlyingErr: apiErr,
    Retryable:    true,
    UserMessage:  "Rate limited by LLM provider",
    Resolution:   "Retrying with backoff... (Attempt 1/5)",
    RetryCount:   1,
}
```

---

## 6. Cleanup Procedures

### 6.1 Resource Cleanup Patterns

#### Stream Cleanup

```go
// Pattern 1: Defer Close (Standard)
stream := c.client.Messages.NewStreaming(ctx, params)
defer stream.Close()

if err := stream.Err(); err != nil {
    return nil, err  // Defer will close stream
}

for stream.Next() {
    // Process
}

if err := stream.Err(); err != nil {
    return nil, err  // Defer will close stream
}

// Pattern 2: Conditional Close (Early Exit)
stream := c.client.Chat.Completions.NewStreaming(ctx, params)

if stream.Err() != nil {
    // Error before iteration - close before returning
    stream.Close()
    return nil, stream.Err()
}

defer stream.Close()  // Now safe to defer

// Pattern 3: Explicit Close (Error Path)
stream := c.client.GenerateContent.NewStreaming(ctx, config)

for res, err := range stream {
    if err != nil {
        // Close on error
        stream.Close()
        return nil, fmt.Errorf("stream error: %w", err)
    }
}
```

#### Connection Pool Cleanup

```go
// Close idle connections after error
func (c *StreamingClient) closeIdleConnections() {
    if c.httpClient != nil {
        c.httpClient.CloseIdleConnections()
    }
}

// Call on permanent error or shutdown
defer func() {
    if err != nil {
        c.closeIdleConnections()
    }
}()
```

#### Context Cleanup

```go
// Cancel child contexts on error
func (llm *LLM) loop(ctx context.Context) error {
    for {
        ctx, span := Tracer(ctx).Start(ctx, "LLM query")
        res, err := client.SendQuery(ctx, messages, tools)
        telemetry.End(span, func() error { return err })
        
        // Span automatically closed and canceled
        if err != nil {
            // Child context is cleaned up
            return err
        }
    }
}
```

### 6.2 Cleanup on Different Error Paths

#### Normal Completion
```
Stream iteration → stream.Close() via defer ✓
Parse response ✓
Return result ✓
```

#### Stream Error (Retryable)
```
Stream error detected
├─ stream.Close() via defer ✓
├─ Mark for retry
├─ Release event buffer ✓
└─ Return error (will be retried)
```

#### Stream Error (Non-Retryable)
```
Stream error detected
├─ stream.Close() via defer ✓
├─ Log error details ✓
├─ Release event buffer ✓
├─ Close any child connections ✓
└─ Return wrapped error
```

#### Context Cancellation
```
Context cancelled
├─ stream.Close() via defer ✓
├─ Mark as user-interrupted
├─ Preserve partial results ✓
├─ Close child contexts ✓
└─ Return nil (preserve results)
```

#### Memory Exhaustion
```
OOM detected
├─ stream.Close() via defer ✓
├─ Release accumulator ✓
├─ Clear event buffer ✓
├─ Signal fallback to buffered mode
└─ Trigger garbage collection
```

### 6.3 Cleanup Checklist for New Stream Operations

When implementing a new streaming client:

- [ ] Always wrap stream in `defer stream.Close()`
- [ ] Check `stream.Err()` before iteration
- [ ] Check `stream.Err()` after iteration loop
- [ ] Release event accumulator on error
- [ ] Cancel child contexts on cleanup
- [ ] Close HTTP connections on permanent error
- [ ] Flush pending telemetry data
- [ ] Log cleanup actions for debugging
- [ ] Test cleanup in error paths
- [ ] Verify no goroutine leaks

---

## 7. Code Structure for Error Handling (Go Patterns)

### 7.1 Client Interface Definition

```go
// LLMClient defines the interface all LLM providers must implement
type LLMClient interface {
    // SendQuery sends a query and handles streaming
    SendQuery(ctx context.Context, history []*ModelMessage, tools []LLMTool) (*LLMResponse, error)
    
    // IsRetryable determines if an error should be retried
    IsRetryable(err error) bool
}

// Endpoint holds provider configuration
type LLMEndpoint struct {
    Model    string
    BaseURL  string
    Key      string
    Provider LLMProvider
    Client   LLMClient
}
```

### 7.2 Error Classification Helpers

```go
// Error classification utilities
func isTransientError(err error) bool {
    switch {
    case err == nil:
        return false
    case strings.Contains(err.Error(), "rate_limit"):
        return true
    case strings.Contains(err.Error(), "timeout"):
        return true
    case strings.Contains(err.Error(), "temporarily unavailable"):
        return true
    case strings.Contains(err.Error(), "connection reset"):
        return true
    default:
        return false
    }
}

func isPermanentError(err error) bool {
    switch {
    case errors.Is(err, context.Canceled):
        return true  // User cancelled
    case errors.Is(err, context.DeadlineExceeded):
        return true  // Timeout
    case strings.Contains(err.Error(), "invalid api key"):
        return true
    case strings.Contains(err.Error(), "forbidden"):
        return true
    case strings.Contains(err.Error(), "not found"):
        return true
    default:
        return false
    }
}

func isMemoryError(err error) bool {
    return strings.Contains(err.Error(), "out of memory") ||
           strings.Contains(err.Error(), "no memory") ||
           strings.Contains(err.Error(), "allocation failed")
}

// Context-aware error checking
func isContextCancelled(err error) bool {
    return errors.Is(err, context.Canceled)
}

func isContextTimeout(err error) bool {
    return errors.Is(err, context.DeadlineExceeded)
}

func isContextError(err error) bool {
    return isContextCancelled(err) || isContextTimeout(err)
}
```

### 7.3 Retry Logic Pattern

```go
// RetryConfig encapsulates retry behavior
type RetryConfig struct {
    InitialInterval time.Duration
    MaxInterval     time.Duration
    MaxElapsedTime  time.Duration
}

// WithRetry wraps an operation with exponential backoff
func WithRetry(ctx context.Context, config RetryConfig, op func(context.Context) error) error {
    b := backoff.NewExponentialBackOff()
    b.InitialInterval = config.InitialInterval
    b.MaxInterval = config.MaxInterval
    b.MaxElapsedTime = config.MaxElapsedTime
    
    return backoff.Retry(func() error {
        err := op(ctx)
        
        // Don't retry certain errors
        if err == nil {
            return nil
        }
        
        if isContextError(err) {
            return backoff.Permanent(err)
        }
        
        if isPermanentError(err) {
            return backoff.Permanent(err)
        }
        
        // Retryable error - backoff will retry
        return err
    }, backoff.WithContext(b, ctx))
}

// Usage
err := WithRetry(ctx, RetryConfig{
    InitialInterval: 1 * time.Second,
    MaxInterval:     30 * time.Second,
    MaxElapsedTime:  2 * time.Minute,
}, func(ctx context.Context) error {
    return client.SendQuery(ctx, messages, tools)
})
```

### 7.4 Stream Error Handling Template

```go
// Template for implementing streaming clients
type CustomClient struct {
    client   *CustomSDKClient
    endpoint *LLMEndpoint
}

var _ LLMClient = (*CustomClient)(nil)

func (c *CustomClient) IsRetryable(err error) bool {
    // Implement provider-specific retry logic
    if err == nil {
        return false
    }
    
    msg := err.Error()
    retryablePatterns := []string{
        "rate_limit", "timeout", "503", "429",
        "temporarily unavailable", "service overloaded",
    }
    
    for _, pattern := range retryablePatterns {
        if strings.Contains(msg, pattern) {
            return true
        }
    }
    
    return false
}

func (c *CustomClient) SendQuery(
    ctx context.Context,
    history []*ModelMessage,
    tools []LLMTool,
) (res *LLMResponse, rerr error) {
    // Setup telemetry
    stdio := telemetry.SpanStdio(ctx, InstrumentationLibrary)
    defer stdio.Close()
    
    // Convert input
    params, err := c.convertParams(history, tools)
    if err != nil {
        return nil, fmt.Errorf("failed to convert params: %w", err)
    }
    
    // Create stream
    stream := c.client.NewStreaming(ctx, params)
    
    // Check early errors (before iteration)
    if stream.Err() != nil {
        return nil, fmt.Errorf("stream setup failed: %w", stream.Err())
    }
    defer stream.Close()
    
    // Accumulate response
    acc := new(ResponseAccumulator)
    
    for stream.Next() {
        event := stream.Current()
        
        // Process event
        if err := c.processEvent(event, acc); err != nil {
            return nil, fmt.Errorf("event processing failed: %w", err)
        }
        
        // Stream telemetry
        if text := event.TextDelta(); text != "" {
            fmt.Fprint(stdio.Stdout, text)
        }
    }
    
    // Check stream completion error
    if err := stream.Err(); err != nil {
        return nil, fmt.Errorf("stream error: %w", err)
    }
    
    // Validate accumulated response
    if acc.IsEmpty() {
        return nil, &ModelFinishedError{Reason: "empty response"}
    }
    
    // Convert to generic format
    return c.convertResponse(acc), nil
}

// Helper method
func (c *CustomClient) processEvent(event *StreamEvent, acc *ResponseAccumulator) error {
    // Process different event types
    switch {
    case event.IsContent():
        acc.AddContent(event.Content())
    case event.IsToolCall():
        toolCall, err := c.parseToolCall(event)
        if err != nil {
            return fmt.Errorf("invalid tool call: %w", err)
        }
        acc.AddToolCall(toolCall)
    case event.IsUsage():
        acc.SetTokenUsage(event.Usage())
    }
    return nil
}
```

### 7.5 Fallback Handler Pattern

```go
// StreamingFallbackHandler manages fallback between streaming and buffered modes
type StreamingFallbackHandler struct {
    primaryClient   LLMClient  // Streaming client
    fallbackClient  LLMClient  // Buffered client
    logger          Logger
}

func (h *StreamingFallbackHandler) Send(
    ctx context.Context,
    history []*ModelMessage,
    tools []LLMTool,
) (*LLMResponse, error) {
    // Try primary (streaming)
    res, err := h.primaryClient.SendQuery(ctx, history, tools)
    
    // Detect fallback triggers
    if err != nil {
        if isMemoryError(err) {
            h.logger.Warn("memory exhausted in streaming, falling back to buffered mode")
            return h.fallbackClient.SendQuery(ctx, history, tools)
        }
        
        if shouldFallbackForToolCalls(tools, err) {
            h.logger.Debug("falling back to buffered mode for tool calls")
            return h.fallbackClient.SendQuery(ctx, history, tools)
        }
        
        return nil, err
    }
    
    return res, nil
}

// Helpers
func isMemoryError(err error) bool {
    return strings.Contains(err.Error(), "out of memory") ||
           strings.Contains(err.Error(), "allocation failed")
}

func shouldFallbackForToolCalls(tools []LLMTool, err error) bool {
    // Some providers require buffered mode for tool calls
    // Check configuration or error signal
    return false  // Provider-specific
}
```

### 7.6 Context-Aware Error Handling

```go
// ContextAwareErrorHandler adds context information to errors
type ContextAwareErrorHandler struct {
    operationName string
    startTime     time.Time
    attempt       int
}

func (h *ContextAwareErrorHandler) WrapError(err error) error {
    if err == nil {
        return nil
    }
    
    elapsed := time.Since(h.startTime)
    
    // Preserve context-cancelled/deadline-exceeded
    if isContextError(err) {
        return err  // Let context errors propagate unchanged
    }
    
    // Wrap other errors with context
    return fmt.Errorf(
        "[%s] operation failed after %v (attempt %d): %w",
        h.operationName, elapsed, h.attempt, err,
    )
}

// Usage
handler := &ContextAwareErrorHandler{
    operationName: "stream_query",
    startTime:     time.Now(),
    attempt:       1,
}

res, err := client.SendQuery(ctx, history, tools)
if err != nil {
    return nil, handler.WrapError(err)
}
```

### 7.7 Logging for Error Debugging

```go
// StructuredErrorLogging provides context-rich logging
func logStreamError(ctx context.Context, err error, phase string, provider string) {
    if err == nil {
        return
    }
    
    logger := slog.FromContext(ctx)
    
    logAttrs := []slog.Attr{
        slog.String("phase", phase),
        slog.String("provider", provider),
        slog.String("error_type", fmt.Sprintf("%T", err)),
    }
    
    // Add context info
    if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
        logAttrs = append(logAttrs,
            slog.String("trace_id", span.SpanContext().TraceID().String()),
            slog.String("span_id", span.SpanContext().SpanID().String()),
        )
    }
    
    // Determine log level
    if isContextError(err) {
        logger.Debug("stream operation cancelled", logAttrs...)
    } else if isPermanentError(err) {
        logger.Error("permanent stream error", logAttrs...)
    } else {
        logger.Warn("transient stream error (retrying)", logAttrs...)
    }
}

// Usage
if err := stream.Err(); err != nil {
    logStreamError(ctx, err, "streaming", "anthropic")
    return nil, err
}
```

### 7.8 Testing Error Handling

```go
// Error handling test patterns
func TestStreamErrorHandling(t *testing.T) {
    tests := []struct {
        name          string
        error         error
        isRetryable   bool
        isContext     bool
        isPermanent   bool
        expectedFallback bool
    }{
        {
            name:          "rate limit",
            error:         fmt.Errorf("rate_limit_error"),
            isRetryable:   true,
            isContext:     false,
            isPermanent:   false,
            expectedFallback: false,
        },
        {
            name:          "invalid api key",
            error:         fmt.Errorf("invalid api key"),
            isRetryable:   false,
            isContext:     false,
            isPermanent:   true,
            expectedFallback: false,
        },
        {
            name:          "out of memory",
            error:         fmt.Errorf("out of memory"),
            isRetryable:   false,
            isContext:     false,
            isPermanent:   false,
            expectedFallback: true,
        },
        {
            name:          "context cancelled",
            error:         context.Canceled,
            isRetryable:   false,
            isContext:     true,
            isPermanent:   true,
            expectedFallback: false,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            client := &TestClient{}
            
            assert.Equal(t, tt.isRetryable, client.IsRetryable(tt.error))
            assert.Equal(t, tt.isContext, isContextError(tt.error))
            assert.Equal(t, tt.isPermanent, isPermanentError(tt.error))
        })
    }
}
```

---

## 8. Implementation Checklist

### 8.1 For New Streaming Clients

- [ ] Implement `LLMClient` interface
  - [ ] `SendQuery(ctx, history, tools)` method
  - [ ] `IsRetryable(err)` method
  
- [ ] Set up streaming properly
  - [ ] Create stream with context
  - [ ] Check initial error before iteration
  - [ ] Use `defer stream.Close()` for cleanup
  - [ ] Check final error after iteration
  
- [ ] Add telemetry
  - [ ] Output writer for live text
  - [ ] Token usage tracking
  - [ ] Span creation/completion
  - [ ] Error logging
  
- [ ] Handle provider-specific errors
  - [ ] Document retryable errors
  - [ ] Implement detection logic
  - [ ] Add test cases
  
- [ ] Implement data conversion
  - [ ] History → provider format
  - [ ] Tools → provider format
  - [ ] Response → generic LLMResponse
  - [ ] Error handling for conversion failures

- [ ] Test error paths
  - [ ] Transient errors
  - [ ] Permanent errors
  - [ ] Malformed responses
  - [ ] Context cancellation
  - [ ] Stream closure

### 8.2 For Error Handling Integration

- [ ] Retry logic with backoff
  - [ ] Configure backoff parameters
  - [ ] Test retry decision logic
  - [ ] Verify max attempt limits
  
- [ ] Error wrapping
  - [ ] Include context information
  - [ ] Preserve error chain
  - [ ] Add user-friendly messages
  
- [ ] Resource cleanup
  - [ ] Defer stream.Close()
  - [ ] Close HTTP connections
  - [ ] Cancel child contexts
  - [ ] Flush telemetry
  
- [ ] Fallback mechanisms
  - [ ] Streaming → buffered detection
  - [ ] Provider fallback setup
  - [ ] Partial result preservation
  
- [ ] Logging and debugging
  - [ ] Structured error logs
  - [ ] Telemetry integration
  - [ ] Trace/span creation
  - [ ] Error categorization

### 8.3 User-Facing Error Messages

- [ ] Review error message text
  - [ ] Is it specific? (not generic)
  - [ ] Is it actionable? (steps to resolve)
  - [ ] Is it understandable? (no jargon)
  
- [ ] Test error scenarios
  - [ ] Missing credentials
  - [ ] Invalid endpoint
  - [ ] Network failure
  - [ ] Permission error
  - [ ] Resource exhaustion
  
- [ ] Documentation
  - [ ] Common errors documented
  - [ ] Resolution steps provided
  - [ ] Examples for configuration
  - [ ] Troubleshooting guide

---

## 9. Example: Complete Error-Aware Client

```go
package core

import (
    "context"
    "errors"
    "fmt"
    "strings"
    "time"
    
    "dagger.io/dagger/telemetry"
    "github.com/cenkalti/backoff/v4"
)

// ExampleStreamingClient implements error-aware streaming
type ExampleStreamingClient struct {
    endpoint *LLMEndpoint
    config   *StreamingConfig
}

type StreamingConfig struct {
    RetryMaxAttempts int
    RetryInitialDelay time.Duration
    RetryMaxDelay time.Duration
    BufferedFallback bool
}

func (c *ExampleStreamingClient) IsRetryable(err error) bool {
    if err == nil {
        return false
    }
    
    // Context errors are not retryable
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return false
    }
    
    msg := err.Error()
    retryablePatterns := []string{
        "rate_limit", "429",
        "overloaded", "503",
        "timeout", "temporary",
        "connection reset",
    }
    
    for _, pattern := range retryablePatterns {
        if strings.Contains(msg, pattern) {
            return true
        }
    }
    
    return false
}

func (c *ExampleStreamingClient) SendQuery(
    ctx context.Context,
    history []*ModelMessage,
    tools []LLMTool,
) (*LLMResponse, error) {
    // Setup telemetry
    stdio := telemetry.SpanStdio(ctx, InstrumentationLibrary)
    defer stdio.Close()
    
    // Phase 1: Validate inputs
    if len(history) == 0 {
        return nil, fmt.Errorf("message history cannot be empty")
    }
    
    // Phase 2: Convert to provider format
    params, err := c.prepareParams(history, tools)
    if err != nil {
        return nil, fmt.Errorf("failed to prepare request: %w", err)
    }
    
    // Phase 3: Create stream with early error detection
    stream := c.createStream(ctx, params)
    
    // Critical: check stream establishment error
    if err := stream.Err(); err != nil {
        return nil, fmt.Errorf("failed to establish stream: %w", err)
    }
    defer stream.Close()
    
    // Phase 4: Accumulate response
    acc := NewResponseAccumulator()
    
    for stream.Next() {
        event := stream.Current()
        
        // Process event with error handling
        if err := c.processEvent(event, acc); err != nil {
            // Log but don't fail immediately - might be partial event
            fmt.Fprintf(stdio.Stderr, "warning: event processing error: %v\n", err)
            continue
        }
        
        // Output streaming text
        if text := extractText(event); text != "" {
            fmt.Fprint(stdio.Stdout, text)
        }
    }
    
    // Critical: check stream completion error
    if err := stream.Err(); err != nil {
        return nil, fmt.Errorf("stream interrupted: %w", err)
    }
    
    // Phase 5: Validate response
    if acc.IsEmpty() {
        return nil, &ModelFinishedError{Reason: "no content in response"}
    }
    
    // Phase 6: Convert and return
    return c.buildResponse(acc), nil
}

// Helper methods
func (c *ExampleStreamingClient) createStream(ctx context.Context, params interface{}) Stream {
    // Implementation-specific stream creation
    // Would use actual SDK here
    panic("implement")
}

func (c *ExampleStreamingClient) processEvent(event Event, acc *ResponseAccumulator) error {
    // Safe processing with error wrapping
    switch {
    case event.Type() == "content_block_delta":
        acc.AddContent(event.Content())
    case event.Type() == "tool_use":
        toolCall, err := parseToolCall(event)
        if err != nil {
            return fmt.Errorf("invalid tool call in event: %w", err)
        }
        acc.AddToolCall(toolCall)
    case event.Type() == "usage":
        acc.SetTokenUsage(event.Usage())
    }
    return nil
}

func (c *ExampleStreamingClient) prepareParams(history []*ModelMessage, tools []LLMTool) (interface{}, error) {
    if len(history) == 0 {
        return nil, errors.New("history cannot be empty")
    }
    // Convert...
    panic("implement")
}

func (c *ExampleStreamingClient) buildResponse(acc *ResponseAccumulator) *LLMResponse {
    panic("implement")
}

// Types
type Stream interface {
    Next() bool
    Current() Event
    Err() error
    Close() error
}

type Event interface {
    Type() string
    Content() string
    Usage() *TokenUsage
}

type ResponseAccumulator struct {
    content string
    toolCalls []LLMToolCall
    tokenUsage LLMTokenUsage
}

func NewResponseAccumulator() *ResponseAccumulator {
    return &ResponseAccumulator{}
}

func (a *ResponseAccumulator) AddContent(content string) {
    a.content += content
}

func (a *ResponseAccumulator) AddToolCall(tc LLMToolCall) {
    a.toolCalls = append(a.toolCalls, tc)
}

func (a *ResponseAccumulator) SetTokenUsage(tu *TokenUsage) {
    a.tokenUsage = LLMTokenUsage{
        InputTokens: tu.InputTokens,
        OutputTokens: tu.OutputTokens,
    }
}

func (a *ResponseAccumulator) IsEmpty() bool {
    return a.content == "" && len(a.toolCalls) == 0
}

// Helper functions
func extractText(event Event) string {
    if event.Type() == "content_block_delta" {
        return event.Content()
    }
    return ""
}

func parseToolCall(event Event) (LLMToolCall, error) {
    // Parse tool call from event
    panic("implement")
}

type TokenUsage struct {
    InputTokens  int64
    OutputTokens int64
}
```

---

## 10. Monitoring and Observability

### 10.1 Key Metrics to Track

```
Metric: llm.request.duration_ms
- Labels: provider, model, phase (connection|streaming|total)
- Purpose: Detect slow operations and bottlenecks

Metric: llm.request.retries
- Labels: provider, model, error_type
- Purpose: Identify problematic providers or error patterns

Metric: llm.input_tokens, llm.output_tokens
- Labels: provider, model
- Purpose: Cost and usage tracking

Metric: llm.streaming.errors
- Labels: provider, model, error_category (transient|permanent|context)
- Purpose: Identify error trends

Metric: llm.fallback.triggered
- Labels: provider, model, fallback_type (buffered|provider_switch)
- Purpose: Track fallback frequency and effectiveness
```

### 10.2 Logging Strategy

```
Log Level: ERROR
- Permanent errors (auth, invalid request)
- Unrecoverable failures
- Should create alerts

Log Level: WARN
- Transient errors before retry
- Fallback activations
- Resource pressure

Log Level: INFO
- Operation start/completion
- Retry count summary
- Fallback details

Log Level: DEBUG
- Stream events
- Event accumulation
- Detailed timing
- Full error chains
```

### 10.3 Debugging Checklist

When troubleshooting streaming errors:

1. **Check error classification**
   - Is it retryable or permanent?
   - Is it context-related?
   - Provider-specific patterns?

2. **Review telemetry**
   - Trace context (trace ID, span ID)
   - Timing information
   - Token usage

3. **Inspect stream lifecycle**
   - Stream creation error?
   - Early vs late error?
   - Complete iteration?

4. **Verify resource state**
   - File descriptor count?
   - Memory usage?
   - Connection pool status?

5. **Check configuration**
   - Correct API key?
   - Valid endpoint URL?
   - Model available?
   - Credentials not expired?

---

## Appendix: Error Reference Quick Guide

| Scenario | Error Type | Action | Fallback |
|---|---|---|---|
| API returns 429 | Transient | Retry with backoff | None |
| API returns 503 | Transient | Retry with backoff | Provider switch |
| API returns 401 | Permanent | Fail, check credentials | None |
| API returns 400 | Permanent | Fail, check request | None |
| Network timeout | Transient | Retry with backoff | Longer timeout |
| Connection refused | Transient | Retry with backoff | Check endpoint |
| DNS failure | Permanent | Fail | Check URL |
| Out of memory | Transient | Switch to buffered | Increase memory |
| Stream drops | Transient | Retry full request | Buffer mode |
| Invalid tool call | Permanent | Return error to LLM | Manual intervention |
| User cancels | Expected | Preserve results | Use partial output |
| Timeout | Expected | Preserve results | Extend timeout |

---

## Document Metadata

- **Version**: 1.0
- **Last Updated**: 2024
- **Author**: Dagger Team
- **Scope**: LLM Streaming Operations
- **Related Components**: llm.go, llm_*.go clients, backoff retry logic, telemetry system

