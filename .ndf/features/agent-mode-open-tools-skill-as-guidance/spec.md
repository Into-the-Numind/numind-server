# NDF S2 Spec · `agent-mode-open-tools-skill-as-guidance`

**Stage**: S2 (technical design) · **Date**: 2026-05-31
**Resolves S1 gate** (CHANGES-REQUIRED → all P1/P2 addressed below, see §9)

---

## 0. Design decisions (authoritative)

| ID | Decision | Rationale |
|---|---|---|
| D1 | **Full-open filter** = register every tool from `registry.ListAllTools()` where `t.IsEnabled(fullyEnabledToolConfig)` is true, `fullyEnabledToolConfig = ToolConfig{EnableSandbox:true, EnableImageGen:true, EnableSkills:true}`. | Auto-excludes the hard stub `document_generate` (`IsEnabled` returns `false` unconditionally — [tool_document_generate.go:47](internal/numind/biz/agent/tool_document_generate.go:47)); includes `bash_exec`/`run_python`/`image_gen`/skills + all default-`true` base tools. Self-maintaining: any future hard-stub auto-excluded. **Resolves S1-P1-1.** |
| D2 | **`load_skill`** is a NEW tool (`tool_load_skill.go`, `LoadSkillToolName="load_skill"`). Resolution: DB-bound skill (`turn.SkillByName[name]`) FIRST, disk `registry.Get(name)` SECOND, miss→soft-error ack listing available names. | Single tool over two backends; DB business skills are agent-specific so they win (a parent who named a skill identically to a platform skill intends their own). |
| D3 | **Name-collision policy**: on DB/disk same-name, DB wins + WARN log. The unified catalog renders disk skills under their plain name; if a DB skill shadows a disk skill name, the catalog lists the DB one only and WARN-logs the shadow at render time. | Predictable, observable. No silent platform-skill corruption beyond a logged warning. **Resolves S1-P2 collision.** |
| D4 | **`use_skill` + `read_skill` removed** from factory + `safeToolBaseline` + `categoryToTools`; `load_skill` takes their slot. No runtime aliases. | LLM has no persisted memory across runs — it learns the tool set fresh each run from the system prompt + tool schema. |
| D5 | **`load_skill` registration** (in the full-open loop): register it only when the agent has something to load — `r.platformSkillRegistry != nil` (disk skills exist) OR `useSkillTurnState != nil` (agent has DB bindings). Otherwise skip (keeps the tool list clean for skill-less agents). All OTHER tools come from the filtered `ListAllTools()`. | Mirrors today's conditional `use_skill` injection; avoids a dead `load_skill` in a skill-less agent's list. |
| D6 | **`ToolFlag` validator stays wired, untouched.** Add a unit test pinning it returns `Passthrough` for `bash_exec`/`image_gen`/`load_skill`/`run_python` under a category-key `tool_flags` config. | Inert for category configs (keys are `code_sandbox`/`media`/`enable_skills`, never raw tool names — [tool_flag.go:43](internal/numind/biz/permission/validators/tool_flag.go:43)); preserved as the future per-tool gate hook the user deferred. **Resolves S1-P2 ToolFlag.** |
| D7 | **`UseSkillTurnState` struct keeps its name**, loses `AllowedTools`. Fields after: `InvocationCount, Cap, PendingSkills, SkillByID, SkillByName`. | Internal struct; rename = pure churn. Bounded diff for a security-sensitive change. |
| D8 | **`allowed_tools` → recommendation**: kept in DB; on `load_skill` of a DB skill with non-empty `allowed_tools`, append `\n\n💡 推荐配合使用的工具：<a, b, c>` INSIDE the `<system-reminder>` wrapper. No enforcement, zero migration. | |

---

## 1. File-level change manifest (numind-server only)

**Legend**: ✏️ edit · ➕ new · ❌ delete · 🔁 both runner paths

