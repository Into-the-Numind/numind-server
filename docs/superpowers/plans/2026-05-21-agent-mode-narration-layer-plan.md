# Task Plan: Agent Mode Narration Layer (#8/14)

**Status**: S3 → S4 transition
**Feature ID**: `agent-mode-narration-layer`
**Spec**: `numind-server/docs/superpowers/specs/2026-05-21-agent-mode-narration-layer-design.md`
**Branch**: `feature/agent-mode-narration-layer`
**Worktree**: `/private/tmp/wt-agent-mode-narration-layer-numind-server`

---

## 1. Task Decomposition (12 M-tasks)

Each task is atomic, independently buildable, and produces exactly 1 commit (Conventional Commits prefix `feat(agent-narration): MN ...`).

| # | Task | Files (owned) | Approx LOC | Depends on |
|---|---|---|---|---|
| **M1** | event.go + tests + iconForState lock | `internal/numind/biz/narration/event.go`, `internal/numind/biz/narration/event_test.go` | 80 + 50 = 130 | none |
| **M2** | error_translate.go + tests | `internal/numind/biz/narration/error_translate.go`, `internal/numind/biz/narration/error_translate_test.go` | 70 + 90 = 160 | none |
| **M3** | configs/tool-display.yaml (6 built-in tools + defaults) | `configs/tool-display.yaml` | 80 | none |
| **M4** | display.go + tests (parse-time validation + missingkey=zero + 9 fixtures) | `internal/numind/biz/narration/display.go`, `internal/numind/biz/narration/display_test.go` | 180 + 200 = 380 | none (defines own YAMLConfig types) |
| **M5** | streamer.go + tests (race-safe send/close, drop-oldest, 7 fixtures) | `internal/numind/biz/narration/streamer.go`, `internal/numind/biz/narration/streamer_test.go` | 180 + 200 = 380 | M1 |
| **M6** | translator.go + tests (yaml→fallback, panic recovery, time injection) | `internal/numind/biz/narration/translator.go`, `internal/numind/biz/narration/translator_test.go` | 120 + 140 = 260 | M1, M2, M4 |
| **M7** | provider.go + tests (LoadOrStore race-safety, CloseRun cleanup, 6 fixtures) | `internal/numind/biz/narration/provider.go`, `internal/numind/biz/narration/provider_test.go` | 130 + 130 = 260 | M5, M6 |
| **M8** | hooks.go +2 fields + hooks_test.go +1 test | `internal/numind/biz/agent/hooks.go`, `internal/numind/biz/agent/hooks_test.go` | +3 + 10 = 13 | M7 (for narration import path stability) |
| **M9** | adapter_full_to_eino.go +emit sites + 4 fixtures | `internal/numind/biz/agent/adapter_full_to_eino.go`, `internal/numind/biz/agent/adapter_full_to_eino_test.go` | +40 + 150 = 190 | M7, M8 |
| **M10** | runner.go option + defer + attach + 1 fixture | `internal/numind/biz/agent/runner.go`, `internal/numind/biz/agent/runner_test.go` | +14 + 50 = 64 | M7, M8 |
| **M11** | biz.go wire (4 lines + helper) | `internal/numind/biz/biz.go` | +10 | M7, M9, M10 |
| **M12** | S5 acceptance doc + run race+vet | `numind-server/docs/superpowers/qa/2026-05-21-agent-mode-narration-layer-s5-acceptance.md` | 200 | M1-M11 |

**Total**: ~2117 LOC across 19 files (13 new + 6 modified). Above spec's 1700 estimate; difference is test fixture density.

---

## 2. Wave Plan (S4 execution order)

