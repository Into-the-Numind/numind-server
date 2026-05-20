# Proposal: Agent Mode Narration Layer (#8/14)

**Status**: S1 → S2 transition
**Feature ID**: `agent-mode-narration-layer`
**Requirement**: `numind-server/requirements/agent-mode-narration-layer.md`
**Blueprint**: `docs/agent-mode/architecture-v1.md` §4.7

---

## 1. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│  biz/narration package (NEW)                                     │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────┐    │
│  │ event.go     │  │ display.go   │  │ error_translate.go  │    │
│  │ - State enum │  │ - YAML load  │  │ - ClassifyError()   │    │
│  │ - Event{}    │  │ - Renderer{} │  │ - 5 categories      │    │
│  │ - EmitPayload│  │ - tmpl funcs │  │                     │    │
│  └──────────────┘  └──────────────┘  └─────────────────────┘    │
│         ▲                ▲                       ▲              │
│         │                │                       │              │
│         └──────────┬─────┴───────────────────────┘              │
│                    │                                            │
│           ┌────────┴────────┐    ┌──────────────────┐           │
│           │ translator.go   │    │ streamer.go      │           │
│           │ - Translate     │    │ - Streamer iface │           │
│           │ - LLMFallback   │    │ - memStreamer    │           │
│           │   iface (#14)   │    │ - per-runID map  │           │
│           └────────┬────────┘    └────────┬─────────┘           │
│                    │                      │                     │
│                    └──────┬───────────────┘                     │
│                           ▼                                     │
│                ┌─────────────────────┐                          │
│                │ provider.go         │                          │
│                │ - Provider struct   │                          │
│                │ - NewProvider(cfg)  │                          │
│                │ - Emit(ctx,name,…)  │ ◄─── agent/adapter      │
│                │ - Subscribe(runID)  │ ◄─── #11 student-ux     │
│                └─────────────────────┘                          │
└──────────────────────────────────────────────────────────────────┘
                            ▲
                            │ singleton wire (biz/biz.go)
                            │
┌──────────────────────────────────────────────────────────────────┐
│  biz/agent package (MODIFY 3 files, NO signature breaks)         │
│                                                                  │
│  hooks.go      : add  NarrationProvider *narration.Provider     │
│                  field to RunHooks (opt-in nil = legacy)         │
│  runner.go     : add  WithNarrationProvider(p) option;           │
│                  attach to per-Run effectiveHooks                │
│  adapter_full_to_eino.go : on Pre/PostToolCall, after            │
│                  Registry.Record, dispatch to hooks.NarrationProvider │
└──────────────────────────────────────────────────────────────────┘
                            ▲
                            │ biz.go:245-249 wire
                            │
┌──────────────────────────────────────────────────────────────────┐
│  configs/tool-display.yaml (NEW)                                 │
│  - 6 built-in tool entries × 4 templates (use/result/error/      │
│    rejected) + verb + detail                                     │
└──────────────────────────────────────────────────────────────────┘
```

**Key invariant**: Adapter delegates to `hooks.NarrationProvider.Emit(...)` only — Adapter knows nothing about yaml, templates, or LLM fallback. Provider is the single decision point.

---

## 2. Package Boundary & Import Direction

```
biz/narration         ──── DOES NOT import ────► biz/agent  (avoid cycle)
biz/agent             ──── imports ────────────► biz/narration  (one-way)
biz/biz.go            ──── imports both        (wire site)
```

Concretely:
- `biz/narration/event.go` defines `State` (string enum), `Event` (the wire struct), `EmitPayload` (the input to `Emit`). All string/byte-based; no `FullTool` reference.
- `biz/agent/hooks.go` adds `NarrationProvider *narration.Provider` field — opaque pointer; agent package treats it as a sink.
- Adapter calls `hooks.NarrationProvider.Emit(ctx, toolName, state, payload)` and gets back nothing meaningful (best-effort fire-and-forget; errors logged at warn).

Rationale (D4 from S0): keeps `narration` package import-cycle-free, plays well with future per-tenant Provider variants (#10), and lets tests instantiate Provider without spinning up the full agent runtime.

---

## 3. NarrationEvent / Payload / State (event.go)

```go
package narration

type State string

const (
    StateQueued    State = "queued"    // reserved; v1 unused
    StateUse       State = "use"       // adapter PreToolCall Continue → before Execute
    StateProgress  State = "progress"  // reserved; v1 unused (sandbox push #13/#14)
    StateResult    State = "result"    // adapter PostToolCall execErr == nil
    StateError     State = "error"     // adapter PostToolCall execErr != nil
    StateRejected  State = "rejected"  // adapter PreToolCall HookAction != Continue
)

// Event is the wire-format struct surfaced via Subscribe(runID).
type Event struct {
    RunID      uint64    `json:"run_id"`
    ToolCallID string    `json:"tool_call_id"`           // monotonic per Run; set by Provider
    ToolName   string    `json:"tool_name"`              // e.g., "bash_exec"
    State      State     `json:"state"`
    Verb       string    `json:"verb,omitempty"`         // "正在执行"
    Detail     string    `json:"detail,omitempty"`       // "你的脚本"
    Icon       string    `json:"icon,omitempty"`         // "⋯" / "✓" / "⚠️" / "✕"
    Message    string    `json:"message"`                // fully-rendered Chinese text
    Reason     string    `json:"reason,omitempty"`       // for StateRejected; #6 will populate
    Timestamp  time.Time `json:"timestamp"`
}

// EmitPayload is what callers pass to Provider.Emit.
// Distinct from Event because Provider fills in computed fields (Icon,
// Message, Timestamp) before fanning out to streamer.
type EmitPayload struct {
    RunID      uint64
    ToolCallID string                 // caller-supplied, e.g., fmt.Sprintf("%d-%d", runID, callSeq)
    Input      json.RawMessage        // observable input (post-BackfillObservableInput)
    Result     json.RawMessage        // for StateResult
    Err        error                  // for StateError
    Reason     string                 // for StateRejected; v1 empty, #6 sets
}
```

ADR-1: `Icon` is computed by Provider from `State`, hardcoded mapping (`use`/`queued`/`progress` → `⋯`, `result` → `✓`, `error` → `⚠️`, `rejected` → `✕`). Matches blueprint §4.7.6.

ADR-2: `ToolCallID` format is `"<runID>-<seq>"` where `seq` is an atomic counter scoped to the Run. The adapter increments seq once per `InvokableRun` call and reuses the same ID for the matched `use → result/error` pair. No persistence; just a debug correlator.

---

## 4. tool-display.yaml schema (display.go)

```yaml
# numind-server/configs/tool-display.yaml
# v1: platform-global templates. #10 will add per-parent overrides.

tools:
  bash_exec:
    verb: "正在执行"
    detail_template: "{{ .input.action }}"           # uses observable input fields
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "执行完成{{ if .result.stdout }}，已输出 {{ .result.stdout | len }} 字符{{ end }}"
    error_template:  "执行遇到问题，{{ .reason_friendly }}"
    rejected_template: "这个命令被规则拦截了"

  document_generate:
    verb: "正在生成"
    detail_template: "{{ .input.format }} 文档"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "文档已生成"
    error_template:  "生成中断，{{ .reason_friendly }}"
    rejected_template: "文档生成被规则拦截"

  # ... image_gen / kb_search / learner_data_query / get_current_date entries follow same shape

defaults:
  verb: "正在处理"
  use_template:    "{{ .verb }}"
  result_template: "处理完成"
  error_template:  "处理失败，{{ .reason_friendly }}"
  rejected_template: "操作被拦截"
```

ADR-3 (resolves S0 Q2): **warn-and-fallback** on missing yaml key. Loader at startup logs `zap.Warn` listing FullTool names missing from yaml; Provider.Emit falls through to `defaults` block. Rationale: forward-compat with #14-added tools (admin-defined MCP tools that don't exist at boot). Strict-mode reserved for future flag `strict_yaml: true` in the yaml itself.

ADR-4: template engine = Go `text/template` standard library (no new deps). Custom funcs: `len` (string length), `truncate` (cap at N chars), `default` (fallback empty). All template execution wrapped in `defer recover()` — template panic logs warn, falls back to `defaults` block, never crashes adapter goroutine.

**P1-3 fix — template parse-time errors**: `NewProvider` MUST call `template.New(...).Funcs(funcs).Parse(tmplString)` on EVERY template field (use/result/error/rejected × every yaml tools entry + defaults block) during init, and return error on the FIRST parse failure with a clear message identifying the tool key + field name. Parsed templates are cached on `*Renderer` (one `*template.Template` per (toolName, state) pair). Runtime `Execute` cannot encounter a parse error — only an execution-time panic (caught by ADR-4's `defer recover()`). This bifurcation prevents the "valid YAML, malformed template" silent-failure mode.

ADR-5: yaml parsing uses `gopkg.in/yaml.v3` (already in go.mod). Loader: `display.go::Load(path string) (*Renderer, error)` + `display.go::LoadFromBytes([]byte) (*Renderer, error)` for tests.

---

## 5. Error Classifier (error_translate.go)

Resolves S0 Q1.

```go
package narration

type ErrorCategory string

const (
    ErrCatContextCanceled   ErrorCategory = "context_canceled"   // ctx.Err() == context.Canceled
    ErrCatDeadlineExceeded  ErrorCategory = "deadline_exceeded"  // ctx.Err() == context.DeadlineExceeded
    ErrCatPermissionDenied  ErrorCategory = "permission_denied"  // errors.Is errno.ErrPermissionDenied (placeholder; #6 lands real)
    ErrCatSandboxKilled     ErrorCategory = "sandbox_killed"     // bashvalidator output / sandbox OOM signal (placeholder)
    ErrCatGeneric           ErrorCategory = "generic"            // anything else
)

// ClassifyError takes the execErr from PostToolCall and returns
// (category, friendlyReason). friendlyReason is the {{ .reason_friendly }}
// substitution used by error_template — Chinese, < 20 chars, no internal
// jargon, no stack trace fragments.
func ClassifyError(err error) (ErrorCategory, string) { ... }
```

Mappings:
| Category | friendlyReason (Chinese) |
|---|---|
| `context_canceled` | "操作被中断" |
| `deadline_exceeded` | "超过时间限制" |
| `permission_denied` | "这个操作没有权限" |
| `sandbox_killed` | "运行环境被回收" |
| `generic` | "稍后再试一下" |

**P1-2 fix — context.Canceled ambiguity**: `context.Canceled` fires from both user-initiated cancel (`AgentRunner.Cancel(runID)`) AND runtime-initiated cancel (parent ctx torn down by higher-layer timeout, sandbox session destroyed by PostToolCall cleanup, etc.). v1 cannot reliably distinguish — `ctx.Err()` is the same value in both cases. So the friendly reason uses the neutral wording "操作被中断" (was: "你取消了这次操作"). When #6 lands a structured cancellation channel, ClassifyError will accept an optional `cancellationSource` hint to disambiguate; v1 API leaves this as a single-arg signature for forward-compat.

ADR-6: `ClassifyError` is pure (no I/O, no goroutine). Returned `friendlyReason` is the only substring the error template ever sees — `err.Error()` raw text is **never** rendered. This is the security-critical contract enforced by tests.

---

## 6. Translator + LLMFallback (translator.go)

```go
package narration

// LLMFallback is the v1 hook for #14 to plug in aiservice.Chat-backed
// generation when yaml has no entry. v1 default impl is deterministic stub.
type LLMFallback interface {
    Render(ctx context.Context, toolName string, state State, payload EmitPayload) (verb, detail string)
}

// stubLLMFallback implements deterministic, non-LLM rendering for v1.
type stubLLMFallback struct{}

func (stubLLMFallback) Render(_ context.Context, toolName string, state State, _ EmitPayload) (string, string) {
    // returns ("正在执行", toolName) — minimum viable narration when no yaml entry exists
}

// Translator owns the renderer + fallback chain.
type Translator struct {
    renderer *Renderer
    fallback LLMFallback
}

// Translate is the core function: yaml first → fallback.
// Always returns a usable Event (never returns error to caller).
func (t *Translator) Translate(ctx context.Context, payload EmitPayload, toolName string, state State) Event {
    // 1. Try yaml template via renderer
    // 2. If renderer returns ErrNoEntry, call fallback.Render
    // 3. Always compute Icon, Timestamp, ToolCallID
}
```

ADR-7: `LLMFallback` interface is intentionally narrow (returns `verb`, `detail` only — Translator wraps them in the standard message template). This keeps #14's `aiservice.Chat` integration trivially substitutable for the stub.

---

## 7. Streamer (streamer.go)

```go
package narration

type Streamer interface {
    Send(ev Event)                                      // adapter side; non-blocking
    Subscribe(runID uint64) (<-chan Event, func())     // consumer side; returns cleanup
    CloseRun(runID uint64)                              // adapter side; cleanup when Run terminates
}

// memStreamer is the v1 in-memory impl: map[runID]*runChannel.
type memStreamer struct {
    mu       sync.RWMutex
    runs     map[uint64]*runChannel
    bufferSz int  // D2: 256
}

type runChannel struct {
    ch     chan Event
    closed atomic.Bool
}
```

ADR-8: `Send` is non-blocking. On full channel, drops OLDEST event (drain 1 → push new) and emits `zap.Warn` with `run_id`, `tool_call_id`, `dropped_state`. Rationale: bounded memory + observability of stream lag.

ADR-9: `Subscribe(runID)` creates the channel lazily if absent. This allows subscribers (#11) to connect before the first event fires. Per-Run channel is **single-subscriber** in v1; multi-subscriber fan-out is a #11 concern (HTTP SSE handler can be one subscriber feeding N HTTP clients).

ADR-10: `CloseRun` is called by adapter at the end of the Run (wire: `runner.Run` `defer` block).

**P1-1 fix — Send/CloseRun race safety**: Channel close is single-direction (CloseRun-only). `Send` MUST check `closed atomic.Bool` BEFORE attempting channel write, and use a `defer recover()` to absorb the rare race where `closed` was false on check but `CloseRun` fired before `case ch <- ev:` executed:

```go
func (rc *runChannel) send(ev Event) (dropped bool) {
    if rc.closed.Load() { return true }  // fast-path: already closed
    defer func() {
        if r := recover(); r != nil {
            // race: closed between Load() and send; benign because we GC anyway
            dropped = true
        }
    }()
    select {
    case rc.ch <- ev:
        return false
    default:  // full → drop oldest
        select { case <-rc.ch: default: }
        select { case rc.ch <- ev: return false; default: return true }
    }
}

func (rc *runChannel) close() {
    if rc.closed.CompareAndSwap(false, true) {
        close(rc.ch)
    }
}
```

`CloseRun` is idempotent (CompareAndSwap). Subscriber sees `<-chan` return zero-value + ok=false when channel closes. Cleanup callback from `Subscribe` is a no-op signal (subscriber abandoned) but does NOT close the channel — only `CloseRun` does. Test under `-race` with concurrent `Send` + `CloseRun` is mandatory (S3 plan).

---

## 8. Provider (provider.go)

```go
package narration

type Provider struct {
    translator *Translator
    streamer   Streamer
    callSeq    sync.Map  // map[uint64]*atomic.Int64 — accessed via LoadOrStore (P0-2)
}

// nextCallID returns "<runID>-<seq>" with seq monotonically incrementing per runID.
// Must use sync.Map.LoadOrStore (NOT Load → nil-check → Store) to avoid TOCTOU race
// when Eino schedules concurrent InvokableRun calls within the same Run.
func (p *Provider) nextCallID(runID uint64) string {
    v, _ := p.callSeq.LoadOrStore(runID, &atomic.Int64{})
    seq := v.(*atomic.Int64).Add(1)
    return fmt.Sprintf("%d-%d", runID, seq)
}

func NewProvider(cfg Config) (*Provider, error) { ... }

// Emit is the adapter's single entry point.
// Fire-and-forget; never blocks; never returns error (errors logged warn).
func (p *Provider) Emit(ctx context.Context, runID uint64, toolName string, state State, payload EmitPayload) {
    payload.RunID = runID
    if payload.ToolCallID == "" {
        payload.ToolCallID = p.nextCallID(runID)
    }
    ev := p.translator.Translate(ctx, payload, toolName, state)
    p.streamer.Send(ev)
}

// Subscribe re-exports streamer for #11.
func (p *Provider) Subscribe(runID uint64) (<-chan Event, func()) {
    return p.streamer.Subscribe(runID)
}

// CloseRun re-exports for runner.Run defer.
func (p *Provider) CloseRun(runID uint64) { p.streamer.CloseRun(runID) }

type Config struct {
    YAMLPath     string      // configs/tool-display.yaml
    YAMLBytes    []byte      // alternative for tests (in-memory)
    BufferSize   int         // D2 default 256
    LLMFallback  LLMFallback // nil → stubLLMFallback
    ToolNames    []string    // for startup-time yaml-key validation
}
```

ADR-11: `Provider` is goroutine-safe. Adapter goroutines call `Emit` concurrently across Runs and across tool calls. `translator` is read-only after init; `streamer` uses its own mutex; `callSeq` uses `sync.Map` + `atomic.Int64` per key.

ADR-12: `NewProvider` fails fast on:
- YAMLPath missing AND YAMLBytes nil
- YAML parse error
- (warn but not fail) yaml-key set doesn't match `ToolNames` — log missing names as warn, continue

---

## 9. Integration into biz/agent

### 9.1 hooks.go (1 field added)

```go
type RunHooks struct {
    PreToolCall       func(ctx context.Context, t tool.BaseTool, input string) (HookAction, error)
    PostToolCall      func(ctx context.Context, t tool.BaseTool, output string, err error) (HookAction, error)
    Registry          *HookActionRegistry            // #5
    NarrationProvider *narration.Provider            // #8: nil = legacy (no narration emit)
    NarrationRunID    uint64                          // #8: set by runner.Run from agent_run.ID
}
```

Why two fields not one wrapper struct: `NarrationProvider` is the shared singleton (lifetime = process), `NarrationRunID` is per-Run (lifetime = single Run). Keeping them separate avoids allocating a per-Run wrapper struct.

### 9.2 adapter_full_to_eino.go (3 emit sites added)

```go
func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
    input := ToolInput(args)

    if a.hooks != nil && a.hooks.PreToolCall != nil {
        action, err := a.hooks.PreToolCall(ctx, a, args)
        if err != nil { return "", fmt.Errorf("PreToolCall: %w", err) }
        if a.hooks.Registry != nil { a.hooks.Registry.Record(action) }
        if action != HookActionContinue {
            // EMIT REJECTED, no Execute, no use, no result/error
            a.emitNarration(ctx, narration.StateRejected, input, nil, nil, "")
            return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
        }
    }

    // EMIT USE (after Continue, before Execute)
    a.emitNarration(ctx, narration.StateUse, input, nil, nil, "")

    result, execErr := a.ft.Execute(ctx, input)
    var output string
    if result != nil { output = string(result) }

    // effectiveErr captures the error the CALLER will observe. It starts as execErr;
    // PostToolCall may upgrade it (postErr != nil && execErr == nil → return postErr).
    // Narration MUST reflect what the caller sees — emit StateResult only if effectiveErr == nil.
    effectiveErr := execErr

    if a.hooks != nil && a.hooks.PostToolCall != nil {
        postAction, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
        if a.hooks.Registry != nil { a.hooks.Registry.Record(postAction) }
        if postErr != nil {
            log.Warnw("PostToolCall failed",
                "tool", a.ft.Name(),
                "post_err", postErr,
                "exec_err", execErr)
            if execErr == nil {
                effectiveErr = fmt.Errorf("PostToolCall: %w", postErr)
            }
        }
    }

    // EMIT RESULT or ERROR after PostToolCall — based on effectiveErr (P0-1 fix).
    if effectiveErr != nil {
        a.emitNarration(ctx, narration.StateError, input, nil, effectiveErr, "")
    } else {
        a.emitNarration(ctx, narration.StateResult, input, result, nil, "")
    }

    if effectiveErr != nil { return output, effectiveErr }
    return output, nil
}

