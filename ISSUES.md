# Code Review: Last Two Commits

Reviewed commits:
- `26c0371` Port pi-ai core to Go pkg/ai
- `c10eb99` Build agent core package

Tests pass, `go vet` is clean. Issues below are sorted roughly by severity.

---

## Critical

### 1. Race condition / panic in `AssistantMessageEventStream.Push`

**File:** `pkg/ai/event_stream.go:38-67`

`Push()` releases the mutex before writing to the channel. Between the unlock and the channel send, another goroutine can call `End()` or another `Push(EventDone)`, which calls `closeOnce()` and closes the underlying channel. Sending to a closed channel panics in Go, and the `default` case in the `select` does not prevent this.

```
Goroutine A: Push(textDelta) -> lock, done=false, unlock -> about to write
Goroutine B: Push(EventDone) -> lock, done=true, unlock -> writes -> closeOnce() closes channel
Goroutine A: select { case events <- event: } -> PANIC: send on closed channel
```

If the stream is strictly single-writer (one goroutine), this is mitigated in practice but not by the API contract. Nothing prevents concurrent Push calls, and the mutex usage implies multi-writer safety was intended.

**Fix:** either hold the lock during the channel write, use a recover guard, or document and enforce single-writer semantics.

### 2. Silent event dropping under backpressure

**File:** `pkg/ai/event_stream.go:58-63`

When the channel buffer (256) is full, events are silently dropped via the `default` case. This includes potentially critical events like `EventToolCallEnd`. Consumers of the stream have no way to know events were lost, which can cause `RunTurn` to miss tool calls entirely, producing incorrect behavior with no error.

**Fix:** consider blocking (with context cancellation), growing the buffer dynamically, or at minimum emitting a warning/error event when drops occur.

---

## High

### 3. `CalculateCost` mutates its input

**File:** `pkg/ai/models.go:132-142`

`CalculateCost(model, usage)` writes directly into `usage.Cost.*` fields and then returns `usage.Cost`. It both mutates the input and returns a value. Callers may not expect their `Usage` struct to be modified as a side effect. This is a footgun.

**Fix:** compute into a local `CostBreakdown` and return it, or rename to `ApplyCost` to make the mutation explicit.

### 4. Symlink bypass in filesystem policy

**File:** `pkg/agentcore/tool_runner.go:167-176`

`isWithinRoot` uses `filepath.Rel` to check path containment but never resolves symlinks. A symlink inside the workspace (e.g., `workspace/link -> /etc`) would pass the check and allow reading/writing outside the allowed roots.

**Fix:** use `filepath.EvalSymlinks` on the resolved path before the containment check.

### 5. No output size limits on `shell.exec` and `fs.read`

**Files:** `pkg/agentcore/tool_shell.go:63-66`, `pkg/agentcore/tool_fs.go:35`

`shell.exec` captures all stdout/stderr into unbounded `bytes.Buffer`s. `fs.read` uses `os.ReadFile` which loads the entire file into memory. A malicious or buggy tool call (e.g., `cat /dev/urandom` or reading a multi-GB file) can exhaust memory.

**Fix:** cap output buffers (e.g., `io.LimitReader`) and return a truncation notice.

### 6. Enum validation is a no-op

**File:** `pkg/ai/validation.go:38-44`

The enum check iterates over candidates and breaks on match, but never returns an error when no value matches. The for loop exits silently for invalid values. Enums are effectively not enforced.

```go
if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
    for _, candidate := range enumValues {
        if fmt.Sprint(candidate) == fmt.Sprint(value) {
            break  // found it, but then what? falls through to type check
        }
    }
    // no error if nothing matched
}
```

**Fix:** track whether a match was found and return a validation error if not.

---

## Medium

### 7. `nhooyr.io/websocket` incorrectly marked as indirect

**File:** `go.mod:5`

The websocket package is directly imported by `pkg/ai/provider_openai_codex_responses.go` but is marked `// indirect` in `go.mod`. This is semantically wrong and may confuse dependency tooling.

**Fix:** remove the `// indirect` comment.

### 8. `boundMessages` truncation can split tool call/result pairs

**File:** `pkg/agentcore/session.go:20-30`

Naive tail truncation takes the last N messages. This can split an assistant message containing tool calls from the corresponding tool result messages, producing an invalid conversation that will confuse providers.

**Fix:** truncate at conversation-safe boundaries (e.g., never split a tool call from its results).

### 9. No context cancellation check between tool rounds

**File:** `pkg/agentcore/run_turn.go:35-140`

The `RunTurn` loop iterates up to `MaxToolRounds` times but never checks `ctx.Err()` between the end of tool execution and the start of the next provider call. If the context is cancelled during tool execution, the loop will start another provider stream before discovering the cancellation.

**Fix:** add `if ctx.Err() != nil { return ... }` at the start of each iteration.

### 10. `JSONLEventLogger` opens and closes the file on every append

**File:** `pkg/agentcore/logger_jsonl.go:20-43`

Each `Append()` call does `os.OpenFile`, writes one JSON line, and closes. For a turn with many events (deltas, tool calls, results), this means many open/write/close syscall cycles. This will be a bottleneck under load.

**Fix:** keep the file handle open for the logger's lifetime, or batch writes.

