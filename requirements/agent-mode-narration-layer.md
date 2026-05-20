# Requirement: Agent Mode Narration Layer (#8/14)

**Feature ID**: `agent-mode-narration-layer`
**Track**: Standard
**Blueprint Ref**: `docs/agent-mode/architecture-v1.md` §4.7 (Narration Layer)
**Prerequisites**: #2 runtime-skeleton (RunHooks, adapter) merged; #3 tool-registry (FullTool 8 narration methods + BaseTool defaults) merged; #5 skill-system (HookActionRegistry, biz.go wire pattern) merged.

---

## 1. Problem

Agent Runtime today executes tools (Pre/PostToolCall hook chain in `adapter_full_to_eino.go`) but emits **zero learner-visible narration**. The `FullTool` interface declares 8 narration methods (`UserFacingName`, `NarrationVerb`, `GetActivityDescription`, `RenderToolUseMessage`, `RenderToolResultMessage`, `RenderToolErrorMessage`, `NarrationDetail`, `ShouldShowResultInNarration`), `BaseTool` ships defaults that return empty `NarrationMessage{}`, and each concrete tool overrides `UserFacingName + NarrationVerb` only. There is **no central place that turns tool execution into human-readable Chinese for the learner**, and the `RunResult` / SSE wire surface carries internal `ToolResult` JSON.