| File | Action | What |
|---|---|---|
| `internal/numind/biz/agent/tool_load_skill.go` | ➕ | New `load_skill` FullTool (merges use_skill DB-lookup + read_skill disk-read; DB-first resolution; system-reminder wrap; allowed_tools recommendation; cap via turn state). |
| `internal/numind/biz/agent/tool_use_skill.go` | ✏️ | Keep `UseSkillTurnState` (− `AllowedTools`), `PendingSkill`, ctx helpers `WithUseSkillTurn`/`UseSkillTurnFromCtx`, `WithSkillBindings`/`SkillBindingsFromCtx` (if still used), `NewUseSkillTurnState`, `UseSkillTurnCapDefault`. **Delete** the `useSkillTool` type + `NewUseSkillTool` + `UseSkillToolName` + `WithAgentBaseToolNames`/`AgentBaseToolNamesFromCtx`/`CtxKeyAgentBaseToolNames`. (File becomes "skill turn-state + ctx helpers"; consider renaming later — out of scope.) |
| `internal/numind/biz/agent/tool_read_skill.go` | ❌ | Logic absorbed into `tool_load_skill.go` (disk read + size cap + soft-error + `availableSkillNames`). |
| `internal/numind/biz/agent/factory_platform.go` | ✏️ | Replace `NewUseSkillTool()` + conditional `NewReadSkillTool(reg)` with conditional `NewLoadSkillTool(reg)` (registered when `reg != nil`; DB-only agents still get it via runner D5 even if reg nil — see note). Update metadata: drop `use_skill`/`read_skill`, add `load_skill`. |
| `internal/numind/biz/agent/runner.go` | ✏️🔁 | (a) full-open base-tool loop (D1); (b) delete `extraTools` union block; (c) delete `WithAgentBaseToolNames` injection; (d) `load_skill` conditional registration (D5); (e) PendingSkills consumption unchanged; (f) catalog: replace `buildSkillCatalogBlock` + `RenderSkillCatalog` dual-injection with ONE `buildUnifiedSkillCatalog(dbSkills, reg)`. |
| `internal/numind/biz/agent/runner_runstream.go` | ✏️🔁 | Identical (a)-(f) as runner.go. **LIVE path.** |
| `internal/numind/biz/agent/skill_catalog.go` | ✏️ | Add `buildUnifiedSkillCatalog(dbSkills []model.Skill, reg skills.Registry) string` — one `## 可用技能` block listing DB-bound + disk skills, teaching `load_skill({name})`. Update `skillCatalogHeader`/`Footer`: `read_skill`→`load_skill`. Keep `RenderSkillCatalog` only if still referenced, else fold in. |
| `internal/numind/biz/agent/output_tools_priority_prompt.go` | ✏️ | `read_skill`→`load_skill` in both EN + 中文 sections (Layer 2 STEP A). **Load-bearing — injected into every system prompt.** |
| `internal/numind/biz/agent/student_run_lifecycle.go` | ✏️ | `safeToolBaseline`: `read_skill`→`load_skill`. `categoryToTools["enable_skills"]`: `{read_skill, run_python}`→`{load_skill, run_python}`. |
| `internal/numind/biz/agent/tool_run_python.go` | ✏️ | Description string mentions `read_skill({"skill_name":...})` → `load_skill({"name":...})`. |
| `internal/numind/biz/agent/tool_create_html.go`, `tool_create_png_chart.go` | ✏️ | Descriptions mention `read_skill → run_python` → `load_skill → run_python`. |
| `internal/numind/biz/biz.go` | ✏️ | Log strings referencing `read_skill` → `load_skill` (cosmetic). Factory wiring: `NewPlatformToolFactoryWithSkills` unchanged signature (still passes reg). |
| `internal/numind/biz/permission/validators/use_skill_turnscope.go` | ❌ | Dead validator (never wired — verified). |
| `internal/numind/biz/permission/validators/use_skill_turnscope_test.go` | ❌ | Tests for the dead validator. |
| `internal/numind/biz/permission/validators/eino_skill_integration_test.go` | ❌→➕ | Delete (scenario c tests the dead validator; a/b/d test the tool). Re-create equivalent (a)(b)(d) as `tool_load_skill_test.go` in package `agent`. |
| `internal/numind/biz/agent/tool_full.go` | ✏️ | Add exported helper `FullyEnabledToolConfig()` (or a package const) = `ToolConfig{true,true,true}` for the full-open filter (single source of truth). |
| `internal/numind/biz/agent/skill_progressive_loader_regression_test.go` | ✏️ | Update assertions: `read_skill`→`load_skill` in categoryToTools / safeToolBaseline / OutputToolsPriorityAddendum / skillCatalogHeader; assert NO `use_skill`/`read_skill`/`invoke_skill` survive. Intent (single-loop, no double-LLM) preserved. |
| `configs?/tool-display.yaml` (locate exact path in S4) | ✏️ | Rename `use_skill` entry key → `load_skill`; update verb/templates to cover DB+disk. Keep `use_skill` (+ add `read_skill`) tombstone entries for historical `agent_run` narration (D-narration). |
| `internal/numind/biz/agent/tool_load_skill_test.go` | ➕ | DB-load, disk-load, DB-first collision, system-reminder wrap, allowed_tools recommendation render, cap exhaustion, Chinese name round-trip. |
| `internal/numind/biz/agent/runner_*_test.go` | ✏️/➕ | Full-open registration assertion (both paths): a no-skill agent's Eino tool set includes `bash_exec`/`image_gen` and EXCLUDES `document_generate`. |

