# Agent Mode v2 · Skill Invocation — 提案 + PRD

**Feature ID**：`agent-mode-v2-skill-invocation`
**起草日期**：2026-05-24
**状态**：S1（提案 + PRD）
**S0 引用**：[numind-server/requirements/agent-mode-v2-skill-invocation.md](../requirements/agent-mode-v2-skill-invocation.md)
**autopilot**：S1 不停顿等父账户硬门禁；reviewer PASS 即进 S2

---

## §1 方案概述 [客户可见]

**一句话**：让 Agent 在对话中能"按需调用"父账户装载的 Skill——LLM 自主判断何时该用哪个 Skill，runtime 把 Skill 内容载入对话上下文，并临时启用该 Skill 需要的额外工具。

**对父账户的可见变化**：

- 在 Agent 配置页装载多个 Skill 后，**学员对话时 LLM 真的会调用这些 Skill**（v2 #1 只让装载关系入了库，#2 让 runtime 真的读这些关系）
- 学员视角：chat 流里出现"📚 调用技能：销售话术训练"气泡，LLM 后续回复明显贴合该 Skill 的指引
- v1 时代配置的 Agent（无 binding）行为完全不变——dual-read 兜底保证零回归

**对父账户的不可见但重要的工程价值**：

- 不再需要"一个 Agent 一个 Skill"的 1:1 心智模型——一个 Agent 可装载多个 Skill，按对话场景动态切换
- 为 v2 #3 marketplace 解锁运行时承载方式（订阅来的 Skill 装载即可用）
- 为 Skill 演进解锁路径（更新一个 Skill 不需要重做问卷拼装整个 Agent）

**演示场景（S5 验收 + 父账户硬验收用同一脚本）**：

> 父账户配置 "全能销售助手" Agent，装载 2 个 Skill：
> - "销售话术训练"（when_to_use: 学员发来销售对话希望分析或改写）
> - "客户画像分析"（when_to_use: 学员需要从碎片信息生成客户画像）
>
> 学员对话场景 1："我刚跟一个新客户聊了半小时，他说他们公司在考虑 ERP 系统迁移，预算 50w。能帮我整理一下这个客户的画像吗？"（明确触发 when_to_use 关键词"客户画像"）
> → LLM 自主 emit `use_skill("客户画像分析")` → 气泡显示 → 回复给出行为/动机/痛点结构化画像
>
> 学员对话场景 2："这段我跟客户的对话怎么改更好？[贴对话]"
> → LLM 自主 emit `use_skill("销售话术训练")` → 气泡显示 → 回复给出 3 个改写方向

## §2 报价与周期 [客户可见]

- **预估工作量**：10 工作日（2 周日历周）
- **报价**：无（v2 三件套内部规划，不单独报价）
- **交付时间线**：
  - S0-S3 plan 完成 + reviewer PASS：D1-D2
  - 等 #1 land develop（被动）：D3-D5（与 #1 进度同步，可与 S3 plan 重叠）
  - S4 编码（7 task × ~1 天）：D6-D12
  - S5 本地验收 + Langfuse 验证：D13
  - S6 ndf-done + /deploy-dev + 父账户演示：D14
- **prod 部署**：不在本 feature 范围（按 agent-mode autopilot，待父账户单独确认）
- **#1 阻塞 fallback**：本 feature 通过 ScheduleWakeup 1800s 轮询检测 #1 land 状态（最多 7 天 = 336 次唤醒，cold-start prompt 已约定）；若 #1 D5 仍未 land：
  - D6-D7：继续阻塞 + 同步同期 NDF retro 写"#1 延期影响 #2"
  - D8+：spawn 父账户介入 chip 提示需要决策（继续等 / 用 adapter pattern mock #1 接口启动 S4 / cancel #2）
  - 本 feature 不主动 mock #1 启动 S4——避免 #1 接口变化导致 rework

