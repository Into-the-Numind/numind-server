# NDF S1 Proposal · `agent-mode-open-tools-skill-as-guidance`

**Stage**: S1 (proposal + PRD)
**Date**: 2026-05-31
**Builds on**: `requirement.md` (S0, PASS-WITH-NITS)

---

## 0. ⚠️ S1 re-baseline — code reality vs the S0 mental model

S0 was drafted from the cold-start brief. A full read of the live code (`runner.go`, `runner_runstream.go`, `tool_use_skill.go`, `tool_read_skill.go`, `use_skill_turnscope.go`, `tool_flag.go`, `factory_platform.go`, `student_run_lifecycle.go`, `skill_catalog.go`, both test files) surfaced **four corrections** that reshape the design. These are load-bearing — the implementer must internalize them.

| # | S0 assumption | Code reality (verified, develop HEAD) | Consequence |
|---|---|---|---|
| **R1** | `UseSkillTurnScope` validator denies non-base tools at runtime | **It is NEVER wired into the permission chain.** [biz.go:316-326](internal/numind/biz/biz.go:316) installs exactly 7 validators (PlatformHardRule, SandboxOverride, TenantAdminRule, WorkingDir, ToolFlag, UserSessionRule, AutoModeLLMValidator). `NewUseSkillTurnScope()` is referenced only by its own definition + unit tests. **It is dead code.** | "Remove the deny layer" = delete dead code. LOW behavioral risk. The skill `allowed_tools` union registered in runner is **already ungated** at runtime today. |
| **R2** | Cold-start pain: agents can't call `get_current_date` because base whitelist omits it | `get_current_date` **is** in `safeToolBaseline` ([student_run_lifecycle.go:718](internal/numind/biz/agent/student_run_lifecycle.go:718)) — every agent already has it. The truly registration-gated tools are `bash_exec` + `image_gen` (only added when `code_sandbox`/`media` category flags are on). | The "full-open" win is about `bash_exec`/`image_gen`/skill-`allowed_tools`, not `get_current_date`. AC example corrected. |
| **R3** | Tool availability is validator-gated | Availability is gated at **registration**: `toolNamesFromFlags()` builds `req.ToolNames` = `safeToolBaseline` + enabled categories; runner registers only those to Eino. `ToolFlag` validator only denies a tool whose **exact name** appears `false` in `tool_flags` — but the frontend stores **category** keys (`code_sandbox`/`media`/`enable_skills`), never raw tool names, so ToolFlag is effectively inert for real configs. | "Full-open" = **register all registry tools per agent** (`AgentToolRegistry.ListAllTools()`), not a validator change. ToolFlag can stay wired (inert now; doubles as the future per-tool gate hook). |
| **R4** | One runner path | **Two parallel implementations**: `runner.go::Run` AND `runner_runstream.go::RunStream`. RunStream is the **live** path ([student_run_stream.go:138](internal/numind/controller/v1/agent/student_run_stream.go:138)). Both duplicate skill-loading + tool-registration + catalog + PendingSkills. | **Every change lands in BOTH files**, identically. Single biggest implementation hazard (same class as the stream-emit-toolcall-events bug). |

**Net effect on scope**: the work splits cleanly into (A) dead-code deletion (low risk), (B) one real behavioral change — register-all-tools (medium risk, the actual "full-open"), (C) the `use_skill`/`read_skill` → `load_skill` merge (medium, two skill systems + two catalogs), (D) `allowed_tools` → recommendation. No DB migration. Bash gate stays deferred (and ToolFlag survives as its future hook).

---

## 1. Current architecture (the two skill systems)

There are **two independent skill systems**, each with its own tool + catalog renderer:

| | **DB business skills** | **Disk platform skills** |
|---|---|---|
| Source | `skill` table (parent-authored markdown), bound via `agent_skill_binding` | `<skills_root>/<name>/SKILL.md` (xlsx-author / docx-author / pptx-author / pdf-from-html) |
| Tool | `use_skill({name})` ([tool_use_skill.go](internal/numind/biz/agent/tool_use_skill.go)) | `read_skill({skill_name})` ([tool_read_skill.go](internal/numind/biz/agent/tool_read_skill.go)) |
| Body | business prompt guidance | code-gen recipe (run via `run_python`) |
| Catalog | `buildSkillCatalogBlock(skills)` ([runner.go:1534](internal/numind/biz/agent/runner.go:1534)) | `RenderSkillCatalog(reg)` ([skill_catalog.go:54](internal/numind/biz/agent/skill_catalog.go:54)) |
| Registered | runner-injected when agent has bindings (`AlwaysLoad=false`) | factory-registered when `skillRegistry != nil`; in `safeToolBaseline` + `categoryToTools["enable_skills"]` |
| `allowed_tools` | JSON field; union collected in runner (R1: but ungated) | n/a |
| Delivery | body wrapped `<system-reminder>` in tool result (single-loop ✓) | SKILL.md returned in tool result (single-loop ✓) |

Both are single-loop already (the 2026-05-29 refactor). Both inject their catalog into §2 of the system prompt; they coexist there today ([runner.go:717-732](internal/numind/biz/agent/runner.go:717)).

After removing the (dead) permission gate, `use_skill`'s only differentiator vanishes — both tools just inject a markdown guidance string into the same agent context. **They become the same operation on two storage backends.** Hence the merge.

---

## 2. Proposed solution (the 4 moves)

