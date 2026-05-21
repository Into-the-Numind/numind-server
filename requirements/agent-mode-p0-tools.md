# Feature: agent-mode-p0-tools (S0 Requirements)

> Track: **standard** · Stage: **S0** · Repos: **numind-server + numind-web-v3** · Created: 2026-05-22

---

## 1. 问题陈述（Problem Statement）

agent-mode（蓝本 v1 文档）已落地 14 个 feature，Runtime / Tool Registry / Skill / Memory / Permission / Compliance / Billing / Narration / Sandbox / Compact 全栈就绪。但当前内置工具集合**只有 8 个**——`kb_search` / `learner_data_query` / `document_generate` / `image_gen` / `bash_exec` / `get_current_date` / `memory_write` / `memory_read`——对比 Claude Code、ChatGPT Agent、Coze、Cursor 等已成熟产品，**教育场景下的 4 个 P0 工具完全缺失**：

| 缺失工具 | 用户痛点 | 影响场景 |
|---------|---------|---------|
| `web_search` | 学员问"最近政策"/"今年比赛"/"今日新闻"时 agent 只能凭训练数据回答，常常过时或编造 | 高考志愿、竞赛信息、行业资讯、最新政策类任务 |
| `web_fetch` | 学员发链接（如教育部公告 / 微博文章 / 知乎专栏）让 agent 读，agent 无法访问 | 链接型任务、文档解读、外部资料消化 |
| `ask_user_question` | agent 遇到歧义（"你说的小红是哪个班的"/"你是想看 A 方案还是 B 方案"）只能猜或写一大堆假设 | 任务起步信息不全、决策分支选择、个性化偏好收集 |
| `file_read` | 学员上传 PDF/图片/docx 后 agent 无法按 ID 读（kb_search 只能检索已索引的 corpus，不是按文件 ID 读完整内容） | "帮我读这份 PDF"、"看这张作业图片"、"消化这个 docx" |

**核心断言**：4 个工具中 3 个是几乎所有成熟 agent 产品的标配（web_search / web_fetch / file_read），最后 1 个（ask_user_question）是莫小派教育场景特有的**对话连贯性刚需**——B 端机构主和 C 端学员都需要 agent 在歧义时礼貌反问而不是冒进。

**为什么先做这 4 个不做其他**：
1. 与教育场景直接相关（vs Calendar API / Email 这类与产品弱相关的）
2. 不引入新业务实体（vs Notion API / Drive API 这类需要 OAuth + 凭据存储）
3. 实现成本可控（每个工具 < 500 LOC backend + 0-200 LOC frontend）
4. 都能从 agent 已有的 hooks / state machine / billing / Langfuse 基础设施直接受益，无需独立架构改造

---

## 2. 实施范围（Scope）

### 2.1 numind-server 后端

**新增 4 个 PlatformTool（遵循现有 8 工具的 flat-file 命名约定）：**

- `internal/numind/biz/agent/tool_web_search.go` + `tool_web_search_test.go`
- `internal/numind/biz/agent/tool_web_fetch.go` + `tool_web_fetch_test.go`
- `internal/numind/biz/agent/tool_ask_user_question.go` + `tool_ask_user_question_test.go`
- `internal/numind/biz/agent/tool_file_read.go` + `tool_file_read_test.go`

**State machine 改造（最关键，影响 runner.go）：**

- `internal/numind/biz/agent/state.go` 增加：
  - `TerminalReason`: 新增 `TerminalWaitingForUserChoice = "waiting_for_user_choice"`（第 14 个）— 表示 run 进入"挂起等用户回答"状态
  - `LoopEvent`: 新增 `LoopEventAskUserPaused`（第 20 个）— state machine 跳出 ReAct loop 的事件
  - `Transition()` 增加新事件 case：`LoopEventAskUserPaused → ("", "", true)` 中止 loop 但**不**写 final completion（特殊：semi-terminal）

- `internal/numind/biz/agent/runner.go` 改造：
  - `ask_user_question` tool 的 `Execute` 返回**特殊 sentinel error**（`ErrYieldForUserQuestion`）
  - runner 捕获 sentinel → 立即停 ReAct loop → 发 `LoopEventAskUserPaused` → 把 question payload 通过 narration / SSE 推到前端 → run.state_reason = `waiting_for_user_choice`
  - 用户从前端 POST 答案 → 后端 inject 为下一轮 user message → reopen run（同一 run_id）→ 恢复 ReAct loop

- `internal/numind/biz/agent/agent_run` 数据模型可能需要新字段：`pending_question` (JSON)、`pending_question_at` (timestamp)，存"挂起的问题"以便前端 reconnect 时还能渲染（S2 决策）