```
Wave A (parallel, Tier 3, 3 implementer agents + 1 main-session yaml)
├── M1 (event.go)           ◄── agent narration-a1
├── M2 (error_translate.go) ◄── agent narration-a2
├── M3 (tool-display.yaml)  ◄── main session (just write yaml)
└── M4 (display.go)         ◄── agent narration-a4
└── synchronisation: all 3 agents commit + main commits yaml → 4 commits in main worktree

Wave B (serial, single agent)
└── M5 (streamer.go) — needs Event type from M1
   └── after M5 commits, run `go test ./internal/numind/biz/narration/... -race` → must PASS

Wave C (serial, single agent)
└── M6 (translator.go) — needs Renderer (M4), ClassifyError (M2), Event (M1)
   └── after M6 commits, run race+vet → must PASS

Wave D (serial, single agent)
└── M7 (provider.go) — needs Translator (M6), Streamer (M5)
   └── after M7 commits, run race+vet → must PASS
   └── 🔑 biz/narration package is now complete and isolated; reviewers can do package-level review

Wave E (parallel, Tier 3, 2 implementer agents + 1 main-session hooks)
├── M8 (hooks.go +2 fields) ◄── main session (3 lines + 1 test)
├── M9 (adapter.go emit)    ◄── agent narration-e9
└── M10 (runner.go option)  ◄── agent narration-e10
   └── M9 + M10 modify DIFFERENT files in biz/agent; disjoint per check
   └── after both commit, run `go test ./internal/numind/biz/agent/... -race` → must PASS

Wave F (serial)
└── M11 (biz.go wire) — needs all of biz/narration + adapter + runner
   └── after M11 commits, run full `go test ./... -race` + `go vet ./...` → must PASS

Wave G (serial)
└── M12 (S5 acceptance) — verification doc + final smoke
```

---

## 3. Tier 3 Disjoint File Verification

### Wave A disjoint check

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/numind/biz/narration/event.go,internal/numind/biz/narration/event_test.go" \
  "internal/numind/biz/narration/error_translate.go,internal/numind/biz/narration/error_translate_test.go" \
  "internal/numind/biz/narration/display.go,internal/numind/biz/narration/display_test.go"
# expected: exit 0 (M3 yaml file is main-session, not in the parallel set)
```

### Wave E disjoint check

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/numind/biz/agent/adapter_full_to_eino.go,internal/numind/biz/agent/adapter_full_to_eino_test.go" \
  "internal/numind/biz/agent/runner.go,internal/numind/biz/agent/runner_test.go"
# expected: exit 0 (M8 hooks.go is main-session)
```

Both Tier 3 sets exit-0 — confirmed disjoint. M3 + M8 are tiny edits handled inline by main session (no agent dispatch overhead).

---

## 4. Per-Task Implementation Brief

### M1 — event.go + event_test.go

Implement spec §2 verbatim. Tests in §10.0 (icon assignment table for all 6 states) + a simple `IsTerminal()` truth-table.

Acceptance: `go test ./internal/numind/biz/narration/ -run TestIcon -v` passes; package compiles standalone.

### M2 — error_translate.go + error_translate_test.go

Implement spec §4 verbatim. Tests in §10.2 (7-case table + raw-leak guard).

Acceptance: `go test ./internal/numind/biz/narration/ -run TestClassify -v` passes; 100% coverage on ClassifyError.

### M3 — configs/tool-display.yaml

Main-session direct write per spec §8. Validate YAML syntax with `gopkg.in/yaml.v3 || yamllint` (yamllint not available; use Go test in M4 to validate).

### M4 — display.go + display_test.go

Implement spec §3 + §3.1 (compilation algorithm with missingkey=zero, S2-D1). Tests in §10.1 (9 fixtures including missing-key-uses-default).

Acceptance:
- `go test ./internal/numind/biz/narration/ -run TestRender -v -race` passes
- `go test ./internal/numind/biz/narration/ -run TestNewRenderer -v -race` passes — including template-parse-error fixtures
- **MANDATORY fixture (S3 P0-1 fix)**: `TestNewRendererFromPath_RepoRootYAML` — calls `NewRendererFromPath("../../../configs/tool-display.yaml")` and asserts no error + all 6 built-in tools present in the renderer. This proves the production yaml file ships valid and loadable. If this test fails, the production server cannot boot.
- coverage ≥ 95%

### M5 — streamer.go + streamer_test.go

Implement spec §6 verbatim. Tests in §10.3 (7 fixtures including concurrent Send/CloseRun with -race).

