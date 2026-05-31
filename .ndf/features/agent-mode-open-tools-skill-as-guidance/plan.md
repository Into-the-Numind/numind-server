# NDF S3 Plan · `agent-mode-open-tools-skill-as-guidance`

**Stage**: S3 (task plan) · **Date**: 2026-06-01 · Drives S4. Authoritative design = `spec.md` (incl. §9b refinements).

---

## Pre-flight (before T1 coding)
- `cd` worktree `/private/tmp/wt-agent-mode-open-tools-skill-as-guidance-numind-server`; `git fetch origin develop`.
- Check `agent-tool-schema-infra` (parallel, S3): it kept `InputSchema()=json.RawMessage`, so overlap = `tool_read_skill.go` only (this feature deletes it). If it has landed on develop, `git merge origin/develop` into the feature branch and reconcile `tool_read_skill.go` deletion + any `InputSchema` content it added to `tool_use_skill.go`/skill tools. If not landed, proceed; reconcile at S6.
- `task lint` + `go test ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...` baseline green before touching anything.

## Task atomicity rationale
Go compiles as a unit, so the refactor must land in compile-green increments. Two tasks, each independently buildable + testable + reviewable:
- **T1 = Moves A+B** (delete dead permission layer + full-open registration). Lower risk (mostly deletion + one registration swap). `use_skill`/`read_skill` still exist as two tools after T1.
- **T2 = Moves C+D** (merge → `load_skill` + `allowed_tools`→recommendation + unified catalog + string renames). Higher touch.
- **T3 = S5 validation strategy** (planning artifact, Rule 10).

---

## T1 — Delete dead permission layer + full-open registration (Moves A+B)

**Goal**: every agent gets all (non-stub) registered tools; the dead `UseSkillTurnScope` machinery and `turn.AllowedTools` are gone. `use_skill`/`read_skill` remain as-is (merge is T2).