**新 API endpoint（用户端 user_token）：**

- `POST /v1/agent/sessions/:run_id/answer` — 接收用户对 `ask_user_question` 的答案
  - body: `{ selected: ["option-key-1"], free_text?: string }`
  - 通过 user_token middleware；biz 层校验 run 归属当前 user_id
  - 校验 run.state_reason == "waiting_for_user_choice"；否则 400
  - 把答案打包为 user message，inject 到 run.messages，重启 ReAct loop（同 run，新一轮）
  - 返回 `{ run_id, status: "resumed" }`

**新增 controller**：`internal/numind/controller/v1/agent.go` 已存在，加 `AnswerPendingQuestion` handler。

**File 上传基础设施**（前置依赖判定）：

- **S2 强制调研**：grep 仓库找 `multipart` / `Upload` / `c.FormFile` 看有没有现成的 user-scoped file upload endpoint。
  - **若有**（如 sales_rag 的文档上传）：复用 + 加 `agent_file` 表追踪文件属于哪个 agent_run（用 file_id 隔离 user scope）
  - **若无**：S2 决策点 — 拆分 sub-feature `agent-mode-file-upload-infra` 单独跑，或纳入本 feature 范围
- 上传 endpoint（预期）：`POST /v1/agent/files/upload` 返回 `{ file_id, mime_type, byte_size }`

**Provider/外部服务集成（必经 aiservice 入口）：**

- `web_search` provider（S2 选型）：候选 Tavily / Serper / Bing / DuckDuckGo
  - 必经 `aiservice` 入口包一层 wrapper（即使不是 LLM）— 走 Langfuse Span 记录
  - 配置项进 `config_*.yaml` 的 `web_search.*` 段（API key 不写代码）
- `web_fetch` 复用 `internal/pkg/httpclient`，HTML→Markdown 用 `go-readability` 或 `html-to-markdown`
- `file_read` PDF parser 复用 `qwen-long`（DashScope compatible-mode，已在 aiservice）；图片复用百度/阿里 OCR；docx 用 `unioffice`

**Tool 注册**：

- `internal/numind/biz/agent/factory_platform.go` 的 `getAllBaseTools` 列表追加 4 个新 tool 实例
- `internal/numind/biz/biz.go` 装配链按需添加新依赖注入（web_search provider client / file storage）

**可能涉及的 DB schema 变更**（S2 锁定）：

- `agent_file` 表（如新建）— file_id PK + user_id FK + agent_run_id FK + mime_type + storage_path + byte_size + uploaded_at + status
- `agent_run` 表加 `pending_question_json` 字段（可空，JSON 类型）记挂起问题

### 2.2 numind-web-v3 前端

**新增组件：**

- `src/components/agent/QuestionPrompt.vue` — 渲染 `ask_user_question` 的多选 UI，emit `answer-submitted` 事件
  - props: `{ question, options: Array<{label, description?}>, header?, multiSelect, runId }`
  - 内部状态：selected[], freeText
  - 点击"提交" → POST `/v1/agent/sessions/:run_id/answer` → emit 给父
- `src/components/agent/AttachmentUploader.vue`（如 file 上传 UI 不存在）— chat 输入框旁的附件按钮，调 `/v1/agent/files/upload`

**改造组件：**

- `src/views/agent/AgentChatView.vue` 监听 SSE event `tool_call_yield`（type=ask_user_question）→ 渲染 QuestionPrompt
- chat 消息流处理：当收到 `waiting_for_user_choice` 状态 → 暂停输入框，等待用户在 QuestionPrompt 选完恢复

**API：**

- `src/api/agent.ts` 加 `postAgentAnswer(runId, payload)` 包装
- 如有 file upload UI 改动 → `src/api/agent.ts` 加 `uploadAgentFile(formData)` 包装

**SSE 协议（前后端约定）：**

- 新增 SSE event type: `tool_call_yield` — payload 含 question/options/header/multiSelect/run_id
- 新增 SSE event type: `run_resumed` — 用户答完后后端 inject + 重启时通知前端继续监听 stream

### 2.3 numind-admin-web 内部管理端

**0 改动。** P0 工具是给 agent runtime 用，不需要 admin CRUD UI（admin 已有 `tool_definition` 管理界面在 #5 的 e2e-rollout，新工具通过 seed 自动注册即可）。

---

## 3. Out of Scope（明确划线）

### 3.1 工具能力相关

