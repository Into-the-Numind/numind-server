# NDF S5 Validation Strategy · `agent-mode-open-tools-skill-as-guidance`

**Stage**: S3 artifact (Rule 10 — strategy fixed in S3, executed in S5) · reviewed by the S3 gate reviewer.

## Why this mix (rationale)

This is a **backend-only**, **high-risk** change (touches `runner.go` + `runner_runstream.go` = all agents; permission/skill surface). Two layers:

1. **Primary — Go test suite (persistent regression protection).** The change is entirely backend logic (tool registration, skill loading, permission deletion). The deterministic Go tests exercise BOTH runner paths and every AC directly, are CI-runnable, and live in the repo forever. This is the real regression net — it does not depend on a flaky browser or a live LLM. Rule 10 honest declaration: the Go suite IS persistent regression (unlike a one-shot /qa).
2. **Secondary — dev live-path E2E (confidence on the real stream).** After `/deploy-dev server`, validate the actual `RunStream` path with a real LLM + real registry + real sandbox in the browser. This catches integration issues the unit tests can't (prompt assembly, LLM actually seeing the catalog, tool genuinely callable end-to-end).

Playwright-persistent vs gstack-one-shot for layer 2 is decided at S5 execution by feasibility (see below) — but layer 1 already guarantees regression protection regardless, which satisfies the high-risk requirement.

## Layer 1 — Go suite (must all pass before S6)

`go test ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...` + `task lint`. Coverage by AC:
- AC-1 full-open: `runner_fullopen_test.go` (both `Run` + `RunStream`) — no-skill agent's tool set ⊇ {bash_exec, image_gen, run_python}, ∌ {document_generate, use_skill, read_skill}.
- AC-3/4 load_skill DB: wrap + recommendation line + Chinese name round-trip.
- AC-5 load_skill DB+disk + DB-first collision.
- AC-6 cap exhaustion graceful ack.
- AC-7 regression test (single-loop, no invoke_skill/use_skill/read_skill; load_skill in catalog/addendum/baseline).
- AC-8 dead-code gone + ToolFlag-inert assertion + prompt-string assertion.

## Layer 2 — dev E2E (after deploy, key user paths)

Credentials: `E2E_USERNAME` / `E2E_PASSWORD` env vars. Target: `$DEV_SITE_URL` (agent-mode student UI). Paths:
1. **P1 — full-open reachability** (AC-1): run an agent (no skills) and prompt it toward a task that needs a previously-gated tool (e.g. "用 Python 算一下…" → `run_python`/`bash_exec`); confirm via the streamed tool-call cards / dev backend agent_run log that the tool is **invoked** (not denied "工具未启用").
2. **P2 — skill guidance takes effect** (AC-3): an agent with a bound DB skill OR a doc task ("生成一个 PPT") → confirm `load_skill` is called and the run proceeds (read_skill→run_python two-step now `load_skill`→run_python), file produced.
3. **P3 — behavior no-drift** (AC-2): a sales-FAQ-style agent given a normal domain question still answers in-domain and does NOT spontaneously call `run_python`/`bash_exec` (assert on tool-call sequence in the agent_run log/trace, not exact text).
4. **P4 — existing B2B skill config intact** (AC-4): an agent with an existing bound skill carrying `allowed_tools` JSON loads via `load_skill` without error; recommendation line visible in the trace.

## Persistent vs one-shot decision (executed in S5)
- **Preferred**: add a Playwright spec under `numind-web-v3/e2e/` if the agent-mode student flow is E2E-reachable (login + run + assert tool-call stream). This gives persistent layer-2 regression.
- **Fallback (honest)**: if the agent-mode UI lacks a stable E2E hook (the modality is young), layer 2 runs as gstack `/qa` one-shot browser validation on dev, and the **Go suite remains the persistent regression net** (justified: the change is backend-only and both runner paths are unit-covered). This fallback is acceptable for THIS change specifically because no business logic lives in the frontend — the frontend only renders tool-call/narration events it already handles.

## Out
No prod. Deploy stops at dev. User acceptance gates prod.
