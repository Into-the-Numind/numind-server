# agent-mode-v2-skill-invocation — 实施计划

**关联**：[S0 需求卡](../../../requirements/agent-mode-v2-skill-invocation.md) · [S1 提案+PRD](../../../proposals/agent-mode-v2-skill-invocation-proposal.md) · [S2 spec](../specs/2026-05-24-agent-mode-v2-skill-invocation-design.md)

**日期**：2026-05-24 · **总 task 数**：9（含 T09 S5 验证策略 task，NDF Rule 10）· **估时**：S4 编码 7 工作日

**硬依赖**：v2 #1 `agent-mode-v2-skill-as-artifact` 必须先 land develop（提供 skill / agent_skill_binding 表 + biz/skill 包），S3→S4 阻塞检查协议见 cold-start prompt + Task #5。

---

## §1 Task 总览（依赖图）

```
T01 verify #1 interfaces ──┬──> T02 types + ctx helpers ──> T03 use_skill tool full
                           ├──> T04 narration skill_use event type
                           ├──> T05 UseSkillTurnScope validator
                           └──> T08 frontend ToolBubble (numind-web-v3, 跨 repo 并行)

T02 + T03 + T04 + T05 ──> T06 runner.go 改造 ──> T07 biz.go wire ──> T09 S5 验证策略
                                                                  (Eino integration + Playwright E2E + unit tests)
```

**Wave 调度（NDF Rule 12 Tier）**：

| Wave | Tier | Tasks | 备注 |
|---|---|---|---|
| **W1** | Tier 2 | T01 (interface verify + 缺函数补) | 串行 gate — 必须先验证 #1 land 才能开始 |
| **W2** | Tier 3 disjoint (numind-server) **+** Tier 2 cross-repo (numind-web-v3) | T02 (types) / T04 (narration event.go) / T05 (validator) / T08 (frontend ToolBubble) | 4 个并发：T02/T04/T05 同 numind-server 不同文件，T08 在 numind-web-v3 worktree。dispatch 前跑 `ndf-check-disjoint.sh` 验证 T02/T04/T05 文件归属无交集 |
| **W3** | Tier 2 | T03 (use_skill tool full Invoke) | 依赖 T02 types，串行 |
| **W4** | Tier 2 | T06 (runner.go 改造) | 依赖 T01/T02/T03/T05；改 runner.go 必须独占该文件 |
| **W5** | Tier 2 | T07 (biz.go wire) | 依赖 T06 |
| **W6** | Tier 2 | T09 (S5 验证策略 task) | 收尾，含 Eino integration / Playwright E2E specs / 多个 unit test |

**Tier 3 disjoint 文件归属声明（W2 dispatch 前必须输出）**：

```
T02 owns: internal/numind/biz/agent/tool_use_skill.go (新建, 仅 types + ctx helpers + tool 骨架, 无 Invoke)
T04 owns: internal/numind/biz/agent/narration/event.go (新增 skill_use kind) + provider.go (新增 emit helper, append-only)
T05 owns: internal/numind/biz/permission/validators/use_skill_turnscope.go (新建)
```

跑 `numind-server/scripts/ndf/ndf-check-disjoint.sh` 验证 3 文件归属无交集。失败立刻降级 Tier 4 串行（按 T02 → T04 → T05 顺序）。

> T08 (frontend) 在 numind-web-v3 worktree，是 Tier 2 跨 repo，无需 ndf-check-disjoint。

---

## §2 Tasks 详细规格

### T01 — Verify v2 #1 interfaces + 缺函数补丁

**描述**：S4 启动门——验证 #1 land develop 后提供的接口与 spec §2 假设一致；缺函数本 feature 在 worktree 补。

**涉及文件**：
- 检查（读 only，不动）：
  - `numind-server/internal/numind/biz/skill/service.go` (期望含 `GetByID(ctx, uint64) (*model.Skill, error)`, `GetByNameForUser(ctx, parentUserID uint, name string) (*model.Skill, error)`)
  - `numind-server/internal/numind/biz/skill/binding.go` (期望含 `ListByAgent(ctx, agentID uint64) ([]*model.AgentSkillBinding, error)`)
  - `numind-server/internal/pkg/model/skill.go` (含 Skill struct)
  - `numind-server/internal/pkg/model/agent_skill_binding.go` (含 AgentSkillBinding struct)