- ❌ **per-Agent tool whitelist**（父账户配置 agent 时勾选"这个 agent 允许用 web_search 吗"）— 是 P1 独立 feature，本 feature 4 工具默认对所有 agent 可见
- ❌ **web_search 的 DB 持久化 cache** — 用 in-memory TTL cache 即可（5 分钟），DB cache 是后续优化
- ❌ **file_read 的 chunk 分页查询** — 一次性读完，超长截断到 200KB + 标注 `truncated=true`
- ❌ **ask_user_question 的多模态回答**（语音回答 / 图片回答）— v1 仅文本+按钮多选
- ❌ **web_fetch JavaScript 渲染**（headless browser）— v1 仅静态 HTML→Markdown，SPA 类页面读不到内容是已知限制
- ❌ **重构现有 8 个工具** — 本 feature 只加新的，不动旧代码

### 3.2 部署/上线相关

- ❌ **Prod 部署** — dev 部署 OK；prod 部署文档可写但不执行
- ❌ **打 git tag v\***  — tag 由用户手动
- ❌ **修改 `config_prod.yaml`** — prod 配置由用户/运维手动 sync

### 3.3 实验性能力

- ❌ **Per-agent budget for web_search**（"这个 agent 一天最多 5 次 web_search"）— budget 用现有 BudgetTracker（按 credits 算钱即可）
- ❌ **Citation tracking**（agent 输出引用了哪个 search result）— v1 让 LLM 自己在回答里 inline 引用，不做结构化 citation 字段
- ❌ **A/B testing 多 provider**（同时调 Tavily + Serper 对比质量）— v1 只接一个 provider，由 S2 决策

---

## 4. 业务目标 / 验收标准

### 4.1 业务目标

**学员侧（C 端）核心收益：**
- 能让 agent 处理"最新政策""今年比赛""昨天新闻"等时效性强的开放问题
- 能给 agent 发链接说"读这个"，agent 读得到
- agent 在歧义场景下能反问而不是瞎猜
- 上传 PDF/图片后能让 agent 真读到内容（不止 RAG 检索）

**配置者侧（B 端机构主）核心收益：**
- 配置 agent 时一句 system prompt 就可以让 agent "如果学员问最新政策记得搜索"——4 工具自动可见
- 不用再为常见教育场景定制 SOP 卡，agent 能自主组合工具完成

**Numind 平台收益：**
- 拉齐与 Claude Code / Coze / Cursor 等成熟 agent 产品的能力门槛
- 后续 Bookend P1 features（agent skills / 多模态 / 文件版本管理等）的基础

### 4.2 验收标准（PRD §4 等价）

| # | 用户故事 | 验收条件 |
|---|---------|---------|
| US-1 | 作为学员，我希望 agent 能搜最新信息 | agent 收到"最近教育部公告"类问题，自动调 web_search → 返回 ≥3 条结果 → narration 输出"搜到 3 篇，最新的是 X" |
| US-2 | 作为学员，我希望发链接让 agent 读 | agent 收到含 URL 的消息，调 web_fetch → 返回 Markdown 摘要 → narration 输出"读完了，主要讲 X" |
| US-3 | 作为学员，我希望 agent 不懂时反问 | agent 遇到歧义（如"帮我看小明的"），调 ask_user_question 弹出多选 → 前端渲染按钮 → 学员点击 → agent 用答案继续 |
| US-4 | 作为学员，我希望上传 PDF/图后 agent 能读 | 学员通过附件按钮上传文件 → agent 拿到 file_id → 调 file_read → narration 输出"读完了，第 3 页提到 X" |
| US-5 | 作为父账户（B 端），我希望默认所有 agent 都可用 4 工具 | 父账户新建/编辑 agent 时，4 工具在 tool registry 列表中可见且默认勾选；存到 agent_definition.allowed_tools 表 |
| US-6 | 作为系统，我希望 4 工具调用都有 Langfuse trace | 每次工具调用在 Langfuse UI 看得到 Generation（LLM-based 如 file_read PDF parser）/Span（非 LLM 如 web_search/web_fetch/ask_user_question）|
| US-7 | 作为系统，我希望 ask_user_question 的 yield-turn 不破坏 BudgetTracker | yield 时 BudgetTracker 暂停，用户答完恢复，credits 计算正确（不把"等用户"时间算成 agent 耗时）|

### 4.3 性能/质量预期

- web_search p50 延迟 < 2s（含 provider 调用 + JSON 处理）
- web_fetch p50 < 3s（含 HTTP fetch + HTML→Markdown）
- file_read PDF p50 < 5s/页（qwen-long 主要瓶颈）
- ask_user_question 后端处理 < 100ms（不含用户回答时间）
- 单元测试覆盖：4 工具每个 happy path + 至少 1 个 error path + 1 个权限边界
- 至少 1 个 Playwright e2e 覆盖 ask_user_question 完整流程（最复杂的 yield turn）