### Move A — Delete the dead permission layer
- Delete `permission/validators/use_skill_turnscope.go` + `use_skill_turnscope_test.go`.
- Delete `UseSkillTurnState.AllowedTools` + every read/write (use_skill merge logic, runner union blocks in **both** paths).
- Delete `WithAgentBaseToolNames` / `AgentBaseToolNamesFromCtx` / `CtxKeyAgentBaseToolNames` + the two ctx injections (consumed only by the dead validator). **Keep** `WithFullToolMap` (used by live permission `WrapHooks`).
- Delete the `eino_skill_integration_test.go` turn-scope-deny scenario (c); migrate scenarios (a)(b)(d) to a new `load_skill` test in package `agent`.

### Move B — Full-open tool registration (the one real behavioral change)
- In **both** `runner.go::Run` and `runner_runstream.go::RunStream`, replace the `for _, name := range req.ToolNames` registration with iteration over `r.registry.ListAllTools()` — every agent gets every registered tool.
- Delete the runner `extraTools` union blocks (skill `allowed_tools` pre-registration) in both paths — now subsumed by full registration.
- `req.ToolNames` / `toolNamesFromFlags` / `safeToolBaseline` / `categoryToTools`: left intact (still produced, now advisory — they no longer cap availability). `ToolFlag` validator: left wired (inert for category configs; preserved as the future per-tool gate hook the user deferred).
- Net: `bash_exec`, `image_gen`, and all former skill-`allowed_tools` tools become available to every agent. Skills no longer gate/unlock anything.

### Move C — Merge `use_skill` + `read_skill` → `load_skill`
- New tool `load_skill({name})` ([new file] `tool_load_skill.go`), constant `LoadSkillToolName = "load_skill"`. Resolution order: agent's bound DB skill (`turn.SkillByName[name]`) → else disk `registry.Get(name)`.
  - DB hit: validate `IsActive`/`BodyMd`, wrap body in `<system-reminder>`, append `allowed_tools` **recommendation** line (Move D), append `PendingSkill`, bump `InvocationCount` (cap-checked when turn state present).
  - Disk hit: read SKILL.md (existing read_skill logic), return body.
  - Miss in both: soft-error ack listing available skill names.
- Always registered when there is anything to load (disk registry non-nil **or** agent has DB bindings). Replaces both `use_skill` and `read_skill` in the factory + `safeToolBaseline` + `categoryToTools["enable_skills"]`.
- Unify the two catalog renderers into one `## 可用技能` §2 block listing both DB skills (this agent's bindings) and disk skills, instructing `load_skill({name})`. Update `OutputToolsPriorityAddendum` + `skillCatalogHeader`/`Footer` to teach `load_skill` (was `read_skill`).
- Turn state struct keeps its name `UseSkillTurnState` (internal, blast-radius bound) minus `AllowedTools`; cap + `PendingSkills` + `SkillBy*` retained. (Naming-legacy noted; optional rename deferred to keep the diff reviewable.)

### Move D — `allowed_tools` → recommendation (zero migration)
- Keep the DB `skill.allowed_tools` column + all existing values.
- On `load_skill` of a DB skill with non-empty `allowed_tools`, append a hint line to the wrapped body: `\n\n💡 推荐配合使用的工具：<a, b, c>`. The LLM sees it; since all tools are now registered, it can act on it. No enforcement.

---

## 3. Alternatives considered

| Option | Why not |
|---|---|
| Full-open via rewriting `toolNamesFromFlags` to return all tools | It's a static function with no registry handle; would need plumbing. Registering at the runner (where `r.registry` lives) is more surgical and centralizes the change. |
| Remove `ToolFlag` validator too (truly delete all gating) | Extra blast radius into the #6 permission-pipeline feature + its tests; ToolFlag is inert for current configs anyway and is the natural home for the **future** per-tool/bash gate the user explicitly deferred. Keeping it costs nothing and preserves that hook. |
| Keep two tools, just delete permission | Leaves two homogeneous tools + two catalogs + two param names (`name` vs `skill_name`) — the duplication the user explicitly called out. Merge is the point. |
| Rename `UseSkillTurnState` → `SkillTurnState` now | Pure churn across many references; the struct is internal. Deferred to keep the security-sensitive diff small and reviewable. |
| Fold in doc-gen防错 (ADR 0001) | User chose "keep separate" (S0-D1); orthogonal layer. |

---

## 4. Blast radius & the one real tradeoff (carried from S0, completed per review)

- **Files touched** (numind-server only): `runner.go`, `runner_runstream.go`, `tool_use_skill.go` (→ `tool_load_skill.go`), `tool_read_skill.go`, `skill_catalog.go`, `factory_platform.go`, `student_run_lifecycle.go`, `output_tools_priority_prompt.go`, `use_skill_turnscope.go` (delete), tests, `tool-display.yaml`. No DB, no API, no frontend code.
- **The tradeoff**: prompt-injection blast radius grows — every agent can now reach `bash_exec`, `web_fetch`, `memory_write`, etc. `run_python`/`bash_exec` stay sandbox-contained; network (`web_fetch`/`web_search`) and memory (`memory_write`) tools are **not** sandbox-contained and become reachable. compliance + budget gates unchanged. **B2B SaaS acceptable; user accepted (2026-05-31). S1 surfaces the completed version (incl. network/memory) for the record.**

---

## 5. Out of scope (unchanged from S0)
doc-gen防错; bash/run_python agent gate (ToolFlag preserved as its future hook); DB schema; API; admin/web-v3 UI copy; prod deploy.