> Note on D5 + factory: simplest is to register `load_skill` in the factory when `reg != nil` (like read_skill). For DB-only agents where `reg == nil`, the runner can still expose `load_skill` from the registry IF it's there; if the factory didn't register it (reg nil), a DB-only deployment loses load_skill. In practice prod always has a skills registry, so `reg != nil` holds. S4: register `load_skill` in factory whenever the factory is the `WithSkills` variant; the runner's D5 condition then just decides exposure. Confirm prod always uses `NewPlatformToolFactoryWithSkills`.

---

## 2. `load_skill` tool design (`tool_load_skill.go`)

```
type loadSkillTool struct {
    BaseTool
    registry skills.Registry   // disk skills; may be nil
}
func NewLoadSkillTool(reg skills.Registry) FullTool
const LoadSkillToolName = "load_skill"

Name() = "load_skill"
IsReadOnly() = true ; IsEnabled(cfg) = cfg.EnableSkills   // so full-open filter includes it
InputSchema: { "name": string (required) }   // unified param name
Description: "Load a skill's guidance into the conversation. Skills are listed in the
  '可用技能' section. For structured files (xlsx/docx/pptx/pdf) load the matching skill then
  write Python per its guidance and run_python. Input {\"name\": string}."
```

**Execute(ctx, input)** (never returns non-nil Go error — soft-error pattern):
1. Unmarshal `{name}`; empty → soft-error ack.
2. `turn, hasTurn := UseSkillTurnFromCtx(ctx)`.
3. If `hasTurn` and `turn.InvocationCount >= turn.Cap` → graceful "已达本轮技能调用上限" ack (count NOT bumped). (Cap only when turn state present — DB-bound agents; disk-only agents have no cap, same as today's read_skill.)
4. **Resolve DB-first**: if `hasTurn`, `sk, ok := turn.SkillByName[name]`. If ok && sk != nil:
   - validate `IsActive` / `BodyMd != ""` (soft-error otherwise).
   - body = `BodyMd` + (if `allowed_tools` non-empty & valid JSON) `\n\n💡 推荐配合使用的工具：<join(", ")>`.
   - wrap: `<system-reminder>\n以下是技能 '%s' 的详细指引（v%d）...\n\n%s\n</system-reminder>`.
   - append `turn.PendingSkills`; `turn.InvocationCount++`.
   - Langfuse span (reuse use_skill pattern).
   - return ack `{status:"loaded", source:"db", skill_name, skill_version, body:wrapped, recommended_tools:[...], turn_invocation, turn_cap, message}`.
5. **Else resolve disk**: `registry.Get(name)`:
   - not found → soft-error ack listing `availableSkillNames(registry)` ∪ DB skill names (so the LLM sees ALL valid names).
   - read `<RootDir>/SKILL.md`, enforce `readSkillMaxBodyBytes` (4096), Langfuse span.
   - (optional) if `hasTurn`: `turn.InvocationCount++` (count disk loads against cap too, for the bound-agent case) — **D-cap-disk**: only bump when hasTurn; keeps disk-only agents uncapped.
   - return ack `{status:"loaded", source:"disk", name, description, body_md, max_runtime_seconds, categories, message}`. (Body NOT system-reminder-wrapped for disk skills — preserves read_skill's exact output contract for I3; the SKILL.md body is the payload the LLM consumes verbatim.)
6. Collision WARN: if both `turn.SkillByName[name]` and `registry.Get(name)` resolve, log WARN "DB skill shadows platform skill %q" before returning the DB one.

**Backward-compat note**: step 5's disk ack mirrors `readSkillOutput` shape exactly (`name/description/body_md/max_runtime_seconds/categories`) so the LLM's downstream `run_python` flow is byte-identical to today (I3).

---

## 3. Unified catalog (`buildUnifiedSkillCatalog`)

One `## 可用技能` block for §2. Inputs: this agent's bound DB skills (`[]model.Skill`) + disk `skills.Registry`.

```
## 可用技能（Skills）

需要某个技能时，用 load_skill({"name":"<技能名>"}) 把它的指引载入对话。
生成 PPT/Excel/Word/PDF 等结构化文件：先 load_skill 读指引，按指引写 Python，再 run_python 执行。
每轮最多调用 <Cap> 次技能。

可用技能：
- **<DB skill name>**：<description>
  - 何时使用：<when_to_use>          (if present)
- `<disk skill name>`: <description>   (skipped if shadowed by a DB skill of same name → WARN)

工作流（结构化文件）：
1. load_skill({"name":"<选定>"}) → 看返回指引
2. 按指引写完整 Python
3. run_python({"code":"...", "input_files":[...]})
```

- Deterministic order (DB skills by binding order then disk skills alpha — keep cache-friendly).
- Reuse existing soft caps (desc 200 chars, total 2000 chars).
- Replaces BOTH `buildSkillCatalogBlock` (runner.go:1534) and `RenderSkillCatalog` (skill_catalog.go) at the injection sites (runner.go:525/717-732 and runner_runstream.go:171). Old renderers deleted or made to delegate.

---

## 4. Full-open registration (both runner paths)

Replace (in BOTH `runner.go::Run` ~787-861 and `runner_runstream.go::RunStream` ~303-359):

```go
// OLD: for _, name := range req.ToolNames { GetTool(name) ... } + extraTools union + use_skill conditional
// NEW:
fullCfg := FullyEnabledToolConfig()             // {true,true,true}
for _, ft := range r.registry.ListAllTools() {
    if !ft.IsEnabled(fullCfg) {                 // excludes document_generate (hard stub)
        continue
    }
    if ft.Name() == LoadSkillToolName {         // D5: gate load_skill exposure
        continue                                // handled below
    }
    base := adaptFullToEinoTool(ft, effectiveHooks)
    if useCompactV2 { base = wrapToolWithV2ArtifactProcessing(base, ft.Name(), run.ID, ...) }
    einoTools = append(einoTools, base)
    toolMap[ft.Name()] = ft
}
// load_skill: expose only if there is something to load
if (r.platformSkillRegistry != nil || useSkillTurnState != nil) {
    if ft, ok := r.registry.GetTool(LoadSkillToolName); ok {
        einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
        toolMap[LoadSkillToolName] = ft
    }
}
// compactv2 read_tool_artifact: unchanged (still always-inject under useCompactV2)
queryCtx = WithFullToolMap(queryCtx, toolMap)   // KEEP
// DELETE: queryCtx = WithAgentBaseToolNames(...)
```

- `req.ToolNames` / `basicToolNames`: no longer used for registration. Leave the field flowing (harmless) but remove its only consumer (`WithAgentBaseToolNames`). The `len(einoTools)==0` guard still works (never empty now).
- `useSkillTurnState` construction (skill loading block) unchanged EXCEPT the struct no longer has `AllowedTools` and the union-collection is deleted.

---

## 5. Deletion list (dead code, low risk)

- `permission/validators/use_skill_turnscope.go` + `_test.go` (never wired — confirmed grep + biz.go:316-326).
- `eino_skill_integration_test.go` (validators pkg) — replaced by `tool_load_skill_test.go` (agent pkg).
- `UseSkillTurnState.AllowedTools` field + all reads/writes.
- `WithAgentBaseToolNames` / `AgentBaseToolNamesFromCtx` / `CtxKeyAgentBaseToolNames` / both injections.
- `useSkillTool` type + `NewUseSkillTool` + `UseSkillToolName` (replaced by load_skill).
- `readSkillTool` type + `NewReadSkillTool` (absorbed).
- runner `extraTools` union blocks (both paths).

KEEP: `WithFullToolMap` (live, used by permission/compliance WrapHooks), `WithSkillBindings`/`CtxKeySkillBindings` (if any consumer remains — else delete in S4 after grep), the whole 7-validator chain incl. `ToolFlag`.

---

## 6. Backward compatibility

- **`tool_flags` JSON**: unchanged on disk. `enable_skills` key now expands to `{load_skill, run_python}`. Existing agents with `enable_skills:true` keep working (just a different tool name behind the same flag). Zero migration.
- **DB `skill.allowed_tools`**: unchanged; semantics shift to recommendation (D8).
- **Historical `agent_run` narration**: old `use_skill`/`read_skill` tool-call rows render via tombstone `tool-display.yaml` entries (keep both keys) or `defaults` fallback. Acceptable (display-only).
- **`tool_definition` DB upsert**: factory metadata drives upsert; `use_skill`/`read_skill` rows go stale but harmless (LoadAll re-upserts `load_skill`). No cleanup needed (or a follow-up micro).

---

## 7. Test plan (Go)

| Test | Asserts | AC |
|---|---|---|
| `tool_load_skill_test.go::DBSkill_Loads` | DB skill body wrapped in system-reminder, Chinese name round-trips, recommended-tools line present when allowed_tools non-empty | AC-3, AC-4 |
| `..::DiskSkill_Loads` | disk SKILL.md returned in readSkillOutput shape (I3) | AC-5 |
| `..::DBFirst_Collision_Warn` | same-name DB+disk → DB wins | D3 |
| `..::Cap_Exhausted_GracefulAck` | over-cap → error ack, count not bumped | AC-6 |
| `..::Miss_ListsAllNames` | unknown name → soft error listing DB ∪ disk names | — |
| `runner_fullopen_test.go` (both paths) | no-skill agent's tool set ⊇ {bash_exec, image_gen} and ∌ {document_generate, use_skill, read_skill} | AC-1, AC-8 |
| `tool_flag` new test | Passthrough for bash_exec/image_gen/load_skill/run_python under `{code_sandbox:true, enable_skills:true}` | AC-8 (D6) |
| `skill_progressive_loader_regression_test.go` (updated) | load_skill in catalog/addendum/baseline; no use_skill/read_skill/invoke_skill | AC-7 |
| prompt-string test | OutputToolsPriorityAddendum + skillCatalogHeader contain "load_skill", not "read_skill" | S1-P2 |

## 8. Out of scope / deferred
doc-gen防错; bash/run_python agent gate (ToolFlag is its future hook); admin/web-v3 UI copy ("allowed_tools"→"recommended"); `tool_definition` stale-row cleanup (optional micro); `UseSkillTurnState`→`SkillTurnState` rename.

## 9. S1 gate findings → resolution
- **S1-P1-1** (ListAllTools exposes stubs) → **D1** (`IsEnabled(allTrue)` filter excludes `document_generate`).
- **S1-P1-2** (read_skill hardcoded in prompt surfaces) → §1 manifest now lists `output_tools_priority_prompt.go`, `skill_catalog.go`, `tool_run_python.go`, `tool_create_html.go`, `tool_create_png_chart.go`, biz.go logs; §7 adds prompt-string test.
- **S1-P2** ToolFlag-inert AC → **D6** + §7 tool_flag test.
- **S1-P2** catalog/addendum load_skill AC → §7 prompt-string test.
- **S1-P2** DB/disk collision → **D3**.
- **S1-P2** document_generate risk → **D1** turns it from risk into a handled exclusion.