- 缺则补（在本 feature worktree）：
  - 补 `Service.GetByIDs(ctx, []uint64) ([]*model.Skill, error)` 到 service.go（batchGet IN 查询，按 spec §3.6 P1-2 修复）
  - 补 `Binding.ListByAgent` 到 binding.go（按 spec §2.1 提供的实现）

**起步 grep**：
```bash
cd /private/tmp/wt-agent-mode-v2-skill-invocation-numind-server
git fetch origin develop && git rebase origin/develop  # 拉最新 #1 改动
grep -n "GetByID\|GetByNameForUser\|GetByIDs\|ListByAgent" internal/numind/biz/skill/*.go
```

**验收条件**：
- 4 个 interface method 全部存在（已有或本 feature 补）
- `go build ./internal/numind/biz/skill/...` PASS
- `task lint` PASS
- 补的函数有对应 unit test（mock DB）

**估时**：0.5d（如 #1 已补 GetByIDs/ListByAgent，30 分钟完成验证 + lint）

**注意**：
- 如果 #1 已有 `ListBindingsByAgent` 而非 `ListByAgent`（命名微差，见 spec §6 reviewer 指出），本 feature 用别名 wrapper 不重命名（避免改 #1 文件触发 Tier 4 冲突）
- 接口签名不一致时：写 adapter 在 worktree 内（不动 #1 文件），S3 阶段就拒绝

---

### T02 — UseSkillTurnState struct + ctx helpers + tool skeleton

**描述**：定义 `tool_use_skill.go` 内的 types、ctx keys、ctx helper 函数；tool 仅有 skeleton（Name/Description/ParamSchema/IsDestructive），Invoke 留 stub。

**涉及文件**：
- `numind-server/internal/numind/biz/agent/tool_use_skill.go`（**新建**）
- `numind-server/internal/numind/biz/agent/tool_use_skill_test.go`（**新建** — 仅 types + helpers 的 unit test）

**关键内容**（按 spec §3.1 + §3.7）：
- `const UseSkillToolName = "use_skill"` + `UseSkillTurnCapDefault = 3`
- `type ctxKeyUseSkillTurnT struct{}` + `var CtxKeyUseSkillTurn = ctxKeyUseSkillTurnT{}`（同样 CtxKeyAgentBaseToolNames / CtxKeySkillBindings）
- `type UseSkillTurnState struct { InvocationCount int; AllowedTools map[string]struct{}; Cap int; PendingBody string; PendingSkillName string; PendingSkillVersion int; SkillByID map[uint64]*model.Skill }`
- `WithUseSkillTurn(ctx, *UseSkillTurnState) context.Context` + `UseSkillTurnFromCtx(ctx) (*UseSkillTurnState, bool)`
- 同样 helper for AgentBaseToolNames / SkillBindings
- `useSkillTool` struct + 5 method（Name/Description/ParamSchema/IsDestructive return；Invoke return `jsonErr("not implemented yet — T03")`，纯 stub）

**验收条件**：
- `go test ./internal/numind/biz/agent/ -run TestUseSkillTurn` PASS
- `go vet ./internal/numind/biz/agent/` PASS
- ctx helper 单元测试：assert SetValue → GetValue 等价；nil ctx 边界

**估时**：0.5d

---

### T03 — use_skill tool full Invoke 实现

**描述**：实现 spec §3.1 的 Invoke 完整逻辑（11 个步骤），含 Langfuse span（按 spec §7）+ narration emit。

**涉及文件**：
- `numind-server/internal/numind/biz/agent/tool_use_skill.go`（修改 T02 stub → 完整 Invoke）
- `numind-server/internal/numind/biz/agent/tool_use_skill_test.go`（追加 Invoke 单元测试）

**Invoke 11 步骤**（spec §3.1）：
1. JSON unmarshal args
2. 取 turn state from ctx
3. cap check
4. lookup Skill via `skillService.GetByNameForUser(parentUserID, name)`
5. business validation: !IsActive / BodyMD == ""
6. **bound check via `turn.SkillByID[sk.ID]`**（spec §3.1 P0-1 已拍板用 runner cache）
7. merge allowed_tools 到 turn.AllowedTools
8. narration emit
9. write turn.PendingBody / Name / Version（runner T06 在下次 LLM 调用前消费）
10. budget 由 hook 自动记账（无需 Invoke 内操作）
11. InvocationCount++ + 返回 acknowledgement JSON