**Changes**:
1. `tool_full.go`: add `func FullyEnabledToolConfig() ToolConfig { return ToolConfig{EnableSandbox:true, EnableImageGen:true, EnableSkills:true} }`.
2. `runner.go` + `runner_runstream.go` (BOTH):
   - Replace the `for _, name := range req.ToolNames { GetTool(name) ... }` base loop with: iterate `r.registry.ListAllTools()`, skip `!ft.IsEnabled(FullyEnabledToolConfig())` (drops `document_generate`), skip `use_skill`/`read_skill` names (still handled by their existing conditional blocks for now), register the rest. Keep compactv2 wrap.
   - **Delete** the `extraTools` union block (collect skill `allowed_tools` → register). Now subsumed by full registration.
   - **Delete** `queryCtx = WithAgentBaseToolNames(...)`. **Keep** `WithFullToolMap`.
   - Keep the existing `use_skill` conditional injection + `read_skill` (it's in ListAllTools via factory; ensure it still registers — it will, since `IsEnabled(EnableSkills:true)`=true; so actually `read_skill` is covered by the base loop — only `use_skill` needs its conditional since it should stay binding-gated in the interim). Net: in the base loop also skip `use_skill`; let its existing conditional block register it when bindings exist.
3. `tool_use_skill.go`: delete `UseSkillTurnState.AllowedTools` field + `make(...AllowedTools...)` in `NewUseSkillTurnState`; delete the `allowed_tools` merge in `useSkillTool.Execute` (lines ~304-317) + the `allowed_tools_added` ack field; delete `WithAgentBaseToolNames`/`AgentBaseToolNamesFromCtx`/`ctxKeyAgentBaseToolNamesT`/`CtxKeyAgentBaseToolNames`.
4. `permission/validators/use_skill_turnscope.go` + `use_skill_turnscope_test.go`: **delete**.
5. `eino_skill_integration_test.go`: delete scenario (c) (dead validator). Keep (a)(b)(d) but drop the `turn.AllowedTools` assertion in (a) (field gone) → assert `PendingSkills`/`InvocationCount` instead. (Full move to package-agent test happens in T2 when use_skill→load_skill.) Interim: keep file compiling in `validators` pkg minus (c) + minus the import of the deleted validator.
6. Add `runner_fullopen_test.go` (both paths): a no-binding agent's Eino tool set ⊇ {`bash_exec`,`image_gen`,`run_python`} and ∌ {`document_generate`}.

**Acceptance**: `go build ./...`; `go test ./internal/numind/biz/agent/... ./internal/numind/biz/permission/...` green; `task lint` clean. AC-1, AC-8 (partial: AllowedTools/WithAgentBaseToolNames/validator gone). `grep -rn UseSkillTurnScope\|WithAgentBaseToolNames\|AllowedTools.*turn` → no live refs.

**Verify both paths**: explicit assertion that `RunStream` (live) registers full-open identically to `Run`.

---

## T2 — Merge use_skill + read_skill → load_skill + allowed_tools recommendation + unified catalog (Moves C+D)

**Goal**: one `load_skill` tool + one unified catalog; `allowed_tools` rendered as a recommendation; all prompt/baseline strings reference `load_skill`.

**Changes**:
1. `tool_load_skill.go` (NEW): `loadSkillTool{BaseTool; registry skills.Registry}`, `NewLoadSkillTool(reg)`, `LoadSkillToolName="load_skill"`, `IsEnabled=cfg.EnableSkills`, input `{name}`. Execute per spec §2: DB-first (`turn.SkillByName`) → wrap `<system-reminder>` + append `💡 推荐配合使用的工具：…` when `allowed_tools` non-empty + PendingSkills + cap; else disk (`registry.Get`) → `readSkillOutput` shape + size cap + soft error listing DB∪disk names; collision WARN.
2. `tool_read_skill.go`: **delete** (move `readSkillMaxBodyBytes`, `availableSkillNames`, `readSkillOutput`, disk-read logic into `tool_load_skill.go`).
3. `tool_use_skill.go`: delete `useSkillTool` type + `NewUseSkillTool` + `UseSkillToolName` + `Description/InputSchema/Execute`. **Keep** `UseSkillTurnState` (now without AllowedTools), `PendingSkill`, `NewUseSkillTurnState`, `UseSkillTurnCapDefault`, `WithUseSkillTurn`/`UseSkillTurnFromCtx`, `jsonErr`. (Grep `WithSkillBindings`/`CtxKeySkillBindings`; delete if no consumer.)
4. `factory_platform.go`: drop `NewUseSkillTool()` + conditional `NewReadSkillTool(reg)`; add conditional `NewLoadSkillTool(reg)` (when `reg != nil`). Metadata: drop `use_skill`/`read_skill`, add `load_skill` (`Category:"技能"`, `RiskLevel:"safe"`).
5. `runner.go` + `runner_runstream.go` (BOTH): replace the `use_skill` conditional injection (from T1) with `load_skill` conditional registration per spec D5 (`platformSkillRegistry != nil || useSkillTurnState != nil`) + F5 log.Error guard. In the base full-open loop, change the skip from `use_skill`/`read_skill` to `load_skill` (load_skill handled by the conditional). Replace catalog injection: `buildUnifiedSkillCatalog(skills, r.platformSkillRegistry)` at runner.go:525 + the V2 site 717-732 (drop the separate `RenderSkillCatalog` concat), and at runner_runstream.go:171 (verify prompt-assembly flow per spec §9b F1).
6. `skill_catalog.go`: add `buildUnifiedSkillCatalog(dbSkills []model.Skill, reg skills.Registry) string` (spec §3); `read_skill`→`load_skill` in header/footer; keep/retire `RenderSkillCatalog` + `buildSkillCatalogBlock` (delete if no remaining caller).
7. String renames `read_skill`→`load_skill` (+ param `skill_name`→`name`): `output_tools_priority_prompt.go` (EN+中文 STEP A), `tool_run_python.go` Description, `tool_create_html.go`, `tool_create_png_chart.go`, `biz.go` log strings.
8. `student_run_lifecycle.go`: `safeToolBaseline` `read_skill`→`load_skill`; `categoryToTools["enable_skills"]` `{read_skill,run_python}`→`{load_skill,run_python}`.
9. `tool-display.yaml`: rename `use_skill` entry key → `load_skill` (verb/templates cover DB+disk); keep `use_skill` tombstone entry (+ add `read_skill`) for historical narration + F2 test.
10. Tests: NEW `tool_load_skill_test.go` (DB-load wrap+recommendation, disk-load shape, DB-first collision, cap, miss-lists-names, Chinese name) — migrate eino (a)(b)(d) here; update `skill_progressive_loader_regression_test.go` (read_skill→load_skill, no use_skill/read_skill/invoke_skill), `skill_catalog_test.go` (F3), `narration/use_skill_template_test.go` (F2); add `tool_flag` inert test (D6) + prompt-string test (OutputToolsPriorityAddendum + skillCatalogHeader contain `load_skill`, not `read_skill`). Delete the now-empty/duplicative `eino_skill_integration_test.go` if all scenarios migrated.

**Acceptance**: `go build ./...`; `go test ./internal/numind/...` green; `task lint` clean. AC-3..AC-8 + invariants I1-I7. `grep -rn '"use_skill"\|"read_skill"\|NewUseSkillTool\|NewReadSkillTool' internal/ --include=*.go` → only tombstone/historical (yaml + narration test). Regression test green.

---

## T3 — S5 validation strategy (Rule 10; detail in `s5-strategy.md`)
Defines the S5 verification (Playwright E2E mandatory + Go suite), key user paths, and credentials. Reviewed by the S3 gate reviewer alongside this plan.

---

## Sequencing & parallelism
- **Serial T1 → T2** (Tier 4): both touch `runner.go`/`runner_runstream.go`/`tool_use_skill.go` — overlapping files, T2 depends on T1's compile-green base. No Tier-3 parallel split.
- After each task: commit → **parallel** 2× Sonnet reviewer (spec-compliance + code-quality) → fix P0/P1 (P2 inline) → `reviewed_tasks += 1` → next.
- `total_tasks: 2` (T3 is a planning artifact, not a code task — not counted in code progress).
- Rule 11 N/A (architecture decision, not bug-from-customer) — no `test(repro)` first-commit required.
