# NDF S0 Requirement Card · `agent-mode-v2-skill-invocation`

**Track**：Standard
**Feature ID**：`agent-mode-v2-skill-invocation`（v2 第 2 个 feature，**v2 三件套之二**）
**起草日期**：2026-05-24
**起草人**：AI（cold-start prompt from 父账户，agent-mode autopilot 自主推进）
**状态**：S0 草案
**硬依赖**：`agent-mode-v2-skill-as-artifact`（#1）必须先 land develop —— 需要 `skill` 表、`agent_skill_binding` 表、`biz/skill/` 包就位
**并发关系**：与 #1 在不同 worktree 并行写需求/spec；S4 编码前必须等 #1 完成
**阻塞**：`agent-mode-v2-skill-marketplace`（#3）依赖本 feature 的运行时 use_skill 模型

---

## 1. 起因（Why now）

### v2 #1 留下的"装载关系"是 DB 行，runtime 还在按 v1 跑

v2 #1 完成后的状态：

| 存在 | 状态 |
|---|---|
| `skill` 表 | ✅ 父账户能 CRUD |
| `agent_skill_binding` 表 | ✅ 父账户能把 Skill 装载到 Agent |
| `skill_history` 表 | ✅ 版本回滚可用 |
| `biz/skill/{service,binding,migration,frontmatter,versioning}.go` | ✅ 编排层就绪 |
| `agent_definition.generated_skill_body` / `custom_skill_body` | ⚠ 标 deprecated，但 runtime 仍在读 |
| **运行时（`biz/agent/runner.go`）按 binding 调用 Skill** | ❌ **本 feature 任务** |

**矛盾**：父账户在管理台装载了 5 个 Skill 到 "销售训练" Agent，但学员对话时 LLM 完全感知不到这些 Skill 存在——runner.go 还是从 `agent_definition.generated_skill_body` 一次性拼装 system prompt，binding 表只是"装饰"。

### 父账户终态描述（2026-05-24 对话，本 feature cold-start 引用）

> "agent 是 agent，agent 有自己的提示词约束和其它 harness 约束，而 skills 是可以被单独管理、编写的技能型文件，可以被 agent 调用"

关键动词：**"被 agent 调用"**。这意味着 Skill 不是 system prompt 的一部分（静态拼接），而是 Agent runtime 在对话过程中**按需载入**的资产——Claude Code 风格：

- Skill 元数据（name + description + when_to_use）作为"目录"注入 system prompt，让 LLM 知道有哪些 Skill 可用
- Skill body 在 LLM 主动 emit `use_skill("foo")` tool-call 时才载入 turn 上下文
- Skill 自带的 `allowed_tools` 在 Skill 被调用的 turn 内**临时扩展** Agent 工具白名单（turn 结束后撤销）

### v2 三件套总览（本 feature 在中间）

| # | Feature | 范围 | 与本 feature 关系 |
|---|---|---|---|
| 1 | `agent-mode-v2-skill-as-artifact` | DB + CRUD + UI + 数据迁移 | **硬依赖** |
| 2 | **agent-mode-v2-skill-invocation** | **运行时 use_skill + system prompt 注入 + tool 白名单临时扩展 + narration + budget + dual-read 兜底** | **本 feature** |
| 3 | `agent-mode-v2-skill-marketplace` | 跨租户脱敏发布订阅 | 依赖本 feature 的 use_skill 模型 |

**本 feature 唯一职责**：让 Agent 的 LLM 在对话中能"调用"装载的 Skill。**不动 DB schema、不动 CRUD API、不动 UI**——这些都是 #1 的事。本 feature 在 `numind-web-v3` 端只加 **narration 渲染**（在 chat 流里显示"📚 调用技能：{name}"），不加任何配置页面。

---

## 2. 业务范围

### 关键术语统一（继承 #1）

| 术语 | v2 含义 |
|---|---|
| **Skill** | `skill` 表的一行：name + description + when_to_use + allowed_tools(JSON) + body_md |
| **装载关系** | `agent_skill_binding(agent_id, skill_id, sort_order)` |
| **use_skill tool** | runtime 注册到 `AgentToolRegistry` 的平台工具，唯一参数 `name string`，LLM 通过 emit tool-call 触发 Skill body 载入 |
| **Skill turn** | 一次 LLM 调用 `use_skill` 到下一次 user 输入之间的 turn 范围；该 turn 内 Skill 的 allowed_tools 临时合并进 Agent 工具白名单 |
| **dual-read 兜底** | 当 Agent 没有任何 binding 时，runtime 回退到 v1 行为：读 `agent_definition.generated_skill_body` |