---

## 5. Triage

- **推荐轨道**：**Standard**
- **分类理由**（违反 Hotfix 5 条标准至少 4 条）：
  1. ✅ 数据库 schema 变更：**是**（agent_file 表 可能新建 + agent_run 可能加 pending_question_json 字段，S2 确定）
  2. ✅ 新增 API 端点：**是**（至少 `POST /v1/agent/sessions/:run_id/answer`，可能还有 `POST /v1/agent/files/upload`）
  3. ✅ 新外部服务集成：**是**（web_search provider — Tavily/Serper/Bing 等，新引入第三方 API）
  4. ✅ 影响文件数：**>3**（8+ 后端文件 + 2-3 前端文件 + 配置 + migration + 测试）
  5. ✅ 高风险业务逻辑：**是**（state machine 改造影响 ReAct loop 终止条件 + billing 在 yield-turn 的暂停语义 + Langfuse trace 链路连贯性）
- **5/5 全违反 → Standard 强制**。无可降级路径。

- **人类决定**：已由用户在 SessionStart 任务 brief 中确认 Standard

---

## 6. 相关 feature（reference only）

- **agent-mode-runtime-skeleton** (#2/14, merged 45770bb5) — LoopState / RunHooks 接口；本 feature 用 state.go 加新枚举
- **agent-mode-tool-registry** (#3/14) — ToolFactory + PlatformToolFactory；本 feature 通过 PlatformToolFactory.getAllBaseTools 注册 4 新工具
- **agent-mode-skill-system** (#5/14, merged e05498b6) — agent_definition.allowed_tools；新工具默认进 allowed_tools 白名单
- **agent-mode-memory-system** (#7/14, merged 49c8ab67) — memory tool 是 yield-style tool 的先例（不过 memory tool 不 yield turn）；本 feature 的 ask_user_question 是首个**真正 yield turn** 的工具，state machine 改造是新模式
- **agent-mode-narration-layer** (#8/14, merged 124e62b4) — narration provider；4 工具都要走 narration（"搜到了..."/"读完了..."/"请选择..."）
- **agent-mode-billing-integration** (#12/14, merged bd988fd5) — BudgetTracker；本 feature 在 yield-turn 时调 BudgetTracker.Pause()，恢复时 Resume()
- **agent-mode-e2e-rollout** (#14/14, merged) — 提供 tool_definition seed 机制；4 新工具通过 migration seed 自动注册
- **agent-mode-compliance-3layer** (S5 ACCEPTED, 即将 S6) — compliance gate 包工具调用；4 新工具默认走同 hook chain（compliancegate.WrapHooks）

---

## 7. 已知风险/约束

- **R1: web_search provider 选型** — 不同 provider 有不同的费用/质量/合规属性；S2 阶段必须由 reviewer 独立审议选型理由
- **R2: ask_user_question state machine 改造** — 这是 agent runtime 第一个**真正暂停 run** 的 case，state machine + BudgetTracker + Langfuse trace 三层都要协同；S2 spec 必须画出完整时序图
- **R3: 跨账户 file 隔离** — 上传文件如 file_id 不绑定 user scope，父账户可能读到别人的文件。S2 spec 必须明确文件归属与子账户的关系（用 child_user_id 还是 parent_user_id 隔离？)
- **R4: 与并发 feature 冲突** — 当前 `agent-mode-configurator-relocate` 占用 numind-web-v3 worktree (feature 分支 = `feature/agent-mode-configurator-relocate`)，本 feature 用不同 worktree + 不同 feature 分支（`feature/agent-mode-p0-tools`），文件交集需 S3 task 拆分阶段确认
- **R5: 与 agent-mode-compliance-3layer 协调** — compliance gate 已是 hook chain；新工具如果触发 outbound HTTP 是否需进 compliance audit？S2 必须明确
- **R6: web_fetch 的 SSRF 安全** — 必须拒绝 localhost / private IP / .local；S2 spec 强制定义 IP 解析 + allow/deny list

---

## 8. 备注

- 本 feature 自主推进（agent-mode autopilot 规则）— S0 → S6 dev 部署不停顿，仅 prod 操作必须用户明示
- 单 session 可能不够，必要时写 session-handoff 给下一 session 续跑
- 本 feature 不修改 `architecture-v1.md`（本地草稿），但 S6 完成后**必须**更新该文件的工具清单 8 → 12（如该文件后续入 git）