**Sync gate (S3 P2-5 fix)**: Do NOT start M5 until Wave A is fully committed. Verify with `git log --oneline -5` and confirm 4 commits matching M1/M2/M3/M4 land before M5 implementer is dispatched.

Acceptance:
- `go test ./internal/numind/biz/narration/ -run TestMemStreamer -v -race` passes
- The `TestMemStreamer_ConcurrentSendClose_RaceFree` fixture must spawn ≥ 100 goroutines and not race-detect
- coverage ≥ 90%

### M6 — translator.go + translator_test.go

Implement spec §5 verbatim. Tests in §10 (yaml-hit path + fallback path + panic recovery + nowFunc injection).

Acceptance:
- `go test ./internal/numind/biz/narration/ -run TestTranslator -v -race` passes
- coverage ≥ 90%

### M7 — provider.go + provider_test.go

Implement spec §7 verbatim. Tests in §10.4 (6 fixtures including LoadOrStore race + Subscribe-dead-runID lazy-create per S2-D2).

Acceptance:
- `go test ./internal/numind/biz/narration/ -v -race` ALL fixtures pass (full package)
- coverage ≥ 90% on provider.go
- biz/narration package coverage ≥ 80% (S5 gate)

### M8 — hooks.go +2 fields

Main-session direct edit:
```go
NarrationProvider *narration.Provider  // #8: nil = no narration emit (legacy compat)
NarrationRunID    uint64                // #8: per-Run; set by runner.Run
```

Plus one test fixture in `hooks_test.go`:
```go
func TestRunHooks_DefaultNarrationFields_Nil(t *testing.T) {
	h := &RunHooks{}
	if h.NarrationProvider != nil { t.Fatal("default should be nil") }
	if h.NarrationRunID != 0 { t.Fatal("default should be 0") }
}
```

Acceptance: package compiles; existing hooks_test.go fixtures still pass.

### M9 — adapter_full_to_eino.go + tests

Implement spec §9.2 — replace existing `InvokableRun` body with the spec's version (effectiveErr branching, 3 emit sites, emitNarration helper). Add `encoding/json` and narration imports.

Tests: 4 new fixtures per spec §10.5.

Acceptance:
- `go test ./internal/numind/biz/agent/ -run TestAdapter -v -race` ALL existing fixtures still pass + 4 new fixtures pass
- coverage on adapter_full_to_eino.go does not drop below existing baseline

**Baseline recording (S3 P2-3 fix)**: BEFORE dispatching M9, main session runs `go test ./internal/numind/biz/agent/ -coverprofile=/tmp/adapter-baseline.out -run TestAdapter` and records the `adapter_full_to_eino.go` coverage % in the manifest `current_task` field as the M9 anchor (e.g., "M9 baseline adapter_full_to_eino.go=92.3%"). Reviewer B for M9 is responsible for comparing post-M9 coverage against this number and flagging any drop > 2 percentage points as P1.

### M10 — runner.go option + defer + attach

Implement spec §9.3 — add `narrationProvider` field, `WithNarrationProvider` option, register CloseRun defer immediately after `runStore.Create` (S1-D20), attach to effectiveHooks AFTER Registry auto-inject (S2-D3).

Tests: spec §10.6 fixture (real Provider, in-memory yaml, channel-close assertion).

Acceptance: `go test ./internal/numind/biz/agent/ -run TestRunner -v -race` passes; no regression on existing runner tests.

### M11 — biz.go wire

Implement spec §9.4 — `narration.NewProvider(Config{YAMLPath:"configs/tool-display.yaml", BufferSize:256, ToolNames:agentToolNames(registry)})` before `agent.NewAgentRunner(...)`, pass `agent.WithNarrationProvider(narrationProv)`. Add `agentToolNames` helper.

Acceptance:
- `go build ./...` succeeds
- `go vet ./...` clean
- `go test ./... -race` ALL packages pass
- **CONSTRAINT (S3 P1-2 fix)**: M11 does NOT add a `biz_test.go` that calls `NewBiz` directly. The boot-time `narration.NewProvider` validation is covered by M4's `TestNewRendererFromPath_RepoRootYAML` fixture. Adding a `NewBiz`-calling test in M11 would widen the test surface unnecessarily and introduce hidden CWD coupling. If reviewer suggests adding one, decline citing this line.

