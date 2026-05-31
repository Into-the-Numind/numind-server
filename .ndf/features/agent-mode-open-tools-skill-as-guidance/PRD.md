# NDF S1 PRD · `agent-mode-open-tools-skill-as-guidance`

**Stage**: S1 · **Date**: 2026-05-31 · Pairs with `proposal.md`

This is a backend-runtime behavior change. "Product" surface = agent runtime behavior + configurator's mental model. No new UI, no new API.

---

## 1. Behavior changes (before → after)

| Aspect | Before | After |
|---|---|---|
| Tools an agent can call | `safeToolBaseline` + enabled categories (`bash_exec`/`image_gen` only if category on) + skill `allowed_tools` (when bound) | **All registered tools**, every agent. The configurator's category switches no longer cap availability. |
| Skill's role | DB skill "unlocks" its `allowed_tools` (dead-validator, already ungated); disk skill is read-only guidance | Pure guidance. Skill body = markdown instructions injected into context. `allowed_tools` shown as a **recommendation** hint. |
| Skill tools | `use_skill` (DB) + `read_skill` (disk) — two tools, two params, two catalogs | One `load_skill({name})` + one unified `## 可用技能` catalog covering both DB-bound and disk skills. |
| Loop | already single-loop (read SKILL.md / body → same agent writes code) | unchanged (merge must not regress) |
| `bash_exec` for a "sales FAQ" agent | unreachable unless `code_sandbox` category on | reachable (full-open) — sandbox+validator still bound what code does |
| Configurator effort | must curate per-skill `allowed_tools` whitelists + tool categories | none for tool access; skills are just guidance docs |

## 2. Invariants that MUST hold (zero-regression)

- I1 — **Single-loop preserved**: `load_skill` returns guidance to the **same** agent; no inner-LLM. `skill_progressive_loader_regression_test.go` passes (assertions renamed `read_skill`→`load_skill`, no `invoke_skill`, no `use_skill`/`read_skill` survivors).
- I2 — **Existing DB skills load unchanged**: a bound skill with `allowed_tools` JSON loads via `load_skill`, body reaches the LLM wrapped in `<system-reminder>`, plus the recommendation line. Chinese skill names round-trip.
- I3 — **Existing disk skills load unchanged**: `load_skill({name:"pptx-author"})` returns the SKILL.md body; `read_skill→run_python` two-step still works under the new name.
- I4 — **Cap retained**: per-turn `load_skill` count capped (default 3) when a turn state exists; over-cap returns a graceful error ack (not a Go error), count not bumped.
- I5 — **System-prompt 6-segment order** unchanged (CLAUDE.md §6b I3); catalog stays in §2.
- I6 — **Both runner paths** (`Run` + `RunStream`) behave identically.
- I7 — **No tool now denied** that the agent legitimately needs; full-open holds for every existing agent config (verify ToolFlag doesn't re-deny — it won't, but assert it).

## 3. Acceptance criteria (refined from S0; corrected per R1–R4)

| # | Criterion | Verify |
|---|---|---|
| AC-1 | A plain agent (no skills bound, `code_sandbox` category OFF) can call a previously-registration-gated tool — assert `bash_exec` (or `image_gen`) is in its Eino tool set and is callable | Go unit/integration test on runner tool-registration (both paths) + Langfuse/E2E |
| AC-2 | Behavior-no-drift: a sales-FAQ agent given a standard prompt still answers in-domain and does NOT spontaneously emit `run_python`/`bash_exec` unasked — assert on Langfuse tool-call sequence, not exact text | Playwright E2E + trace |
| AC-3 | After `load_skill("X")`, X's body (DB or disk) + the `allowed_tools` recommendation line appears in the next LLM generation input | Go unit test + trace |
| AC-4 | Existing B2B DB skill row (with `allowed_tools` JSON) loads via `load_skill` without error; Chinese name round-trips; no schema/migration | Go unit test with `model.Skill` fixture |
| AC-5 | `load_skill` loads BOTH a DB-bound skill and a disk skill (resolution order DB→disk); `use_skill`/`read_skill` no longer registered | Go unit test (two fixtures) + registry assertion |
| AC-6 | Cap still fires (I4) | Go unit test |
| AC-7 | `skill_progressive_loader_regression_test.go` passes with `load_skill` rename; no `invoke_skill`/`use_skill`/`read_skill` in catalog/addendum/baseline | existing regression test (updated) |
| AC-8 | Dead-code removed: no references to `UseSkillTurnScope`, `turn.AllowedTools`, `WithAgentBaseToolNames`; `task lint` clean; `WithFullToolMap` retained | `task lint` + grep |

## 4. Non-functional
- `go test ./internal/numind/biz/agent/...` + `./internal/numind/biz/permission/...` green; `task lint` clean.
- No latency regression (full-open registers ~22 vs ~16 tools — negligible; one-time per run).
- `load_skill` ≤50ms incl. DB-cache-hit and disk-fallback paths.

## 5. Rollout
- dev only (`/deploy-dev server`). **No prod** (high-risk; user acceptance gates prod).
- S5 validation: Playwright E2E mandatory (high-risk, all agents) — paths AC-1/2/3/4 — + the Go test suite for regression protection.