**单元测试**（spec §10 项 3 + AC-5 + AC-6）：
- happy path（mock skillService 返回 fixed Skill → assert turn.AllowedTools + PendingBody 写入 + count++ + 返回 JSON 含 status=loaded）
- name 不存在 → 返回 jsonErr "技能 'X' 不存在或无权访问"
- !IsActive → 返回 jsonErr "技能 'X' 已被禁用"
- BodyMD == "" → narration error + 返回 jsonErr
- 跨 Agent 未装载（turn.SkillByID 不含 sk.ID） → 返回 jsonErr "技能未装载"
- cap exceeded (InvocationCount = 3) → 返回 jsonErr "已达上限"
- turn state nil（v1 legacy path 走错）→ 返回 jsonErr "未启用"

**Langfuse span**（spec §7）：
- `langfuse.CreateSpan` w/ name "tool_use_skill" + metadata{skill_id, skill_name, skill_version, body_token_count, allowed_tools_added, turn_invocation_count_after, result_status}
- nil-safe（tc != nil 检查）

**验收条件**：
- `go test ./internal/numind/biz/agent/ -run TestUseSkillTool` PASS（至少 7 testcases）
- Coverage on tool_use_skill.go ≥ 85%
- `task lint` PASS

**估时**：1d

---

### T04 — narration provider skill_use event 类型

**描述**：narration provider 新增 `kind="skill_use"` SSE event 类型 + emit helper。

**涉及文件**：
- `numind-server/internal/numind/biz/agent/narration/event.go`（修改 — 加 kind enum 值 `SkillUse`）
- `numind-server/internal/numind/biz/agent/narration/provider.go`（修改 — 加 `EmitSkillUse(ctx, skillName, phase)` helper）
- `numind-server/internal/numind/biz/agent/narration/event_test.go`（追加 SkillUse event unit test）

**event 格式**（spec §2.3）：
```json
{ "kind": "skill_use", "skill_name": "销售话术训练", "phase": "loading|loaded|error", "error_message": "" }
```

**helper 签名**：
```go
// EmitSkillUse(ctx, name string, phase string, errMsg string)
// phase: "loading" | "loaded" | "error"
// errMsg: only used if phase == "error"
func (p *Provider) EmitSkillUse(ctx context.Context, name, phase, errMsg string) error
```

T03 中的 `emitNarrationLoaded(ctx, name)` / `emitNarrationError(ctx, name, msg)` 是 useSkillTool 内 wrapper，调 `Provider.EmitSkillUse`。

**验收条件**：
- `go test ./internal/numind/biz/agent/narration/ -run TestSkillUse` PASS
- 3 phase × Provider==nil 防御 = 4 testcases

**估时**：0.5d

---

### T05 — UseSkillTurnScope validator

**描述**：实现 spec §3.4 的 permission validator，挂入 biz/permission/validators/ 与现有 7 个并列。

**涉及文件**：
- `numind-server/internal/numind/biz/permission/validators/use_skill_turnscope.go`（**新建**）
- `numind-server/internal/numind/biz/permission/validators/use_skill_turnscope_test.go`（**新建**）

**实现规格**（spec §3.4）：
- struct `UseSkillTurnScope` + `NewUseSkillTurnScope() permission.Validator` 构造
- 4 分支决策：
  - 工具 == "use_skill" → `Passthrough(v.ID(), DecisionReasonOther, "use_skill self")`
  - 工具在 `AgentBaseToolNamesFromCtx(ctx)` → `Passthrough(v.ID(), DecisionReasonOther, "in base whitelist")`
  - 工具在 `turn.AllowedTools` → `Passthrough(...)`
  - 否则 → `Deny(v.ID()+":"+toolName, DecisionReasonRule, "...")`
- legacy path（turn state nil）→ `Passthrough`（让后续 validator 判，不应当 deny）