Without a Narration layer:
- Learner UI (#11) has nothing to render mid-stream — sees an empty terminal block and a final assistant message
- Failure messages would leak stack traces / errno codes / tool-internal JSON
- The §4.7.2 dual-output protocol (internal `tool_call` vs learner-visible `narration`) is unenforceable
- Future configurator UX (#10) cannot let operators edit per-tool narration templates

This feature **does not** introduce real SSE / WebSocket wire (that's #11) and **does not** wire real LLM fallback calls (that's #14). It builds the biz protocol and a **best-effort in-memory streamer** so #11 and #14 have something stable to plug into.

---

## 2. Goals

G1. **Single narration source-of-truth**: every tool call generates exactly one `NarrationEvent` per state transition (use / result / error / rejected). Adapter never writes ad-hoc strings.

G2. **6-state enum + 4-state v1 emitter** (blueprint §4.7.4 vs concrete adapter hook points): blueprint defines all six states `queued / use / progress / result / error / rejected`, and v1's `NarrationEvent.State` enum carries **all six values** for forward compatibility. v1 emitter actively fires **4 of 6**: `use / result / error / rejected`. `queued` (LLM has not yet reified the tool call — needs real ReAct loop in #14) and `progress` (mid-execution push from long-running tools — needs sandbox push in #13/#14) are declared but no v1 site emits them.

G2a. **HookAction → narration state mapping** (resolves S0 P1): the adapter's PreToolCall short-circuit emits `StateRejected` ONLY when the caller-supplied hook returns `HookActionStop` or `HookActionBlockingStop` *during a permission-pipeline-style rejection scenario*. Because #6 has not landed yet, v1 cannot distinguish "permission rejected" from "sandbox kill" from "stop hook" at the HookAction level — the enum is flat. v1 ships with a single deterministic rule: **any non-Continue PreToolCall HookAction emits `StateRejected`** (NOT `StateError`), because the rejection happens before tool Execute even runs and a user-facing "your request was blocked" framing is more accurate than "the tool errored." When #6 lands a richer reason channel, `Provider.Emit` for the rejected state will receive a structured reason; v1's reason field defaults to empty string and the yaml `rejected_template` renders a generic "这个操作被规则拦截了". Errors *during* tool Execute (PostToolCall.execErr != nil) emit `StateError` instead. This contract is the integration test fixture (see metrics §4).

G3. **Template-first translation with LLM fallback hook**: every emitted event is rendered through `tool-display.yaml` (static, < 1ms). If the tool name has no entry, the Translator falls back to a `LLMFallback` interface whose v1 impl returns a deterministic stub (`正在执行 <verb><detail>`). #14 replaces the stub with real `aiservice.Chat`.

G4. **Friendly failure narration**: error events translate `execErr` through a small error classifier (timeout / context-canceled / permission-denied / hook-stopped / generic) into one of 5 prebuilt Chinese templates. Never leak `error.Error()` raw.

G5. **Subscriber-agnostic streamer**: the biz layer exposes a `Subscribe(runID uint64) (<-chan NarrationEvent, func())` API. v1 has a single in-memory implementation backed by a per-run buffered channel + cleanup. #11 wraps this in HTTP SSE.

G6. **Zero impact on existing terminal reasons / state machine**: Narration is a side-channel — does not add new `TerminalReason`, does not change `LoopEvent` enum, does not change `RunHooks` PreToolCall / PostToolCall signatures.

G7. **Zero impact on `credit_transaction.source_type` CHECK constraint**: this feature touches no billing.

G8. **Zero prod impact**: feature flag-free (always on once wired), but adapter degrades to legacy behavior if `*narration.Provider` is nil (i.e., #2 mock pathway preserved).

---

## 3. NIS (Non-Implementation Scope) — out of scope

- N1. **Persistence**: no `agent_narration_event` DB table. v1 is memory-only. Replay across restarts is a #11/#14 follow-up.
- N2. **Real LLM fallback**: `LLMFallback` interface is wired, default impl returns deterministic stub. #14 plugs `aiservice.Chat(ctx, "agent-narration-fallback", req)` with `qwen-turbo` and `MaxTokens: 50`.
- N3. **HTTP / SSE / WebSocket wire**: no `controller` / `router` changes. v1 exposes biz-level `Subscribe()` only. #11 builds the wire.
- N4. **Configurator UI**: no admin CRUD for `tool-display.yaml` — file is statically loaded at startup. #10 will edit per-tenant overrides.
- N5. **Progress events from long-running tools**: `StateProgress` enum value exists but no tool emits it in v1. #13 sandbox / #14 LLM ReAct integration will push progress.
- N6. **Queued state**: `StateQueued` enum exists; LLM ReAct loop in #14 will emit it before tool reification. v1 emitter never fires queued.
- N7. **Permission-denied narration semantics (refined)**: v1 emits `StateRejected` for *any* non-Continue PreToolCall HookAction (Stop / BlockingStop) using a single generic Chinese template. v1 does NOT introspect the underlying reason (timeout vs sandbox kill vs permission). #6 permission-pipeline ships a structured `RejectionReason` channel that `Provider.Emit` will accept in a follow-up minor; v1's API leaves the field optional (`Reason string` default-empty) so #6 lands additive.
- N8. **Per-tenant narration overrides**: only platform-level `tool-display.yaml` in v1. Tenant overrides land with #10.
- N9. **Manager-end (admin) HTTP endpoints**: no `admin_router.go` changes.
- N10. **Prod deploy**: this feature is dev-only; no `git tag`, no `/deploy-prod` invocation, no `config_prod.yaml` change.

---

## 4. Success Metrics (S5 acceptance basis)

- `go test ./internal/numind/biz/narration/... -race` PASS (≥ 80% coverage on biz/narration package; template renderer + error classifier ≥ 95%).
- `go test ./internal/numind/biz/agent/... -race` PASS (no regression on adapter / runner / hook coverage from #2-#5 baselines).
- `go vet ./...` clean.
- **`use` event timing contract (resolves S0 P1)**: `use` is emitted by the adapter **AFTER** PreToolCall returns `HookActionContinue` AND BEFORE `FullTool.Execute` is invoked. This is the only emit site for `use`. PreToolCall short-circuit (non-Continue) emits `StateRejected` instead and never emits `use`.
- Integration test: one synthetic AgentRun (mock LLM, 1 fake tool) emits exactly 2 NarrationEvents (1 × `use`, 1 × `result`) observed via `Subscribe(runID)`.
- Failure-path integration test: tool returns `errors.New("boom")` → exactly 1 × `use` + 1 × `error` event; error event payload (rendered Chinese message) contains no raw `boom` substring.
- Rejected-path integration test: PreToolCall returns `HookActionStop` → exactly 1 × `rejected` event; no `use` / `result` emitted (consistent with the timing contract above).
- `configs/tool-display.yaml` is present, parses without error at startup, and contains entries keyed by the **exact string returned by each built-in tool's `Name()` method**: `bash_exec`, `document_generate`, `image_gen`, `kb_search`, `learner_data_query`, `get_current_date`. Yaml-key mismatch is a startup-blocking error (loader validates keys against the registered FullTool name set at boot).
- biz.go wire: `agent.WithNarrationProvider(narrationProv)` option call exists; `narrationProv = narration.NewProvider(...)` initialized from yaml; runner attaches provider to per-Run hooks; `AutoMigrate` list unchanged (no new tables).
- Manifest progress matches `total_tasks == completed_tasks == reviewed_tasks`.
- Zero modification to `config_prod.yaml`, `migrations/`, `credit_*` tables.

---

## 5. Risks

- R1. **Concurrent emit during high-throughput Runs**: `Provider.Emit` must be lock-free or atomic per stream. Mitigation: per-Run channel is single-writer (adapter goroutine) / single-reader (subscriber), buffered (cap 256) → fast path lock-free; oldest event drop on full buffer; document drop semantics.
- R2. **Tool author forgets to override `NarrationVerb`**: Caught at compile time today (`NarrationVerb()` is mandatory in `FullTool`). No risk for narration layer specifically.
- R3. **YAML parse failure at startup**: blocks `biz.NewBiz` → process crashes early. Acceptable for dev; document operator runbook. v1 ships with all 6 built-in tools in yaml; missing entry only hits LLM fallback (deterministic stub).
- R4. **Subscriber leak (channel never closed)**: cleanup callback returned by `Subscribe` MUST be invoked. #11 owns this contract; v1 enforces via test that calling cleanup twice is idempotent and that abandoned channels are GC'd after Run terminates.
- R5. **NarrationMessage struct field expansion**: blueprint mentions `Icon` in §4.7.6 (✓ / ⚠️ / ✕). v1 includes `Icon` field but does not enforce a strict enum (UI-side decision). Future-compatible.
- R6. **Cross-Run pollution via shared `*narration.Provider`**: same pattern as `r.defaultHooks` Registry — mitigated by Provider being a registry of per-runID streamers, not a per-Run state holder.

---

## 6. Term Translation (Blueprint ↔ Numind)

- Blueprint §4.7 `tenant_id` (multi-tenant SaaS framing) → Numind `parent_user_id` (B2B2C parent-child account model). v1 does NOT scope narration templates by tenant — `tool-display.yaml` is platform-global. #10 introduces per-tenant overrides.
- Blueprint §4.7.3 `LLM fallback model = qwen-turbo` → matches `ai-service.md` provider/model registry; v1 stub leaves model selection to #14.
- Blueprint §4.7.4 `verb / detail_template / result_template / progress_template / error_template` → yaml schema preserved 1:1.

---

## 7. Dependency Graph

- **Inputs** (must be on develop):
  - #2 runtime-skeleton: `RunHooks` struct (extend with `NarrationStreamer` pointer field), `adapter_full_to_eino.go` Pre/PostToolCall integration points, `runner.go` `WithDefaultHooks` / `WithSkillStore` option pattern.
  - #3 tool-registry: `FullTool` 8 narration methods, `BaseTool` defaults, `NarrationMessage` struct.
  - #5 skill-system: `biz.go:245-249` wire site, `HookActionRegistry` race-safe pattern (model for `Provider` design).
- **Outputs** (downstream consumers):
  - #6 permission-pipeline: optional consumer of `StateRejected` (today emitted on any non-Continue HookAction; #6 narrows to permission_denied subset).
  - #10 configurator-ux: admin CRUD for `tool-display.yaml` per-tenant overrides.
  - #11 student-ux: subscribes to `Provider.Subscribe(runID)`, renders SSE / WebSocket.
  - #14 e2e-rollout: replaces `LLMFallback` deterministic stub with real `aiservice.Chat`, adds queued + progress emitters via real ReAct loop.

---

## 8. Decided Constraints (locked at S0; S1 may refine but not re-litigate)

- **D1**: `Provider` is a process-singleton holding a per-runID streamer registry — mirrors `HookActionRegistry` pattern. Same shared instance is wired via `agent.WithNarrationProvider(prov)` and looked up per-Run inside `runner.Run`. No per-Run Provider instantiation.
- **D2**: Per-Run channel is buffered (cap **256**) with drop-oldest semantics on overflow. Drop is logged at warn-level via `zap` with `run_id` + `tool_call_id` so #11 can detect stream lag. Rationale: 256 ≥ 5× p95 turns-per-Run estimate and bounds memory at ~64KB per active Run.
- **D3**: `tool-display.yaml` ships at `configs/tool-display.yaml` (ops-visible, mounted via existing config loader). Tests use an in-memory yaml fixture (no filesystem dependency).
- **D4**: `Provider.Emit` API takes `(ctx, toolName string, state State, payload EmitPayload)` — i.e., **string-keyed dispatch**, NOT a `FullTool` reference. The Provider looks up the yaml entry by `toolName` and renders. This keeps `narration` package free of `agent` package import cycle and matches blueprint §4.7.3 hierarchy (yaml first → LLM fallback; no tool-method second pathway in v1). Tool methods like `RenderToolUseMessage` remain on `FullTool` for future Provider extension hook but are **not invoked by v1 Provider**. S1 ADR confirms; S2 specifies `EmitPayload` struct.

## 9. Open Questions (resolve in S1)

- Q1. Error classifier coverage: which errno / standard error types get friendly templates? → S1 ADR enumerates exactly 5 categories (context-canceled, deadline-exceeded, permission, sandbox-kill, generic-fallback) and the Chinese template for each.
- Q2. Should the yaml loader fail-hard if **any** registered FullTool name is missing from yaml, or warn-and-fallback to LLM stub? → S1 ADR (recommend warn-and-fallback for forward compat with #14-added tools).