---

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 复用方式 | 文件 |
|---|---|---|
| `AgentTool` interface | 实现 `use_skill` tool，与 8 个内置工具同构 | `internal/numind/biz/agent/types/tool.go`（或对应位置） |
| `AgentToolRegistry` | 注册 use_skill 工具到 registry，runner 启动时通过 `req.ToolNames` 选用 | `internal/numind/biz/agent/registry.go` |
| `biz/skill/service.go`（#1 land 后） | 查 Skill by id / by parent_user_id+name | 等 #1 |
| `biz/skill/binding.go`（#1 land 后） | 查 Agent 的所有 binding | 等 #1（缺函数本 feature 补） |
| `narration.Provider` | 新增 `skill_use` event 类型 | `internal/numind/biz/agent/narration/` |
| `BudgetTracker` | use_skill PreToolCall/PostToolCall 计入，与其他工具同等 | `internal/numind/biz/agent/budgetgate/` |
| `compliance.WrapHooks` chain | **不新增 chain slot**——5 slot 固定（compliance → permission → budget → sandbox → narration）；turn-scope tool gate 落地形式 S2 选定：(a) permission pipeline 内新 validator（与现有 7 validator 同构）/ (b) 独立 EinoToolWrapper（不进 hook chain，包在 adaptFullToEinoTool 外） | `internal/numind/biz/agent/permission/`（方案 a）或 `internal/numind/biz/agent/`（方案 b） |
| `runner.go` SystemPrompt 6 段拼接（line 578-589） | 在 `body` 后、`memoriesSectionHeader` 前插段位 2.5 | `internal/numind/biz/agent/runner.go` |
| `agent_definition` GORM model | 读 `generated_skill_body` / `custom_skill_body` 做 dual-read 兜底 | `internal/pkg/model/agent_definition.go` |

### 技术风险

| # | 风险 | 缓解（继承 S0 §5 + S1 新增） |
|---|---|---|
| 1 | #1 函数签名/字段名未定 | S3 plan 起草前 git fetch + 读 #1 spec 对齐；S4 编码前再次验证；不一致用 adapter 包一层 |
| 2 | Eino ReAct 不支持运行时动态扩展 tool list | **方案 D4**：runner 启动时预注册"所有 binding allowed_tools 并集"+ turn-scope hook gate 默认拒绝非基础白名单。S2 阶段写最小 Eino integration test 验证 hook 拦截 + use_skill 后放开 ok |
| 3 | dual-read 路径写错破坏 v1 Agent | S5 必跑现有 e2e/agent-student.spec.ts 全套；S4 加 Go unit test 覆盖 binding_count=0 分支 |
| 4 | LLM 不主动调 use_skill | system prompt 段位 2.5 用明确中文 + 示例触发条件；S5 验收手工跑 2 个场景；Langfuse trace 看 tool-call 率 |
| 5 | use_skill 无限递归 | hard cap 3 次/turn；超 cap 返回 result 而非异常；turn-scope 计数器在 user message 到来时 reset |
| 6 | Skill body 注入 Eino messages 顺序混乱 | S2 阶段写 3 个角色（assistant / tool result / system reminder）的 prototype，跑 Eino 集成测试看哪个最不破坏 ReAct 状态；选最稳的 |
| 7 | tool gate 与现有 compliance/permission chain 冲突 | **hook chain 5 slot 固定不动**（CLAUDE.md §6b）。turn-scope tool gate 落地两条路：(a) permission pipeline 内新 validator（推荐，与 7 validator 同构）/ (b) 独立 EinoToolWrapper 包在 adaptFullToEinoTool 外。S2 写 prototype + Eino 集成测试比较 |
| **invariant 兼容性** | 本 feature **不违反** §6b 5 个 invariants（HookAction enum / TerminalReason enum / LoopEvent enum / **system prompt 6 段顺序** / aiservice 唯一入口）；hook chain 5 slot 顺序也不动 | **重要修正**：runner.go 当前 6 段为 [1] PlatformBase / [2] tenantHardRules / [3] body / [4] Memories / [5] toolsSection / [6] PlatformSafetyFooter。Skill 目录**不新增第 7 段**，而是**扩展段位 [3] body** = original_body + "\n\n" + skill_catalog_block。这样 6 段结构不变。S4 实现要在 runner.go body 赋值后立刻 append catalog；S5 单元测试要 assert PlatformBase 在最前、PlatformSafetyFooter 在最后、Memories 在 body 与 toolsSection 之间 |
| 8 | narration `skill_use` event 前端没处理静默无显示 | S5 浏览器 QA 必查；ToolBubble.vue 加 default fallback case + console.warn |
| 9 | #1 数据迁移失败导致 Agent 既无 binding 又无 generated_skill_body | dual-read fallback 到空 body（不抛错）+ warn log + S5 验证此分支 |
| 10 | 配置者改 Skill allowed_tools 时 Agent run 用旧白名单 | 每次 use_skill 重新 lookup（不缓存）；下次 turn 即生效；接受当前 turn 用旧白名单（无业务影响） |
| **新11** | **agent_skill_binding 查询每次 LLM turn 都跑 → DB 压力** | runner 启动时一次查询缓存到 RunRequest scope；同一 Run 内不重查（多轮对话共用）。S5 性能测试覆盖。 |
| **新12** | **Skill body 大小累加 → 触发 compactv2 超 turn budget** | S2 阶段计算：3 次 use_skill × 50KB body = 150KB ≈ 35K token，刚好接近 V2 70% prune 阈值（看 ctx_window 是 200K）。S2 设计预留与 compactv2 协议接口（use_skill body 注入时给 compactv2 hint） |