func (a *fullToolEinoAdapter) emitNarration(ctx context.Context, st narration.State, input ToolInput, result ToolResult, execErr error, reason string) {
    if a.hooks == nil || a.hooks.NarrationProvider == nil { return }
    observableInput := a.ft.BackfillObservableInput(input)  // #3 method — strip secrets if tool implements
    a.hooks.NarrationProvider.Emit(ctx, a.hooks.NarrationRunID, a.ft.Name(), st, narration.EmitPayload{
        Input:  json.RawMessage(observableInput),
        Result: json.RawMessage(result),
        Err:    execErr,
        Reason: reason,
    })
}
```

ADR-13: `BackfillObservableInput` is called before pushing input into Payload. This makes the yaml template safe to reference any input field — secrets/PII have been redacted by the tool's own method. Per-tool override is the tool's responsibility (already part of #3 FullTool contract).

ADR-14: The `use` emit happens AFTER PreToolCall Continue, BEFORE Execute (matches S0 timing contract). The `result/error` emit happens AFTER PostToolCall, NOT before — rationale: PostToolCall may legitimately mutate output (e.g., truncate, sanitize) and we want the narration to reflect the final state observable by the LLM.

### 9.3 runner.go (1 option + 3 lines in Run; P1-4 leak fix)

```go
type agentRunner struct {
    ...
    narrationProvider *narration.Provider  // #8
}