### In scope（本 feature 必交付）

#### 2.1 后端 runtime 改造（`numind-server`）

**A. `biz/agent/tool_use_skill.go`（**新建**）：注册 `use_skill` 工具**

- 实现 `AgentTool` interface（与现有 8 个内置工具同构，见架构 §6.4）
- 参数 schema：`{ "name": string }`（必填，Skill name 在父账户租户内唯一——靠 #1 spec 的 UNIQUE(parent_user_id, name) 约束）
- Invoke 行为：
  1. 通过 `biz/skill/service.go` lookup `parent_user_id + name` → 拿到 `Skill.body_md` + `allowed_tools`
  2. 把 body_md 作为 **assistant 消息** append 到当前 turn 的 messages（不是 system reload，避免破坏 Eino ReAct 内部状态）
  3. 把 `allowed_tools` 合并到一个**调用栈级别**的"该 turn 启用的 tool 集合"中（详见 §2.1.C）
  4. 发 narration "📚 调用技能：{name}"
  5. 返回 tool result "技能 '{name}' 已载入，继续根据该技能指引完成任务"
- 错误路径：name 不存在、跨租户、装载关系不存在 → 返回结构化错误 result（**不抛**，让 LLM 自己处理）；超过 turn 内 use_skill 次数上限（默认 3）→ 返回 result "已达本轮技能调用上限"

**B. `biz/agent/runner.go` 改造：system prompt 注入 Skill 目录**

在 §4 Skill body 装载块之后、6 段 prompt 拼接之前，新增"段位 2.5"——已装载 Skill 目录：

```
## 可用技能
你装载了以下技能。当对话需要某个技能时，使用 use_skill(name="<技能名>") 工具调用它，
工具会把技能详细指引载入对话上下文，并临时启用该技能需要的额外工具。

- 销售话术训练（销售对话拆解 + 改写建议）
  何时使用：学员发来一段销售对话，希望分析或改写
- 客户画像分析（行为/动机/痛点结构化输出）
  何时使用：学员需要快速从碎片信息生成客户画像
```

仅注入 `name + description + when_to_use`（≤200 字/Skill），body_md 不进 system prompt（节省 token，懒加载）。

**C. Runner 工具白名单动态合并机制**

最棘手的设计点（S2 详细方案）：runtime 启动时已通过 `req.ToolNames` 把 Agent 的"基础工具"装入 Eino ReAct，Eino 内部 `react.NewAgent` 把工具列表锁定。

**v2 #2 设计**：runner 在启动前**预先把"该 Agent 所有 binding 装载的 Skill 的 allowed_tools 并集"**全部注册到 Eino，**但**通过一个 **turn-scope tool gate hook**（compliance hook chain 的新一层）控制：默认拒绝非"基础白名单"内的工具，`use_skill` 调用后把该 Skill 的 allowed_tools 加入 turn-scope 允许集合。该 turn 结束（下一个 user 输入到来）→ 重置允许集合回到基础白名单。

**B/C 双轨**：

- runtime 走 binding 路径（如果 Agent 有 ≥1 个 binding） → §2.1.A/B/C 全部生效
- runtime 走 legacy 路径（如果 Agent 0 binding） → 继续读 `generated_skill_body` / `custom_skill_body`，不注入 Skill 目录，不注册 use_skill 工具（dual-read 兜底，保 v1 Agent 零回归）

#### 2.2 binding 查询入口

`biz/skill/binding.go`（#1 已建）需要 v2 #2 用到的查询函数：

| 函数 | 用途 | #1 已有？ |
|---|---|---|
| `ListBindingsByAgent(ctx, agentID)` | runtime 启动时查 Agent 所有 binding | 待 #1 spec 确认；如未实现，本 feature 在 `biz/skill/binding.go` 补充（Tier 2 跨文件协作，不冲突） |
| `GetSkillByNameForUser(ctx, parentUserID, name)` | use_skill 工具按 name 查 Skill | 待 #1 spec 确认；同上 |

**S2 阶段**：与 #1 已 land 的 `biz/skill/binding.go` 对齐，缺哪个函数本 feature 在自己的 worktree 内补。

#### 2.3 narration（`biz/agent/narration/`）

新增 narration segment：当 `use_skill` 工具被 Invoke 时，narration provider 推送 SSE event：