### 涉及仓库

- [x] **numind-server**：runner.go / tool_use_skill.go / narration / budgetgate / hook chain / migration（无需，#1 已建表）
- [x] **numind-web-v3**：ToolBubble 渲染 `skill_use` event（仅 1 个文件改）
- [ ] **numind-admin-web**：不动

### AI 可观测性

- [x] **涉及 LLM 调用**：是
- **Trace 起点**：现有 `biz/agent/runner.go::Run()` 已经创建 agent_run trace（v1 已实现）。本 feature 不新建 trace 根，**追加 span**
- **Generation 点**：
  - 本 feature 不新增 generation（use_skill 不调 LLM）
  - use_skill 调用本身记为 **span**（不是 generation）：
    - span name: `use_skill`
    - metadata: `{ skill_id, skill_name, skill_version, body_token_count, allowed_tools_count }`
- **trace topology 验证**：S5 Langfuse 截图必须显示 agent_run trace → use_skill span（携带 skill metadata）→ 之后的 LLM generation（input 含 skill body）
- **关键元数据**：`agent_run_id, agent_definition_id, parent_user_id, skill_id, skill_name`
- 参照 `.claude/rules/ai-service.md` §3 Span 与 Error 模式

---

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

#### 配置者（父账户）

1. **作为 父账户**，我需要 把多个 Skill 装载到一个 Agent 后，**学员对话时 LLM 真的会按场景调用这些 Skill**，**以便** 实现"一个 Agent 多个能力"的角色（而不是一个 Agent 一个 Skill 的 v1 限制）
2. **作为 父账户**，我需要 v1 配置过的 Agent 在 v2 上线后**行为完全不变**（无 binding 时走 legacy 路径），**以便** v2 升级不破坏老 Agent
3. **作为 父账户**，我需要 学员视角看到清晰的"AI 现在用了哪个技能"提示，**以便** 我能验证装载的 Skill 真的生效

#### 学员（子账户）

4. **作为 学员**，我需要 在对话中看到 Agent 切换技能的过程（"📚 调用技能：销售话术训练"气泡），**以便** 我理解 AI 接下来回复的依据
5. **作为 学员**，我需要 一次对话能让 AI 用多个技能（比如先画像分析再话术训练），**以便** 复杂场景不被"一个 Agent 一个能力"限制

### 验收标准

复用 S0 的 AC-1..AC-10（共 10 条），不再重复。**S1 新增 1 条 PRD 级补强**：

| # | 标准 | 验证方式 |
|---|---|---|
| AC-11 (新) | use_skill 调用频率（Langfuse trace 聚合，按对话场景）应 ≥ 30%（即装载 Skill 的 Agent 在符合 when_to_use 的对话里 LLM 至少 30% 概率调用 Skill）；S5 阶段跑 10 个手工场景统计 | Langfuse 聚合查询 + 手工统计 |

### 边界情况

