# Streaming Error Handling: Quick Reference Guide

## Error Decision Tree (Quick Version)

```
Error occurs
├─ Context error? (Canceled/Deadline)
│  └─ Return as-is, preserve results
├─ IsRetryable() returns true?
│  ├─ Exponential backoff: 1s → 30s over 2m
│  └─ Max 5 attempts
├─ IsRetryable() returns false?
│  ├─ Auth/Permission error?
│  │  └─ Fail immediately with user message
│  ├─ Permanent error?
│  │  └─ Fail immediately
│  └─ Unknown?
│     └─ Fail immediately
└─ Log classification for debugging
```

## Error Types and Actions (One-Page Reference)

| Error Category | Examples | Retryable | Fallback | User Message |
|---|---|---|---|---|
| **Network** | timeout, connection refused, DNS fail | Yes | None | "Network error. Retrying..." |
| **Rate Limit** | 429, rate_limit, quota | Yes | None | "Rate limited. Retrying..." |
| **Auth** | 401, invalid key, forbidden | No | None | "Invalid credentials. Check API key." |
| **Resource** | OOM, too many files | No* | Buffered mode | "Switching to buffered mode..." |
| **Service Down** | 503, unavailable, overloaded | Yes | None | "Service unavailable. Retrying..." |
| **Stream Protocol** | Connection drops, malformed events | Yes | Retry request | "Stream error. Retrying..." |
| **Context** | Canceled, Deadline exceeded | No | Preserve results | "Operation interrupted." |
| **Model Finish** | Stop reason, no tools | No | Interactive prompt | "Ready for next input." |

*Can trigger fallback

## Provider-Specific Checklist

### Anthropic (`llm_anthropic.go`)
- [x] defer stream.Close()
- [x] Check stream.Err() before iteration
- [x] Check stream.Err() after iteration
- [x] IsRetryable() checks patterns
- [x] Handle empty content (space char fix)

### OpenAI (`llm_openai.go`)
- [x] defer stream.Close()
- [x] Check stream.Err() before iteration
- [x] Check stream.Err() after iteration
- [x] Fallback to non-streaming with tools
- [x] IsRetryable() delegates to SDK

### Google (`llm_google.go`)
- [x] Iterator pattern (for range)
- [x] APIError wrapping
- [x] IsRetryable() checks HTTP codes
- [x] Empty response handling

### Claude Code CLI (`llm_claude_code.go`)
- [x] Pattern matching for retryable errors
- [x] exec.ErrNotFound detection
- [x] JSON response validation
- [x] Error flag checking

### Main Loop (`llm.go`)
- [x] Exponential backoff setup
- [x] ModelFinishedError handling
- [x] IsRetryable() check before giving up
- [x] Partial result preservation

## Code Snippets for Common Scenarios

### Detect Retryable Error

```go
if client.IsRetryable(err) {
    // Will be retried with backoff
    return err
} else {
    return backoff.Permanent(err)  // Give up now
}
```

### Wrap Stream Safely

```go
stream := c.client.NewStreaming(ctx, params)
defer stream.Close()           // Always runs

if stream.Err() != nil {       // Check before iteration
    return nil, stream.Err()   // Defer will close
}

for stream.Next() {
    // Process...
}

if stream.Err() != nil {       // Check after iteration
    return nil, stream.Err()   // Defer will close
}
```

### Fallback Pattern

```go
res, err := streamingClient.SendQuery(ctx, history, tools)
if isMemoryError(err) {
    // Fall back to buffered mode
    return bufferedClient.SendQuery(ctx, history, tools)
}
return res, err
```

### Log and Classify

```go
if err != nil {
    if isAuthError(err) {
        logger.Error("authentication failed", "error", err)
    } else if isNetworkError(err) {
        logger.Warn("network error (retrying)", "error", err)
    } else if isContextError(err) {
        logger.Info("operation cancelled", "error", err)
    } else {
        logger.Error("unexpected error", "error", err)
    }
}
```

## Troubleshooting Flowchart

```
User reports error
├─ Read error message
├─ Is it specific and actionable?
│  ├─ Yes → User can fix it
│  └─ No → Check logs with trace ID
├─ Check application logs
│  ├─ "attempt N/5" → Transient, will retry
│  ├─ "not retrying" → Permanent error
│  └─ "switched to buffered" → Memory fallback
├─ Check provider status
│  ├─ Service down? → Temporary
│  └─ Normal? → Check credentials
└─ Check configuration
   ├─ API key valid? (expires?)
   ├─ Endpoint URL correct?
   ├─ Model available for account?
   └─ Rate limits within plan?
```

## Key Metrics to Monitor

```
llm.request.duration_ms
  -> High latency + retries = network issues or rate limiting

llm.request.retries
  -> Increasing = transient errors growing

llm.streaming.errors (by error_category)
  -> transient: normal, managed by retry logic
  -> permanent: investigate
  -> context: expected, user control

llm.fallback.triggered
  -> memory_exhaustion: may need bigger resources
  -> provider_switch: primary provider failing
```