```json
{ "kind": "skill_use", "skill_name": "销售话术训练", "phase": "loading|loaded|error" }
```

前端（`numind-web-v3`）在 chat 流的工具调用气泡里渲染："📚 调用技能：销售话术训练"。

#### 2.4 budget tracker（`biz/agent/budgetgate/`）

`use_skill` 计入当前 turn 的 BudgetTracker.PreToolCall / PostToolCall，与其他工具同等对待。Skill body 长度 → 入 token cost 估算（参照现有 BudgetTracker.RecordUsage）。

#### 2.5 deprecated 字段双读兜底

`agent_definition.generated_skill_body` / `custom_skill_body` / `tool_flags` 标 deprecated（#1 已加注释），本 feature **不删字段**，runtime 优先级：

```
if binding_count(agent_id) > 0:
    新路径（use_skill 模型）
else:
    legacy 路径（v1 行为完全保留）
```

兜底意义：v1 Agent 若未 #1 数据迁移成功（罕见但需防）→ legacy 字段仍 work。

#### 2.6 前端（`numind-web-v3`）

**唯一改动**：narration 渲染层加 `skill_use` event 类型 → chat 流里渲染气泡。

`src/components/chat/ToolBubble.vue`（或对应文件）加 case。无新增页面、无新增路由、无新增 API 调用、无新增 Pinia store。

### Out of scope（**明确不做**）

1. ❌ **DB schema / CRUD API / 管理 UI**：全是 #1 的事
2. ❌ **Marketplace 发布订阅**：#3 的事
3. ❌ **Skill 内嵌 scripts/代码执行**：Claude Code skill 支持 `scripts/` 子目录，v2 #2 也不做（v2.5 评估）
4. ❌ **意图分类器自动 routing**：本 feature 不预决策——LLM 看 Skill 目录自主决定 emit `use_skill`，没有规则引擎/外部分类器
5. ❌ **删 deprecated 字段**：留给 v2 #4（独立 micro feature）
6. ❌ **prod 部署**：本 feature 收尾在 dev，prod 等父账户拍板（按 agent-mode autopilot 规则）
7. ❌ **管理端 admin-web 改动**：不动
8. ❌ **跨 Skill 编排**（一个 use_skill 内嵌另一个 use_skill）：v1 允许 LLM 在一个 turn 内多次 emit use_skill，但禁止递归（hard cap 3 次/turn，防失控）

---

## 3. 业务目标 / 验收标准

### 业务目标

让 Agent 在对话过程中能"按需调用"已装载的 Skill：LLM 根据对话上下文自主判断何时 emit `use_skill`，runtime 把 Skill body 载入 turn 上下文 + 临时扩展工具白名单，让 Agent 的能力**从"配置时一次定死"升级为"运行时动态扩展"**。

### 关键验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| AC-1 | 父账户给 Agent 装载 2 个 Skill 后，LLM 在 system prompt 里能看到这 2 个 Skill 的目录（name + description + when_to_use），通过 Langfuse trace 验证 | Playwright E2E + Langfuse 截图 |
| AC-2 | LLM 主动 emit `use_skill("销售话术训练")` 后，Skill body 被载入当前 turn 的 messages 中（具体 role 由 S2 技术设计决定，候选：assistant / tool result / system reminder），可在 Langfuse trace 的下一次 LLM generation 输入中验证 body 全文出现 | Go unit test on runner.go + Langfuse trace 截图 |
| AC-3 | Skill 自带的 allowed_tools 在 use_skill 后的 turn 内可用；下一个 user 输入到来后撤销 | Go unit test（mock 工具白名单 hook）+ E2E |
| AC-4 | Agent 无任何 binding 时（包括 v1 Agent），runtime 走 legacy 路径，行为与本 feature 上线前完全一致 | 现有 e2e/agent-student.spec.ts 全 pass |
| AC-5 | use_skill 调用错误（name 不存在 / 跨租户 / 未装载） → 返回结构化 error result，LLM 看到后能恢复（不导致 run 失败） | Go unit test |
| AC-6 | use_skill 计入 BudgetTracker，超 turn 上限（默认 3 次）后返回 "已达本轮技能调用上限" | Go unit test |
| AC-7 | narration 在 use_skill 时推送 `{kind:"skill_use", phase:"loading→loaded"}` event，前端 chat 流显示气泡 | Playwright E2E |
| AC-8 | 学员视角 E2E：装载 2 Skill 的 Agent 对话，LLM 自主 emit `use_skill("X")` 后，最终 assistant 回复**含至少 1 个**该 Skill 的 when_to_use 关键词或 body 中明确指引的输出模式（机械判定：用 Playwright 断言 substring 命中；S2 阶段为每个验证 Skill 预定义"指引词典"清单 ≥ 5 词，3 词命中即 pass）| Playwright E2E + 指引词典 substring 断言 |
| AC-9 | Langfuse trace topology 完整：agent_run trace → 每次 use_skill 调用 span → Skill body 载入后的 generation 链 | Langfuse 截图 |
| AC-10 | runtime 启动 Eino agent 前**只装入** Agent 基础工具 + use_skill；Skill 私有工具通过 turn-scope hook 默认拒绝，use_skill 后才放开 | Go unit test（hook chain） |