### M12 — S5 acceptance doc

Write `docs/superpowers/qa/2026-05-21-agent-mode-narration-layer-s5-acceptance.md` with:
- 4-fixture E2E walkthrough (see spec §11)
- Final coverage report (per-file + package)
- Race detector output
- Confirm 0 prod impact (config_prod.yaml zero diff, no migration, no tags, no /deploy-prod)
- Confirm manifest progress matches: total_tasks=12 == completed_tasks=12 == reviewed_tasks=12
- Mark ACCEPTED or NEEDS_REWORK

---

## 5. Per-Task Review Policy (NDF Rule 6: dual reviewer parallel)

After EACH Mn commit, dispatch TWO parallel Sonnet reviewers (`model: "sonnet"`):

**Reviewer A (Spec Compliance)** — does the impl match the spec? Are all spec'd test fixtures present? Does it correctly cite ADRs (S0-D, S1-D, S2-D) in comments where load-bearing?

**Reviewer B (Code Quality)** — gofmt-clean? go vet-clean? error handling correct (no swallowed errors)? Race-safety in concurrent code? Test independence (no shared state)? Comments explain WHY not WHAT?

Both PASS → update manifest `progress.reviewed_tasks += 1`; advance to next task.
Any P0/P1 → fix → re-run BOTH reviewers (full re-review, not just delta).

---

## 6. File Ownership Tables (per Wave, for ndf-check-disjoint)

### Wave A
| Agent | Files |
|---|---|
| narration-a1 (M1) | `internal/numind/biz/narration/event.go`, `internal/numind/biz/narration/event_test.go` |
| narration-a2 (M2) | `internal/numind/biz/narration/error_translate.go`, `internal/numind/biz/narration/error_translate_test.go` |
| main-session (M3) | `configs/tool-display.yaml` |
| narration-a4 (M4) | `internal/numind/biz/narration/display.go`, `internal/numind/biz/narration/display_test.go` |

Intersection check: only `narration-a1`, `narration-a2`, `narration-a4` participate in disjoint-set (M3 is single-file inline). All 3 sets are in different files within `biz/narration/`. **Exit 0 expected.**

### Wave E
| Agent | Files |
|---|---|
| main-session (M8) | `internal/numind/biz/agent/hooks.go`, `internal/numind/biz/agent/hooks_test.go` |
| narration-e9 (M9) | `internal/numind/biz/agent/adapter_full_to_eino.go`, `internal/numind/biz/agent/adapter_full_to_eino_test.go` |
| narration-e10 (M10) | `internal/numind/biz/agent/runner.go`, `internal/numind/biz/agent/runner_test.go` |

Intersection check: M9 and M10 are the parallel set. Both write to `biz/agent/` but different files. **Exit 0 expected.**

Note: M8 must complete BEFORE Wave E dispatch (M9 and M10 both reference `RunHooks.NarrationProvider` which M8 adds). Main session writes M8 + commits, then dispatches Wave E.

---

## 7. S5 Verification Strategy (Rule 10 mandate)

**Chosen approach**: **Go unit + integration tests with -race**.