## Retry Backoff Math

```
Initial: 1 second
After attempt 1: ~2 seconds (2^1)
After attempt 2: ~4 seconds (2^2)
After attempt 3: ~8 seconds (2^3)
After attempt 4: ~16 seconds (2^4)
After attempt 5: ~30 seconds (maxed)

Total time: ~61 seconds (includes jitter)
Max total time: 2 minutes before giving up
```

## Test Scenarios to Verify

1. **Happy Path**
   - Request succeeds on first try
   - No retries, no fallback

2. **Transient Failure (Retryable)**
   - First attempt fails with 429
   - Retries with backoff
   - Eventually succeeds

3. **Permanent Failure (Non-Retryable)**
   - Request fails with 401
   - No retries
   - Fails immediately with clear message

4. **Memory Exhaustion**
   - Streaming hits OOM
   - Falls back to buffered mode
   - Completes successfully

5. **Context Cancellation**
   - User cancels mid-stream
   - Preserves partial results
   - Results accessible

6. **Provider Error**
   - SDK returns specific error type
   - Correctly classified
   - Right action taken

## Configuration Environment Variables

```bash
# Credentials (required for any operation)
export ANTHROPIC_API_KEY=sk-...
export OPENAI_API_KEY=sk-...
export GEMINI_API_KEY=...

# Endpoints (optional, defaults provided)
export ANTHROPIC_BASE_URL=https://api.anthropic.com
export OPENAI_BASE_URL=https://api.openai.com/v1
export GEMINI_BASE_URL=https://...

# Feature flags
export OPENAI_DISABLE_STREAMING=false  # Use buffered mode
export CLAUDE_CODE_MODEL=sonnet

# Logging (for debugging)
export LOG_LEVEL=debug  # More detailed logs
```

## Common Error Messages and Fixes

```
"Rate limited by LLM provider"
→ Solution: Wait, upgrade plan, or reduce frequency

"Invalid API credentials"
→ Solution: Check API key, regenerate, update env vars

"Cannot connect to LLM service"
→ Solution: Check network, verify endpoint URL, check firewall

"Memory exhausted (switching to buffered mode)"
→ Solution: Operation continues but slower; increase memory if persistent

"Operation interrupted"
→ Solution: User action; access partial results if useful

"LLM service temporarily unavailable"
→ Solution: Wait, service is under maintenance; will retry automatically

"Claude Code CLI not found"
→ Solution: npm install -g @anthropic-ai/claude-code

"Model not available"
→ Solution: Check account permissions, upgrade plan, try different model
```

## Files Modified by Error Handling Strategy

```
core/llm.go
  ├─ Add error classification helpers
  ├─ Add StreamError type
  ├─ Update retry loop
  ├─ Add error message derivation
  └─ Add logging

core/llm_anthropic.go
  ├─ Add debug logging
  └─ Enhance error messages

core/llm_openai.go
  ├─ Add debug logging
  ├─ Document fallback behavior
  └─ Enhance error messages

core/llm_google.go
  ├─ Add debug logging
  └─ Enhance APIError wrapping

core/llm_claude_code.go
  ├─ Better CLI error detection
  ├─ Add debug logging
  └─ Enhance error messages

docs/
  ├─ STREAMING_ERROR_HANDLING_STRATEGY.md (new)
  ├─ IMPLEMENTATION_GUIDE.md (new)
  └─ QUICK_REFERENCE.md (this file)
```

## For Operators: Debugging Checklist

When a user reports LLM operation failure:

- [ ] Get error message and trace ID
- [ ] Check application logs: `grep <trace-id> logs/`
- [ ] Is it marked "retrying"?
  - Yes: Transient, likely to succeed eventually
  - No: Permanent, needs user action
- [ ] Check "attempt N/M"
  - Few attempts: May retry more
  - Max attempts: Final failure
- [ ] Check provider status page
  - If down: Inform user to wait
  - If up: Check configuration
- [ ] Verify credentials
  - Try with test request
  - Check expiration date
- [ ] Verify model availability
  - List available models
  - Check account permissions
- [ ] Check resource usage
  - Memory: May need to switch to buffered mode
  - Connections: May need to close idle connections
- [ ] If still stuck
  - Enable debug logging: `LOG_LEVEL=debug`
  - Reproduce issue
  - Share logs with team

## Performance Impact Summary

| Component | Impact | Notes |
|---|---|---|
| Error classification | Negligible | Lightweight string matching |
| Structured errors | <1KB per error | Minimal memory overhead |
| Logging | Controlled | Disabled at info level by default |
| Retry backoff | 1-30s waits | Dominated by network, not overhead |
| Fallback switching | ~5% slower | Acceptable for graceful degradation |

**Conclusion**: No meaningful performance impact on success path.