### 非功能性

- system prompt 注入 Skill 目录后总长度 ≤ 当前 + 800 tokens（≤8 Skill × ~100 token 元数据）
- use_skill tool 平均延迟 ≤ 50ms（纯 DB lookup + memory append，无外部调用）
- runtime 启动延迟（含 binding 查询）≤ 现有 + 30ms
- 现有 e2e 测试零回归（dual-read 兜底保证）

### S5 验证策略雏形（NDF Rule 10 要求 S0/S1 留候选给 S3 确定）

- **候选验证方式**：**Playwright E2E 优先**（涉及 LLM 行为验证 + 前端 narration 渲染 + dual-read 路径切换，三个角度只有 E2E 能端到端覆盖；Go unit test 已覆盖 hook chain / budget cap / 错误路径等纯后端单点）
- **关键 user path**（S5 必须覆盖）：
  1. 父账户给 Agent 装载 2 Skill → 学员视角对话触发 use_skill → 前端气泡正确渲染 + Skill body 影响 LLM 回复
  2. v1 Agent 零 binding → 走 legacy 路径 → 行为与 feature 上线前完全一致（agent-student.spec.ts 全 pass）
  3. use_skill 错误路径（name 不存在 / 跨租户 / 超 cap）→ LLM 收到 error result 后能优雅恢复
- **Langfuse trace 验证**：S5 必须截图 1 个完整 use_skill 调用 trace（agent_run → use_skill span → 下次 generation 含 body）
- **S3 阶段独立 reviewer 确认最终策略**（包括是否需要补 SkillRunner 集成测试 / 是否需要 mock LLM 自动测路径选择）

### NDF 规则 11 适用性

本 feature 起因是 v2 蓝图的下一步（cold-start prompt from 父账户），**非客户 bug 上报**，NDF 规则 11（Bug-from-Customer 强制复现测试）不适用。

---

## 4. Triage

- **推荐轨道**：**Standard**
- **分类理由**：
  1. 数据库 schema 变更：**否**（本 feature 不动 schema，#1 已建表，本 feature 只读）
  2. 新增 API 端点：**否**（本 feature 不加 HTTP 端点；use_skill 是内部 Agent tool，不暴露 REST）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（biz/agent/tool_use_skill.go 新建 + runner.go 改 + budgetgate hook 改 + narration provider 改 + 前端 ToolBubble 改 + 测试）
  5. 高风险业务逻辑：**是**（动 runner.go 影响所有 Agent 运行；dual-read 路径写错 = 全平台 Agent 行为异常；tool 白名单 hook 写错 = 工具权限失控）

条件 4+5 触发 Standard 强制（即使其他三条都是"否"，runner.go 高风险使 Hotfix 不适用）。

- **人类决定**：父账户已通过 cold-start prompt 默认通过本 feature（按 agent-mode autopilot 规则不需要在 S0 硬门禁停顿）

---