**单元测试**（spec §10 项 4 + AC-3 + AC-10）：
- use_skill 永远 Passthrough
- base 白名单 Passthrough
- turn-scope allowed → Passthrough
- 不在任何 set → Deny
- legacy（无 turn state）→ Passthrough
- 边界：req.Tool == nil → Passthrough

**验收条件**：
- `go test ./internal/numind/biz/permission/validators/ -run TestUseSkillTurnScope` PASS（至少 6 testcases）
- `task lint` PASS
- 不依赖 mock — validator 是纯函数，ctx + req 输入即可

**估时**：0.5d

---

### T06 — runner.go 改造（核心 task，最高 LOC）

**描述**：runner.go §3.2 binding 装载块 + §3.6 工具装配 + §3.3 PendingBody 注入到 next LLM call messages + §3.5 dual-read 兜底。

**涉及文件**：
- `numind-server/internal/numind/biz/agent/runner.go`（修改）
- `numind-server/internal/numind/biz/agent/runner_test.go`（追加 4 个 testcase）

**runner.go 改动点**（按 spec §3.2 + §3.5 + §3.6 综合）：

1. **字段新增**（line ~92 后）：
   ```go
   skillService      skill.IService       // v2 #2 wired by biz.go
   skillBindingStore skill.IBindingStore  // v2 #2 wired by biz.go
   ```

2. **构造选项**（line ~125 后）：
   ```go
   func WithSkillService(s skill.IService) RunnerOption { return func(r *agentRunner){ r.skillService = s }}
   func WithSkillBindingStore(s skill.IBindingStore) RunnerOption { return func(r *agentRunner){ r.skillBindingStore = s }}
   ```

3. **§3.2 binding 装载块**（line 391-419 改造，按 spec §3.2 完整代码）：
   - 调 `ListByAgent` 查 binding
   - 调 `GetByIDs` batchGet 缓存到 skillByID map
   - 同名 defensive check (S1-D13)
   - body 双轨：v2 = catalog only / legacy = ad.GeneratedSkillBody
   - 初始化 useSkillTurnState 含 SkillByID + 6 字段，注入 ctx (WithUseSkillTurn)

4. **§3.6 工具装配改造**（line 617-641）：
   - 装载基础工具（不变）
   - 装载 use_skill + binding 的 allowed_tools 并集（使用 skillByID 复用避免 N+1）
   - 注入 ctx WithAgentBaseToolNames(basicToolNames)

5. **§3.3 PendingBody 注入**（在 Eino agent.Generate 调用前，line ~770-790 区域）：
   - 检查 `turn.PendingBody != ""`
   - 若有，append 一条 `schema.Message{Role: schema.User, Content: "<system-reminder>..."}` 到 einoMessages
   - consume：turn.PendingBody = "" / PendingSkillName = "" / PendingSkillVersion = 0

6. **辅助函数新增到 runner.go 同包**：
   - `batchGetSkills(ctx, skillService, bindings) map[uint64]*model.Skill`
   - `checkDuplicateSkillNames(bindings, skillByID) error`
   - `buildSkillCatalogBlockFromMap(bindings, skillByID) string`

**单元测试新增**（spec §10 项 2 + AC-1 + AC-4）：
- `TestRunner_SystemPrompt_6SegmentInvariant`：assert `PlatformBasePrompt` 在最前 / `PlatformSafetyFooter` 在最后 / `## Memories` 在 body 和 toolsSection 之间 / segment count == 6（用 Index assertions）
- `TestRunner_DualReadFallback_NoBindings_UsesLegacyBody`：mock ListByAgent 返回 0 binding → assert SystemPrompt 含 ad.GeneratedSkillBody
- `TestRunner_DualReadFallback_WithBindings_UsesCatalog`：mock ListByAgent 返回 2 binding → assert SystemPrompt 含 "## 可用技能" 且不含 ad.GeneratedSkillBody
- `TestRunner_DuplicateSkillNames_RejectsRun`：mock 2 binding 同名 → assert err 非 nil + RunResult.TerminalReason 未变化