| 场景 | 期望行为 |
|---|---|
| Agent 装载 0 Skill | 走 legacy 路径（不注入 Skill 目录，不注册 use_skill 工具，与 v1 完全一致） |
| Agent 装载 1 Skill | 注入 1 行目录 + 注册 use_skill；LLM 看到只有 1 Skill 可选 |
| Agent 装载 >10 Skill | system prompt 段位 2.5 列全部（10 × ~100 token ≤ 1000 token，可接受）；S2 评估是否需要分页/截断（暂定 ≤20 全列） |
| Skill name 含空格 / 特殊字符 / 中文 | 必须支持中文（"销售话术训练"），LLM 把它作为 use_skill 参数；S2 写 Eino 集成测试验证中文工具参数 |
| Skill body 50KB（#1 上限） | 注入到 turn 上下文不阻塞；触发 compactv2 prune 时按 V2 协议处理 |
| use_skill 同一 turn 多次 | 允许，但 ≤3 次（hard cap）；超 cap 返回 "已达本轮技能调用上限" |
| LLM emit use_skill 但 name 不存在 | 返回 error result "技能 'X' 不存在"，LLM 看到后能自我恢复（不让 run failed） |
| LLM emit use_skill 但 Skill 跨租户 / 已解绑 | 返回 error result "技能 'X' 未装载或无权访问"，不暴露存在性 |
| Skill body 在 use_skill 后但还没 LLM 调用前，DB 中被父账户改了 | 当前 turn 用旧 body（已 lookup 完）；下次 use_skill 新调用拿新 body |
| Skill 自带的 allowed_tools 含 Agent 基础白名单已有的工具 | 取并集，去重；turn-scope 允许集合是 union 不是 replace |
| Agent 同时有 binding AND generated_skill_body 非空 | **binding 优先**，body 不读；不抛错，但写一行 debug log |
| Skill body_md 为空字符串（合法 DB 行）| use_skill 返回 error result "技能 '{name}' 内容为空，请联系配置者更新"；**不**注入空 body 到 messages（避免 LLM 看到无内容的载入提示而困惑）；narration 推送 `phase:"error"` event |
| use_skill 调用时 Skill.is_active=false（被父账户软删除但 binding 仍在）| 返回 error result "技能 '{name}' 已被禁用"；同 fallthrough to 跨租户分支处理（不暴露存在性的程度需 S2 拍板）|

### 权限规则

- **runtime 层**：use_skill 工具的 owner 通过 ctx `parent_user_id` lookup Skill；跨 `parent_user_id` 返回 error result（不抛 + 不暴露存在性）
- **API 层**：本 feature **不新增 API**；所有 binding 查询通过 #1 已实现的 `biz/skill` 包做 owner 校验
- **配置者**：父账户（`parent_user_id=null`）；子账户（含子账户在 use_skill 时通过其 parent_user_id 解析 Skill owner）
- **管理端**：不动

### UI 行为规格

唯一前端改动：`numind-web-v3` chat 流的 ToolBubble 组件加 `skill_use` event 类型 case。

- **页面位置**：学员对话页（`/chat/agent/:agent_id` 或对应路由），chat message stream 区域的工具调用气泡
- **布局**：与现有工具气泡同布局（蓝色背景圆角卡片），不新增 UI 组件
- **交互**：纯展示，无交互（点击不展开 Skill body——body 在 LLM context 里不直接给学员看）
- **状态**：
  - `phase=loading`（极短瞬间）→ "📚 正在加载技能：销售话术训练..."
  - `phase=loaded`（≤50ms 后切换）→ "📚 已调用技能：销售话术训练"
  - `phase=error` → "⚠ 技能加载失败：{error_message}"，红色边框（按现有 error 气泡样式）
- **响应式**：移动端布局自适应（chat 气泡已实现，无新增工作）

---

## §5 已识别的开放问题（S2 阶段拍板）

继承 S0 §7 的 8 个开放项 + S1 新增 4 个：