### 11. `shell.exec` tool's `Run` requires `cwd` but schema says only `command` is required

**Files:** `pkg/agentcore/tool_shell.go:29,39`, `pkg/agentcore/tool_runner.go:105`

The tool schema declares only `"command"` as required. But `Run()` calls `requiredStringArg(input.Args, "cwd")` which errors if cwd is absent. This works only because `enforcePolicy` injects `cwd` as a default. If `Run()` is ever called without going through the runner (e.g., in tests or future refactors), it breaks.

**Fix:** either mark `cwd` as required in the schema, or use `optionalStringArg` with a fallback in `Run()`.

### 12. Hardcoded pi-ai environment variable name

**File:** `pkg/ai/provider_openai_responses.go:230`

`resolveCacheRetention` reads `PI_CACHE_RETENTION` env var. This is a leftover from the pi-ai port and should use a gopher-specific name.

### 13. `init()` panics on bad generated model JSON

**File:** `pkg/ai/models.go:29-31`

`loadGeneratedModels()` calls `panic(err)` if the hardcoded JSON is malformed. This means any import of `pkg/ai` will crash the entire binary with no recovery path. A corrupted models JSON during development would be hard to debug.

**Fix:** log the error and continue with an empty registry, or return an error from a non-init setup function.

### 14. Missing Anthropic in `GetEnvAPIKey`

**File:** `pkg/ai/env_api_keys.go:6-29`

`GetEnvAPIKey` has cases for OpenAI, ZAI, KimiCoding, OpenAI Codex, and Ollama, but no case for Anthropic. Since `APIAnthropicMessages` is a supported API and Anthropic models exist (via kimi-coding at minimum), users of direct Anthropic models will get an empty API key with no error.

**Fix:** add a case for `ProviderAnthropic` (or whatever the canonical provider name would be) reading `ANTHROPIC_API_KEY`.

### 15. `ParseStreamingJSON` is O(n^2)

**File:** `pkg/ai/json_parse.go:20-26`

If the initial parse fails, it trims one character at a time from the end and retries `json.Unmarshal` each time. For a large incomplete JSON string of length n, this is O(n^2) work. Streaming tool call arguments can be large.

**Fix:** scan backwards for the last valid structural character (e.g., `}`, `"`, digit) instead of brute-forcing.

---

## Low

### 16. `cloneAnyMap` duplicated across packages

**Files:** `pkg/agentcore/context_builder.go:78-98`, `pkg/ai/types.go:418-439`

`cloneAnyMap` in agentcore and `cloneMap` in ai are functionally identical deep-copy helpers. This duplication will drift over time.

**Fix:** export one and import it, or extract to a shared internal package.

### 17. `modelsToMap` duplicated across test files

**Files:** `pkg/agentcore/load_test.go:77-83`, `pkg/agentcore/run_turn_ollama_integration_test.go:160-166`

Same helper, different names (`modelsToMap` vs `modelsToMapIntegration`). Trivial duplication.

### 18. Inconsistent `os.Setenv` vs `t.Setenv` in test

**File:** `pkg/ai/env_api_keys_test.go:13`

Uses `os.Setenv("OLLAMA_API_KEY", ...)` while other env vars in the same test use `t.Setenv`. `os.Setenv` doesn't restore the original value after the test, leaking state to other tests (especially in parallel).

### 19. Unrelated constants in same const block

**File:** `pkg/agentcore/types.go:81-88`

`DefaultContextWindow` and `MaxToolRounds` are grouped with `EventType` constants. They're unrelated concepts.

### 20. `ToolOutput.Status` is plain `string` despite having defined constants

**File:** `pkg/agentcore/tool_runner.go:14-17`, `pkg/agentcore/types.go:130`

`ToolStatusOK`, `ToolStatusError`, `ToolStatusDenied` are untyped string constants. `ToolOutput.Status` is `string`. A named type (e.g., `type ToolStatus string`) would prevent accidental assignment of arbitrary strings.

### 21. `models_generated.go` is a single-line JSON blob

**File:** `pkg/ai/models_generated.go`

The entire model registry is one massive string literal on a single line. Impossible to review in diffs, easy to corrupt, hard to search. If this is truly generated, the generator should produce formatted JSON or a structured Go literal.

### 22. Hardcoded magic string "# Juice: 0 !important"

**File:** `pkg/ai/provider_openai_responses.go:209`

When reasoning is enabled for gpt-5 models but no effort/summary is specified, a hardcoded developer message `"# Juice: 0 !important"` is injected. No documentation explains what this does or why.

### 23. Session messages only updated on success

**File:** `pkg/agentcore/run_turn.go:98`

`s.Messages` is only assigned on a clean (no tool calls) terminal response. If a turn fails after several rounds of tool calls, the session retains the pre-turn message history. A retry would replay the entire conversation including already-executed tool calls.

### 24. `defaultHTTPClient` has Timeout: 0

**File:** `pkg/ai/provider_utils.go:15`

The shared HTTP client has no timeout. Individual requests rely on context cancellation, but if a context has no deadline and a server holds the connection, the request hangs indefinitely.

### 25. Hardcoded OAuth client ID

**File:** `pkg/ai/oauth/openai_codex.go:18`

`openAICodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"` is hardcoded. Should be configurable or at minimum documented as an upstream constant.