**验收条件**：
- `go test ./internal/numind/biz/agent/ -run TestRunner` PASS（包括新增 + 现有所有 runner test）
- 现有 `e2e/agent-student.spec.ts` mock 单测部分（如有）PASS
- `task lint` PASS
- **grep 验证 ctx 注入完整**（S3 reviewer P2-1）：
  - `grep -n "WithUseSkillTurn\|CtxKeyUseSkillTurn" internal/numind/biz/agent/runner.go` 至少 1 命中
  - `grep -n "WithAgentBaseToolNames\|CtxKeyAgentBaseToolNames" internal/numind/biz/agent/runner.go` 至少 1 命中
  - `grep -n "CtxKeySkillBindings" internal/numind/biz/agent/runner.go` 至少 1 命中（spec §3.7 预留 key，T06 容易漏写）

**估时**：2d（核心 task，含调试 Eino message append 行为）

**注意**：
- runner.go 长达 1184 行，本 task 主控必须独占该文件（Tier 4 串行）
- 改动尽量集中：所有 v2 #2 改动用 `// v2 #2:` 前缀注释，方便 reviewer / 后续 v2 #4 清理 deprecated 字段时定位
- 不破坏现有 5 个 invariants（CLAUDE.md §6b）— 编码时反复对照

---

### T07 — biz.go wire (Service + Validator + Tool 注册)

**描述**：biz.go 装配点：
- wire `skillService` + `skillBindingStore` 到 agentRunner（通过 WithSkillService / WithSkillBindingStore option）
- 注册 `UseSkillTurnScope` validator 到 PermissionPipeline
- 注册 `useSkillTool` 到 AgentToolRegistry

**涉及文件**：
- `numind-server/internal/numind/biz/biz.go`（修改 — 装配新依赖）
- `numind-server/internal/numind/biz/biz_test.go`（如有，加 wire test）

**改动量**：~20-30 行（构造 + wire）

**验收条件**：
- `go build ./internal/numind/biz/...` PASS
- 启动 server (`task dev` 短跑 + Ctrl+C) → log 显示 "use_skill tool registered" + "UseSkillTurnScope validator wired"
- `task lint` PASS

**估时**：0.5d

---

### T08 — Frontend ToolBubble skill_use case（numind-web-v3）

**描述**：在前端 chat 工具气泡组件加 `skill_use` event 类型 case。Tier 2 跨 repo，可与 W2 后端 task 并行 dispatch。

**涉及文件**（S2 spec §8 候选，T08 起步 git grep 确认实际路径）：
- `numind-web-v3/src/components/chat/ToolBubble.vue` **或** `numind-web-v3/src/components/agent/AgentToolBubble.vue`（grep 确认）
- `numind-web-v3/src/types/narration.ts` 或 `src/api/agent.ts`（event type enum）
- 可能新增：`numind-web-v3/src/components/chat/SkillUseBubble.vue`（如复杂度需要独立组件）

**起步 grep**：
```bash
cd /private/tmp/wt-agent-mode-v2-skill-invocation-numind-web-v3
grep -rn "ToolBubble\|tool-bubble\|kind ===" src/components/ | head -10
grep -rn "narration\|EventSource\|SSE.*tool" src/api/ src/stores/ | head -10
```

**Vue case 模板**（spec §8）：
```vue
<div v-else-if="event.kind === 'skill_use'" class="tool-bubble skill-use">
  <span class="icon">📚</span>
  <span v-if="event.phase === 'loading'">正在加载技能：{{ event.skill_name }}…</span>
  <span v-else-if="event.phase === 'loaded'">已调用技能：{{ event.skill_name }}</span>
  <span v-else-if="event.phase === 'error'" class="error">⚠ 技能加载失败：{{ event.error_message }}</span>
</div>
```

**默认 fallback**（spec §S1 风险 8）：在 ToolBubble.vue 加 default case + `console.warn('Unknown narration kind:', event.kind)` 防静默无显示。

**单元测试**：vitest 渲染测试 3 phase × loaded/loading/error。

**验收条件**：
- `npm run lint && npm run type-check` PASS（numind-web-v3）
- vitest 3 testcase PASS
- 浏览器开发模式手工触发 SSE event (mock) 看到气泡渲染
- **grep 探查协议**（S3 reviewer P1-2）：起步跑两条 grep（见上）；如果 ToolBubble 候选路径 + narration event type 候选路径都 grep 无命中 → **触发 Pause and Ask**（详见 ndf-workflow.md §6），不自行猜测路径。在主控报告："T08 grep 探查无命中，前端 chat 流文件结构可能已重构。请人工确认 ToolBubble 实际位置或确认本 feature 是否需要前端改动。" 期间该 task 暂停，不阻塞其他后端 task