S0 留下来的：
1. Skill 目录在 system prompt 的位置 — **S1 拍板**：扩展段位 [3] body（不新增段位，保 6 段 invariant，见 §3 invariant 兼容性行）
2. Skill body 注入对话上下文的角色（候选：assistant / tool result / system reminder）— S2 跑 Eino prototype 比较
3. turn-scope tool gate 实现方式（候选 a：permission pipeline 内新 validator；候选 b：独立 EinoToolWrapper）— S2 跑 prototype 比较
4. use_skill 调用上限默认值 — **S1 拍板**：3 次/turn（已写入 S0 §3 AC-6，无开放）
5. Skill body 大小与 compactv2 阈值兼容（候选：use_skill 注入时给 compactv2 hint）— S2 拍板
6. Skill 目录排序 — **S1 拍板**：binding.sort_order asc（与 v1 一致，配置者已熟悉；调用频次需埋点 + 1 周数据，过度设计）
7. 多 binding 同名 Skill 防御 — **S1 拍板**：依赖 #1 UNIQUE(parent_user_id, name) 约束 + runner 启动时 defensive check，若发现重名 → fatal log + 拒绝启动 Run（防御性硬错）
8. Eino 工具 schema 支持中文工具名 — S2 写 integration test 验证

S1 新增：
9. **Skill 元数据缓存策略**：每个 Run 启动时一次查询缓存（避免每 turn 重查 binding），S2 拍板缓存粒度
10. **compactv2 集成接口**：use_skill body 注入时是否需要给 compactv2 提示（让它优先 prune 这段而非用户输入）— S2 拍板
11. **前端 ToolBubble 文件路径**：S2 阶段 git grep 确认实际文件
12. **use_skill 测试 fixture**：Go unit test 怎么 mock Skill store + binding store（依赖 #1 的接口定义）— S3 task plan 编排

---

## §6 与 #1 #3 的接口契约

### 本 feature 对 #1 的依赖（输入）

| 资源 | 假设契约 | 验证时机 |
|---|---|---|
| `skill` 表 schema | 含 `id, parent_user_id, name, description, when_to_use, allowed_tools(JSON), body_md, version, is_active` | S3 plan 起草前 git fetch 验证 |
| `agent_skill_binding` 表 schema | 含 `id, agent_id, skill_id, sort_order` | 同上 |
| `biz/skill.Service.GetByID(ctx, skillID) (*Skill, error)` | 按 id 查 Skill | 同上；缺则本 feature 在 worktree 内补 |
| `biz/skill.Service.GetByNameForUser(ctx, parentUserID, name) (*Skill, error)` | 按 name 查 Skill（owner 校验） | 同上 |
| `biz/skill.Binding.ListByAgent(ctx, agentID) ([]*Binding, error)` | 列出 Agent 所有 binding（按 sort_order asc） | 同上 |
| `agent_definition.generated_skill_body / custom_skill_body` 字段保留 | dual-read fallback 用 | 已 land |

### 本 feature 对 #3 的输出（影响）

- `use_skill` 工具的语义对 #3 marketplace 暴露——marketplace 订阅来的 Skill 走同一个 use_skill 入口，#3 不需要额外 runtime 改造
- `skill_use` narration event 是稳定 API，#3 不会扩展（marketplace 不影响学员视角气泡）
- BudgetTracker 计入 use_skill 是稳定行为，#3 marketplace 订阅 Skill 也按同样规则计费

---

## §7 S1 自评：相对 S0 改进点

| S0 模糊 / 缺失 | S1 补齐 |
|---|---|
| AC-2 实现细节固化（reviewer P0） | 已改为实现无关描述 |
| S5 验证策略雏形缺失（reviewer P1） | §3 +AC-11 + 已写候选 + 3 关键 user path |
| AC-8 不可机械验证（reviewer P1） | 改为"指引词典 substring 断言" |
| 估时无依据 | §2 用 7 task × ~1 天展开 |
| 风险表未量化 compactv2 影响 | §3 新11/12 新增缓存 + compactv2 接口风险 |
| 与 #3 接口未声明 | §6 输出契约清单 |
| use_skill 调用率 / 行为生效率无指标 | AC-11 ≥30% 调用率 + S5 手工统计 |

---

## §8 备注

- 本 feature 完成 = v2 三件套核心价值已交付，#3 marketplace 是商业放大不阻塞产品
- 按 agent-mode autopilot 规则：S0/S1/S2/S3 不停顿等父账户硬门禁；S7 prod 部署不做
- 下一步：S2 brainstorming（输入本 PRD 的 §4，输出 spec 到 docs/superpowers/specs/）