func WithNarrationProvider(p *narration.Provider) RunnerOption {
    return func(r *agentRunner) { r.narrationProvider = p }
}

// inside Run, IMMEDIATELY after runStore.Create returns and run.ID is available
// (BEFORE react.NewAgent / WriteTurn / any panic-prone init code) — P1-4 fix:
if r.narrationProvider != nil {
    defer r.narrationProvider.CloseRun(run.ID)
}

// later, after the effectiveHooks setup block:
if effectiveHooks != nil && r.narrationProvider != nil {
    effectiveHooks.NarrationProvider = r.narrationProvider
    effectiveHooks.NarrationRunID = run.ID
}
```

**P1-4 fix — defer registration timing**: The `defer CloseRun(run.ID)` MUST be registered immediately after `run.ID` is materialised by `runStore.Create`, BEFORE any subsequent code that could panic (`react.NewAgent`, skill lookup, hook resolution). Go defers run LIFO, so registering CloseRun first means it runs LAST — but that's fine because CloseRun is independent of context cancellation. Even if a panic torpedoes the rest of Run, the channel is GC'd via deferred close. Without this ordering, a panic in `react.NewAgent` orphans a runChannel entry in `streamer.runs` map until process death.

ADR-15: Provider attachment to `effectiveHooks` is conditional on `effectiveHooks != nil` because if there are no hooks at all (truly hook-less Run), there's no adapter to emit from. In practice #4 SandboxHookManager always wires defaultHooks, so the conditional is mostly defensive. The CloseRun defer is unconditional on hooks — it only depends on `r.narrationProvider != nil`.

### 9.4 biz/biz.go (3 lines at the wire site)

```go
// around biz.go:240 (BEFORE agent.NewAgentRunner)
narrationProv, err := narration.NewProvider(narration.Config{
    YAMLPath:   "configs/tool-display.yaml",
    BufferSize: 256,
    ToolNames:  toolNamesFromRegistry,  // pulled from agent.AgentToolRegistry.ListAllTools()
})
if err != nil {
    return nil, fmt.Errorf("biz.NewBiz narration provider: %w", err)
}