**估时**：1d（含 grep 探查 + 适配现有 ToolBubble 组件风格）

---

### T09 — S5 验证策略 task（NDF Rule 10 硬要求）

**描述**：S5 验收前必须就位的全部验证基础设施 + 验证方式拍板（在 plan 中即固化）。

**S5 验证策略拍板**（NDF Rule 10 + spec §10）：

- **主要方式**：**Playwright E2E 优先**（涉及 LLM 行为 + narration 渲染 + dual-read 切换三角度只有 E2E 端到端覆盖）
- **补充方式**：Go integration test (Eino mock LLM) + Go unit test
- **关键 user path**（S5 必须覆盖）：
  1. 父账户给 Agent 装载 2 Skill → 学员视角对话 → LLM 自主 emit use_skill → 气泡显示 → 回复贴合 Skill 指引（AC-1/2/7/8）
  2. v1 Agent 0 binding → legacy 路径 → `e2e/agent-student.spec.ts` 全 pass（AC-4）
  3. use_skill 错误（name 不存在 / Skill 禁用 / 超 cap） → LLM 优雅恢复（AC-5/6）
- **Langfuse 验证**：S5 必须截图 1 个完整 use_skill 调用 trace（agent_run → use_skill span → 下次 generation 含 body）（AC-9）

**涉及文件**（spec §10 8 测试项映射）：

| 测试项 | 文件 | 类型 |
|---|---|---|
| 1. v2 happy path E2E | `numind-web-v3/e2e/agent-skill-invocation.spec.ts`（**新建**）| Playwright E2E |
| 2. v1 legacy 零回归 | `numind-web-v3/e2e/agent-student.spec.ts`（**复用现有**） | Playwright E2E |
| 3. use_skill 错误路径 | T03 已写 `tool_use_skill_test.go` | Go unit |
| 4. turn-scope tool gate | T05 已写 `use_skill_turnscope_test.go` | Go unit |
| 5. system prompt 6 段 invariant | T06 已写 `TestRunner_SystemPrompt_6SegmentInvariant` | Go unit |
| 6. Eino 中文工具参数 + system-reminder | `numind-server/internal/numind/biz/agent/eino_skill_integration_test.go`（**新建**）| Go integration (Eino) |
| 7. Langfuse trace 完整性 | 手工 trigger + Langfuse 截图（不写代码，写测试脚本步骤到 qa-report） | 手工 |
| 8. AC-11 调用率 ≥30% | 手工 10 场景 + 写脚本 `numind-web-v3/e2e/skill-invocation-rate.spec.ts`（半自动）| Playwright E2E + 手工统计 |
| 9. AC-6 BudgetTracker 集成 | `numind-server/internal/numind/biz/budgetgate/budget_skill_test.go` 或追加现有（**新建/追加**）| Go unit |

**T09 主要交付**：项 1 / 6 / 8 / 9 新建文件 + 项 7 测试步骤脚本。

**Eino integration test 设计**（spec §6）：
- 复用 `runner_integration_test.go` mock pattern（mockChatModel + mockToolRegistry）
- 3 case：
  - 中文工具参数解析：fake LLM emit `use_skill({"name":"销售话术训练"})` → assert Invoke 成功
  - system-reminder 包装 message 不破坏 ReAct：use_skill 后 fake LLM 收到含 `<system-reminder>` 的 user msg → assert 仍返回 assistant 而非 terminate
  - turn-scope deny：use_skill 未调用前 fake LLM 调用 binding tool → assert HookActionPermissionDeny + RunResult.PermissionDenial 含 "skill_not_invoked" 关键字

**Playwright E2E 设计**（spec §10 项 1）：
- 前置：mock 后端响应 / 用 dev fixture 创建 2 Skill + binding
- 步骤：login → 进入 agent 对话页 → 发送触发文（如 spec §1 演示场景 1 措辞）→ assert SSE 流出现 `skill_use` event → assert 最终 assistant 回复含 Skill when_to_use 关键词（AC-8 指引词典）