**Rationale**:
1. Narration layer is pure backend biz logic — no UI surface to verify in browser.
2. SSE/WebSocket wire is NIS for this feature (#11) — no real consumer to exercise.
3. The 4 spec'd integration fixtures in adapter_full_to_eino_test.go + the runner-test channel-close fixture cover all user-observable paths (event counts, error message sanitization, channel lifecycle).
4. Future regression safety = automatic test re-run on every PR.

**Key user paths verified by tests**:

| Path | Test | Layer |
|---|---|---|
| Tool succeeds → learner sees "use" + "result" | `TestAdapter_NarrationEmits_UseResult` | integration (adapter+narration) |
| Tool errors → learner sees "use" + "error" with friendly Chinese msg, no raw err text | `TestAdapter_NarrationEmits_UseError` + `TestClassifyError_NeverLeaksRawErrText` | integration + unit |
| Hook rejects pre-execute → learner sees "rejected" only, no "use" | `TestAdapter_NarrationEmits_Rejected_NoUseEmitted` | integration |
| PostToolCall surfaces error from successful Execute → learner sees "use" + "error" (NOT "result") | `TestAdapter_NarrationEmits_PostErrUpgradesToError` | integration (P0-1 verification) |
| Run completes → per-runID channel closes via defer | `TestRunner_WithNarrationProvider_AttachesAndDefersCloseRun` | integration (runner+narration) |
| YAML missing entry → falls back to defaults block, no crash | `Test_Render_UnknownTool_FallsBackToDefaults` | unit (display) |
| Template missing-key → uses `default` func correctly (no "<no value>" leak) | `Test_Render_MissingMapKey_UsesDefault` | unit (display, S2-D1 verification) |
| Concurrent Send + CloseRun → no race, no panic | `TestMemStreamer_ConcurrentSendClose_RaceFree` | unit (streamer, S1-D19 verification) |
| Concurrent Emit same runID → unique ToolCallIDs | `TestProvider_NextCallID_LoadOrStoreRaceSafe` | unit (provider, S1-D18 verification) |

**NOT verified** (intentional defer):
- SSE/WebSocket consumer UI (#11)
- LLM-driven narration (#14 plugs real LLM into LLMFallback)
- Production environment

**Acceptance gate**: M12 records that all 9 user-path tests above PASS under `-race`, plus the package-level coverage targets.

---

## 8. Risks & Mitigations (S4-specific)

- **R-S4-1** (S3 P0-1 fix): `configs/tool-display.yaml` boot-load fails in CI due to CWD difference. Mitigation: M4 tests use `NewRendererFromBytes` for all logic tests (no disk I/O), AND add ONE disk-load fixture `TestNewRendererFromPath_RepoRootYAML` that resolves `../../../configs/tool-display.yaml` from the `internal/numind/biz/narration/` package (3 levels up to repo root). This validates the production yaml file is shippable and template-clean. M11's production wire continues to use the literal `"configs/tool-display.yaml"` relative path, relying on server boot CWD = repo root (matching the existing `config_dev.yaml` convention also done this way; deploy scripts honor this). M11 does NOT add a `biz_test.go` calling `NewBiz` — that would unnecessarily widen test surface; boot failure surfaces immediately at server start, which is sufficient. Production assurance comes from the M4 disk-load fixture proving the file is loadable + valid.

- **R-S4-2**: Eino's ReAct loop may invoke tools concurrently within a single Run. The spec's `nextCallID(runID)` uses `sync.Map.LoadOrStore` which is race-safe (S1-D18). Mitigation already covered by `TestProvider_NextCallID_LoadOrStoreRaceSafe` running under `-race`.

- **R-S4-3**: M9 modifies the existing `InvokableRun` body wholesale; existing fixtures in `adapter_full_to_eino_test.go` may break if they depend on internal ordering. Mitigation: run existing tests BEFORE applying M9 changes (baseline), then re-run after to confirm new fixtures pass without regression. If breakage occurs, treat as P0 and refine emit-site placement.

- **R-S4-4**: Merge conflicts with parallel sessions #6/#7/#9 on `hooks.go` (M8) and `biz.go` (M11). Mitigation: NDF v2 ndf-done is atomic; conflicts surface at S6 merge step and are resolved by main session.

- **R-S4-5**: `agentToolNames(registry)` called before tools are loaded → empty `ToolNames`, no missing-key warnings. Mitigation: this is documented as best-effort in S2-D4; functionality (yaml lookup + fallback) is unaffected.

---

## 9. Manifest Progress Tracking

After EACH M task commits AND both reviewers PASS:

```bash
# update .ndf/manifest.yaml — agent-mode-narration-layer entry
progress:
  total_tasks: 12
  completed_tasks: <N>       # increment after commit
  reviewed_tasks: <N>        # increment after both reviewers PASS
  current_task: 'M<N+1>: <description>'
```

After M12 (S5 acceptance) and both reviewers PASS:
- `total_tasks: 12 == completed_tasks: 12 == reviewed_tasks: 12`
- `current_task: 'S5 ACCEPTED — ready for S6 ndf-done merge'`
- Manifest stage: S3 → S5 (skip S4 marker; S4 is a wave-based execution stage, not a documented gate)

**Task numbering authority (S3 P2-1 fix)**: The M-numbers in this plan (§1) supersede the spec §12 numbering. Manifest `current_task` field uses plan M-numbers. Spec §12 was a preview; plan §1 is canonical for execution tracking.

---

## 10. Done Criteria (S5 gate, restated)

All from spec §13:
- `go test ./internal/numind/biz/narration/... -race -cover` → biz/narration ≥ 80%; display ≥ 95%; error_translate ≥ 95%
- `go test ./internal/numind/biz/agent/... -race` → no regression on existing baselines
- `go vet ./...` clean across full repo
- `configs/tool-display.yaml` valid (parses + all built-in tools have entries)
- 4 adapter integration fixtures + 1 runner fixture PASS
- 0 modification: `config_prod.yaml`, `migrations/`, `credit_*` tables
- 0 push of `feature/*` to GitHub (pre-push hook holds)
- 0 `git tag v*`, 0 `/deploy-prod`
- Manifest: 12 == 12 == 12

S5 acceptance doc (M12) captures evidence for each bullet.

---

## 11. S6 Merge Strategy (sneak preview, not S3 scope)

After M12 ACCEPTED:

```bash
# main session:
bash numind-server/scripts/ndf/ndf-done.sh
```

If ndf-done fails (state.json clobbered by parallel session, etc.), fallback:

```bash
cd numind-server
git checkout develop && git pull
git merge --no-ff feature/agent-mode-narration-layer
# expected: conflicts on hooks.go (with #6 / #7 / #9 also adding RunHooks fields)
# and possibly biz.go (with #6 / #7 / #9 also adding agent.With* options)
# resolve by accepting all parties' fields; nothing logically conflicts
git push origin develop
git worktree remove --force /private/tmp/wt-agent-mode-narration-layer-numind-server
git branch -D feature/agent-mode-narration-layer
echo '{"version":"ndf-v2","active_feature":null,"active":null}' > .ndf/state.json
```

Post-merge artifacts:
- `docs/agent-mode/deploy-checklist-feature-8.md` — runbook for `/deploy-dev server` (no migration since #8 has no DB changes)

**S6 merge conflict guide (S3 P1-1 fix — state.go array assertion is non-trivial)**:

`state.go` line 36-40 has a compile-time assertion:
```go
var _ = [12]TerminalReason{
    TerminalCompleted, TerminalBlockingLimit, ...
}
```

If parallel session #6 (permission-pipeline) adds `TerminalPermissionDenied` and #9 (compact) adds `TerminalCompactFailed`, all three sessions independently expand this array:
- #6: `[13]TerminalReason{..., TerminalPermissionDenied}`
- #9: `[13]TerminalReason{..., TerminalCompactFailed}`
- #8 (us): no change to state.go (we don't add terminal reasons)

After all 4 land on develop in some order, the array size may show as `[13]` or `[14]` with potentially missing entries, producing a `cannot use [N]TerminalReason{...} as [M]TerminalReason value` compile error.

**S6 merge protocol for state.go**:
1. After `git merge --no-ff feature/agent-mode-narration-layer` (or any other), IMMEDIATELY run `go vet ./internal/numind/biz/agent/...` as the first compile check.
2. If `state.go` fails compilation:
   - Read state.go and count actual `TerminalReason` constants declared
   - Update `[N]TerminalReason{...}` literal to match the count and include ALL declared constants
   - Same for `[7]ContinueReason{...}` if any session added a continue reason
3. Re-run `go vet`; if clean, proceed with `git push`.

#8 does not introduce this conflict (we touch hooks.go, not state.go), but inheriting #6/#9 merge order means we may face it during our S6. Plan ahead by NOT merging if other sessions are also mid-merge — coordinate via shared status.