b.agentRunner = agent.NewAgentRunner(
    runStore,
    registry,
    agent.WithDefaultHooks(sandboxHookManager.AsRunHooks()),
    agent.WithSkillStore(ds.AgentDefinitions()),
    agent.WithNarrationProvider(narrationProv),  // NEW
)
```

ADR-16: Yaml path is hardcoded to `configs/tool-display.yaml` for v1. If absent in dev environments, `NewProvider` returns error → `biz.NewBiz` fails → process crashes early. This is intentional: silent absence would be a regression of UX guarantee.

---

## 10. Test Strategy (preview for S3)

| Layer | Test type | Coverage target |
|---|---|---|
| `event.go` | trivial unit | n/a (just types) |
| `display.go` | unit: yaml parse + template render + missing-key fallback | 95% |
| `error_translate.go` | table-driven: all 5 categories + nil err | 100% |
| `translator.go` | unit: yaml path, fallback path, panic recovery | 90% |
| `streamer.go` | unit: buffer overflow drop, subscribe before emit, cleanup idempotent, race detector | 90% |
| `provider.go` | unit: Emit happy path, callSeq monotonic, CloseRun cleanup | 90% |
| `biz/agent/adapter_full_to_eino_test.go` | integration: 3 fixtures (use+result, use+error, rejected); Provider via in-memory yaml; assert exact event counts | extend existing |
| `biz/agent/runner_test.go` | integration: WithNarrationProvider option attaches; CloseRun deferred | extend existing |
| `biz/agent/hooks_test.go` | add: RunHooks NarrationProvider field default nil | extend existing |

Race detector mandatory: `go test ./internal/numind/biz/narration/... ./internal/numind/biz/agent/... -race`.

---

## 11. Files Changed Summary (S3 plan input)

| File | Action | Approx LOC |
|---|---|---|
| `internal/numind/biz/narration/event.go` | NEW | ~80 |
| `internal/numind/biz/narration/display.go` | NEW | ~180 |
| `internal/numind/biz/narration/error_translate.go` | NEW | ~70 |
| `internal/numind/biz/narration/translator.go` | NEW | ~120 |
| `internal/numind/biz/narration/streamer.go` | NEW | ~160 |
| `internal/numind/biz/narration/provider.go` | NEW | ~110 |
| `internal/numind/biz/narration/*_test.go` | NEW (6 files) | ~700 total |
| `configs/tool-display.yaml` | NEW | ~80 |
| `internal/numind/biz/agent/hooks.go` | EDIT (+2 fields) | +3 |
| `internal/numind/biz/agent/adapter_full_to_eino.go` | EDIT (+3 emit sites, +1 helper) | +35 |
| `internal/numind/biz/agent/runner.go` | EDIT (+1 option, +1 attach, +1 defer) | +14 |
| `internal/numind/biz/agent/adapter_full_to_eino_test.go` | EDIT (+4 narration fixtures: use+result, use+error, rejected, postErr-when-execErr-nil) | +150 |
| `internal/numind/biz/agent/runner_test.go` | EDIT (+1 attach + CloseRun-leak assertion) | +50 |
| `internal/numind/biz/agent/hooks_test.go` | EDIT (+1 nil-default test) | +10 |
| `internal/numind/biz/biz.go` | EDIT (+4 lines wire) | +6 |

**P2-2 fix — explicit test file layout** (S3 plan input):
- `internal/numind/biz/narration/event_test.go` — type sanity (~30 LOC)
- `internal/numind/biz/narration/display_test.go` — yaml parse, template render, missing-key fallback, parse-time error (~180 LOC)
- `internal/numind/biz/narration/error_translate_test.go` — table-driven 5 categories (~80 LOC)
- `internal/numind/biz/narration/translator_test.go` — yaml + fallback paths, panic recovery (~120 LOC)
- `internal/numind/biz/narration/streamer_test.go` — buffer overflow (bufferSize=1 fixture), subscribe-before-emit, Send/Close race with `-race`, cleanup idempotent (~180 LOC)
- `internal/numind/biz/narration/provider_test.go` — Emit happy-path, callSeq LoadOrStore race-safety, CloseRun cleanup (~110 LOC)

**P2-1 note**: streamer drop-oldest + Send/Close-race paths are timing-sensitive. Use `bufferSize=1` to deterministically trigger drop branch; rely on `go test -race` (not coverage %) for the close-race safety contract.

**Total new code** ≈ 1700 LOC across 15 files (9 new — 6 prod + 6 test; 6 modified). Updated estimates account for P0/P1 fix code (effectiveErr branching, LoadOrStore, defer-recover, parse-time template validation, early defer registration).

---

## 12. Out of Scope (re-affirmed from S0 NIS)

N1 (no DB persistence) · N2 (no real LLM fallback) · N3 (no HTTP/SSE/WebSocket) · N4 (no admin UI) · N5 (no progress emitter) · N6 (no queued emitter) · N7 (no permission-reason distinction beyond placeholder Reason field) · N8 (no per-tenant yaml override) · N9 (no admin endpoints) · N10 (no prod deploy).

---

## 13. Decisions Locked at S1 (extends S0 D1-D7)

- **S1-D8**: ToolCallID format `"<runID>-<seq>"` (ADR-2)
- **S1-D9**: Template engine `text/template` stdlib, custom funcs `len/truncate/default` (ADR-4); NewProvider parses all templates at init (P1-3 fix)
- **S1-D10**: YAML missing-key behavior = warn-and-fallback to `defaults` block (ADR-3, resolves S0 Q2)
- **S1-D11**: 5 error categories with locked Chinese friendlyReason strings (ADR-6, resolves S0 Q1); `context.Canceled` → neutral "操作被中断" because user vs runtime cancel is indistinguishable in v1 (P1-2 fix)
- **S1-D12**: `LLMFallback` interface signature `Render(ctx, toolName, state, payload) (verb, detail)` (ADR-7); P2-4 — fallback impls MUST handle internal errors and return safe defaults (never propagate error out); #14's `aiservice.Chat` integration follows this contract
- **S1-D13**: Streamer per-Run = single-subscriber (multi-subscriber fan-out is #11's concern) (ADR-9)
- **S1-D14**: `BackfillObservableInput` called before Emit payload assembly (ADR-13)
- **S1-D15**: `result/error` emit AFTER PostToolCall, branching on **effectiveErr** (execErr or postErr-when-execErr-nil), so narration reflects what the CALLER observes (P0-1 fix to ADR-14)
- **S1-D16**: YAML hard-fail on parse error / file absent (ADR-16); soft-warn on missing keys (ADR-3)
- **S1-D17**: Blueprint §4.7.2 dual-output protocol (LLM emits structured `{tool_call, narration}` JSON) is NOT implemented in v1 — adapter-driven post-hoc template rendering is used instead (P2-5). Rationale: blueprint §4.7.2 requires LLM JSON-mode + dedicated prompt engineering + adapter wire changes that all belong to #14 ReAct loop. v1 narration is a side-channel produced from tool execution metadata, NOT from LLM output. #14 may extend the Provider to accept LLM-supplied narration as an override when both are available — current architecture supports this via an additional `OverrideMessage string` field on `EmitPayload` (reserved, unused in v1).
- **S1-D18**: P0-2 fix — `Provider.nextCallID(runID)` uses `sync.Map.LoadOrStore` idiom, NEVER `Load + nil-check + Store`, to avoid TOCTOU race under concurrent InvokableRun calls within the same Run
- **S1-D19**: P1-1 fix — `runChannel.send` uses `closed.Load() check + defer recover()` pattern; `runChannel.close` uses `CompareAndSwap` for idempotency; concurrent Send+Close race covered by `go test -race`
- **S1-D20**: P1-4 fix — `defer narrationProvider.CloseRun(run.ID)` registered IMMEDIATELY after `runStore.Create` returns, BEFORE any potentially-panicking init code (react.NewAgent, skill lookup) to prevent runChannel leak on panic
- **S1-D21**: P2-3 — `emitNarration` `reason string` parameter always `""` in v1 call sites (3 emit points); reserved for #6 permission-pipeline structured rejection reason — DO NOT remove or rename in v1 cleanup passes