**验收条件**（T09 自身）：
- 项 1 / 6 / 8 / 9 文件创建且 PASS
- qa-report 模板 (`templates/ndf/qa-report.md`) 项 7 手工步骤 + 项 8 手工 10 场景统计表
- **baseline 验证**（S3 reviewer P1-1）：T09 起步必跑 `cd numind-web-v3 && E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- e2e/agent-student.spec.ts` 全 pass 截图存档，确认 #1 改动未破坏 v1 fixture（防 S5 时基线已坏才发现）

**估时**：1d（含 Eino mock 编写 + Playwright spec + qa-report 步骤草稿）+ 0.5d buffer for AC-11 手工 10 场景统计（S3 reviewer P2-2）

**S5 执行窗口建议**：T09 完成后 S5 实际执行（启 local dev + Langfuse + 跑全套测试）预留 1d；含 AC-11 调用率统计的手工 10 场景对话耗时

---

## §3 估时汇总

| Task | 估时 |
|---|---|
| T01 | 0.5d |
| T02 | 0.5d |
| T03 | 1d |
| T04 | 0.5d |
| T05 | 0.5d |
| T06 | 2d |
| T07 | 0.5d |
| T08 | 1d |
| T09 | 1d + 0.5d buffer (AC-11) |
| **合计** | **8d**（S5 执行另预留 1d） |

S1 估时 7 工作日，T09 包含 0.5d buffer 后总 8d，S5 执行 1d 单算（共 ~9d）。

**S4 编码 + S5 执行总窗口**：D6-D14 = 9 工作日（与 S1 §2 时间线 D6-D13 + D14 S6 匹配）。

---

## §4 测试基础设施清单（W1 起步即可建）

**Go 测试 mock（复用 + 新增）**：

| Mock | 复用源 | 新增内容 |
|---|---|---|
| mock `skill.IService` | 无（#1 新接口） | 写在 `tool_use_skill_test.go` |
| mock `skill.IBindingStore` | 无 | 写在 `tool_use_skill_test.go` |
| mock `narration.Provider` | `narration/provider_test.go` | 复用 |
| fake `model.ToolCallingChatModel` (Eino) | `runner_integration_test.go` | 复用 |
| mock `BudgetTracker` | `budgetgate/gate_test.go` | 复用 |

**Playwright fixture**：

| Fixture | 用途 |
|---|---|
| `e2e/auth.setup.ts` | 复用现有 login |
| `e2e/fixtures/skill-bindings.ts` | **新建** — seed 2 Skill + 装载到 fixture Agent |
| `e2e/helpers/diagnose.ts` | 复用现有 |

---

## §5 Risks (S3 plan-level)

| # | 风险 | 缓解 |
|---|---|---|
| 1 | T01 发现 #1 没补 GetByIDs/ListByAgent，本 feature 补又触发 Tier 4 冲突 | T01 起步先 git fetch 确认；缺函数补在我们 worktree 的 service.go / binding.go 末尾追加（新增方法不修改现有），不破 #1 改动 |
| 2 | T06 runner.go 改造 LOC 大 (~150 行)，reviewer 担心原子性 | Task 划分按"逻辑独立单元"原则不按 LOC 限制——T06 是"runner.go 改造原子单元"，拆开会导致 runner.go 半成品不可编译。S3 plan reviewer 验证此判断 |
| 3 | T09 Eino integration test 不复用现有 mockChatModel 导致漂移 | T09 起步必须 grep `runner_integration_test.go` 提取 mockChatModel 模式直接复用 |
| 4 | W2 ndf-check-disjoint 失败（T02/T04/T05 文件归属冲突）| 降级 Tier 4 串行（T02 → T04 → T05），1 天内可补；不影响 T03 起步 |

---

## §6 Open items for S4 execution

无 — S3 plan 后所有设计 / task 拆分 / 验证策略已就位，S4 直接进编码。

T01 在 S4 起步时再做"git fetch verify"，若 #1 接口与 spec 假设不一致超出 adapter 能力 → Pause and Ask 协议（中止 S4，与 #1 协调或与父账户决策）。