## 5. 风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|------|------|------|
| 1 | **#1 spec/CRUD 落地时函数签名/字段名与本 feature 假设不符** | 高 | 中 | S2 阶段对齐 #1 的 spec 文件；S4 编码前再次 git fetch + grep 验证；不一致用 adapter pattern 包一层 |
| 2 | **Eino ReAct 不支持运行时动态扩展工具列表** | 中 | 高 | 改为"启动时预注册所有 binding 的 allowed_tools 并集 + turn-scope hook gate"方案（详见 §2.1.C） |
| 3 | **dual-read 路径写错导致 v1 Agent 行为变化** | 中 | 极高 | 主路径切换前先跑 e2e/agent-student.spec.ts 全套；hot path 加 unit test 覆盖 binding_count=0 分支 |
| 4 | **LLM 不主动调 use_skill（不理解 Skill 目录语法）** | 中 | 中 | system prompt 段位 2.5 用明确的中文 + 一个示例触发条件；S5 验收手工测 2-3 个场景 |
| 5 | **use_skill 递归/无限调用导致 budget 耗光** | 低 | 高 | 硬 cap 3 次/turn；超限返回 result 而非异常；turn-scope 计数器 reset on user message |
| 6 | **Skill body 注入后 Eino 内部 messages 顺序混乱**（Skill body 是 assistant msg 但实际不是 LLM 输出） | 中 | 高 | 用 Eino schema.Message 的 ToolMessage role 包装；S2 阶段验证 Eino 对 tool-result-as-assistant 的处理 |
| 7 | **tool 白名单 turn-scope hook 与现有 compliance/permission hook chain 冲突** | 中 | 中 | hook chain 顺序固定（compliance → permission → budget → sandbox → narration），新增 hook 插在 permission 后；S2 详细设计 + S4 集成测试 |
| 8 | **narration `skill_use` event 类型前端没处理 → 静默无显示**（仅气泡缺失，无报错） | 低 | 低 | S5 浏览器 QA 必查；ToolBubble.vue 加 default fallback case |
| 9 | **#1 数据迁移失败/不完整导致部分 Agent 既无 binding 又无 generated_skill_body** | 低 | 中 | dual-read 路径 fallback 到空 body（不抛错）；记录 warn log；S5 验证 |
| 10 | **配置者改了 Skill 的 allowed_tools，正在跑的 Agent run 使用旧白名单** | 低 | 低 | 每次 use_skill 都重新 lookup Skill（不缓存），下次 turn 即生效；接受当前 turn 用旧白名单（无业务影响） |

---

## 6. 仓库与估时

- **仓库**：`numind-server` + `numind-web-v3`（admin-web 不动）
- **估时**：2 周（含 S0-S6 全流程；S4 编码主体 ~7 工作日）。比 #1 短一周——本 feature 没有 DB schema/CRUD/UI 大块。
- **worktree**：
  - numind-server: `/private/tmp/wt-agent-mode-v2-skill-invocation-numind-server`
  - numind-web-v3: `/private/tmp/wt-agent-mode-v2-skill-invocation-numind-web-v3`
- **S4 启动硬阻塞**：`origin/develop` 已含 `skill` 表 migration + `biz/skill/service.go` 文件（#1 已 land）

---

## 7. S0 待解决项（留给 S1/S2）

1. **Skill 目录在 system prompt 的位置**：插在 `body` 之后、`memoriesSectionHeader` 之前——S2 确认
2. **Skill body 注入对话上下文的角色选择**：assistant msg / tool result / system message？S2 选最不破坏 Eino 内部状态的方案
3. **turn-scope tool gate 实现方式**：放进现有 hook chain 还是单独包一层 EinoToolWrapper？S2 拍板
4. **use_skill 调用上限**：3 次/turn 默认是否合适？S2 与父账户对齐
5. **Skill body 大小**：#1 spec 上限 50KB ≈ 1.2 万 token，注入 turn 后是否触发 compactv2？S2 验证不会破 25K/35K threshold
6. **Skill 目录排序**：按 binding 的 sort_order，还是按调用频次？v1 用 sort_order，S2 确认
7. **多 binding 同名 Skill**（不应发生，但防御）：#1 应保证 UNIQUE(parent_user_id, name)，本 feature 假设 + 防御性 panic（S2 确认）
8. **Eino 工具 schema 是否支持中文工具名**（"销售话术训练" 作为 use_skill 参数值）：S2 阶段写 Eino integration test 验证

---

## 8. 备注

- 本 feature 完成后，#1 + #2 联合交付**最小可用 Skill-as-资产 + 运行时调用闭环**，父账户可以"装载多个 Skill 到一个 Agent，Agent 学员对话时自动调用 Skill"
- 后续 #3 marketplace 是商业放大，不是技术阻塞——本 feature 不为 marketplace 预留任何特殊接口（YAGNI）
- 本 feature 完成 = v2 三件套**核心价值已交付**，#3 是变现层（卖 Skill 订阅、跨租户脱敏分发）
- agent-mode autopilot 规则：不停顿等父账户硬门禁，每个 stage gate 走 AI 自评 + Sonnet reviewer 双确认；不部署 prod
