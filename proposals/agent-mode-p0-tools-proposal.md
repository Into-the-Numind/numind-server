# agent-mode-p0-tools — 提案 + PRD

> Stage: S1 · Track: standard · Repos: numind-server + numind-web-v3 · Date: 2026-05-22
> Predecessor artifact: numind-server/requirements/agent-mode-p0-tools.md (S0, commit e02cdf95)

---

## §1 方案概述 [客户可见]

### 问题

学员用莫小派 agent 模式时常遇到下面 4 类卡点：

1. **agent 知道的太老** — 问"最近高考新政"/"今年竞赛报名"，agent 凭训练数据答，要么过时要么编造
2. **链接发了也白发** — 学员发个微信公众号链接说"这篇你看下"，agent 无法访问外网
3. **agent 太刚** — 学员说"小明那道题"，agent 不会反问"你是说小明哥还是小明姐"，直接编一个继续
4. **文件传了读不到** — 学员上传 PDF / 图片 / 试卷，agent 拿不到完整内容（kb_search 只是"片段检索"，不是"通读"）

### 解决方案

给 agent 加 4 个内置工具，agent 自主决定何时调：

- **web_search 网络搜索** — 实时搜新闻 / 政策 / 资讯，第三方专业搜索 API 兜底（候选 Tavily / Serper / Bing，S2 选型）
- **web_fetch 网页读取** — 拉学员发的 URL，HTML 转 Markdown 喂给 LLM
- **ask_user_question 反问** — agent 遇到歧义时礼貌发问，前端渲染按钮，学员点完继续
- **file_read 读文件** — 按学员已上传的文件 ID 完整读 PDF / 图片 / 文本

### 预期效果

| 场景 | 现在 | 加完之后 |
|------|-----|---------|
| 学员问"今年高考英语考试时间" | agent 说"我训练数据停在 X，建议自查" | agent 自动调 web_search → 反馈最新时间 + 来源 |
| 学员发链接"这篇报告你看下" | agent 说"我看不到链接内容" | agent 调 web_fetch → 总结要点 |
| 学员说"我表姐想报国防科大" | agent 编一段不知所云的回答 | agent 反问"是想问录取分数线、报考流程，还是学校情况？"（按钮）|
| 学员上传一张试卷拍照 | agent 只能 RAG 检索类似题，不会真"看图" | agent 调 file_read → OCR 后逐题分析 |

### 与莫小派现有体系的衔接

- 4 工具默认对所有 agent 可见（无需父账户配置每个 agent 勾选哪些）
- 都自动走 compliance 审计、credit 计费、Langfuse 追踪（agent runtime 现有基建直接受益）
- ask_user_question 是**首个真正暂停 run 等用户回答**的工具，agent runtime 状态机增加 1 个终止枚举 + 1 个事件枚举

---

## §2 工时估算 [客户可见]

### 模块拆分

| 模块 | 后端 LOC | 前端 LOC | 测试 LOC | 估时 |
|------|---------|---------|---------|------|
| web_search tool（含 Tavily provider 集成）| ~300 | 0 | ~200 | 0.5 天 |
| web_fetch tool（含 html-to-markdown + SSRF 防护）| ~400 | 0 | ~300 | 0.5 天 |
| ask_user_question tool（含 state machine 改造 + runner.go yield 协议 + answer endpoint）| ~600 | ~250 | ~400 | 1.5 天 |
| file_read tool（PDF via qwen-long + 图 via 阿里 OCR + 文本直读）| ~400 | 0（复用现有 attachment 上传 UI）| ~250 | 0.5 天 |
| Tool registry 注册 + tool_definition seed migration | ~50 | 0 | ~50 | 0.25 天 |
| Playwright e2e（ask_user_question 完整 yield→answer→resume 流程）| 0 | ~150 | — | 0.5 天 |
| S0-S3 NDF 工件 + S4 双 reviewer | — | — | — | 0.75 天 |
| S5 本地验收 + S6 ndf-done + dev 部署 | — | — | — | 0.5 天 |

### 总估时

**~5 天**单人持续，含 NDF 全流程艺工件 + reviewer 修复。

### 报价

> N/A — 单人公司自研，无报价单。

### 交付时间线

S0 已完成 → S1（本文档） → S2 spec → S3 plan → S4 编码（含 reviewer）→ S5 本地验收 → S6 ndf-done → dev 部署。

按 agent-mode 14-feature 项目的 autopilot 规则，S0-S6 持续推进无停顿；dev 部署后停一次，由用户决定 prod 上线时机。

---

## §3 技术可行性 [AI 内部]

### 3.1 现有功能复用

| 复用模块 | 复用方式 | 节省工时 |
|---------|---------|---------|
| **`internal/numind/biz/agent/`** flat-file 工具模板（`tool_kb_search.go` 等 8 个）| 沿用 `BaseTool` 嵌入 + 5 必需方法重写模式 | ~30% |
| **`internal/numind/biz/agent/factory_platform.go`** `getAllBaseTools` 注册槽 | 新工具 append 到 tools + metadata 列表即可，AutoMigrate 走 tool_definition seed | ~20% |
| **`internal/numind/biz/agent/state.go`** TerminalReason + LoopEvent enum + Transition() | 加 2 个新枚举 case，runner.go 加 1 个 yield sentinel | 实现简化 |
| **`internal/numind/biz/attachment/UploadService`** + `POST /v1/agent-attachments` | file_read 直接读 attachment URL（COS 公开 URL or 本地 file），无需再建上传 endpoint | ~1 天 |
| **`internal/pkg/aiservice`** 统一入口 | file_read PDF parser 走 aiservice.Chat（qwen-long），web_search 包一层 aiservice wrapper 保证 Langfuse Span | ~30% |
| **`internal/pkg/httpclient`** 连接池 + 重试 | web_fetch 直接用，无需自建 | ~20% |
| **`internal/pkg/langfuse`** trace / span / generation API | 4 工具都走 langfuse.FromContext + CreateSpan/CreateGeneration | 现成 |
| **Tencent COS 上传体系**（`util.UploadBytesToCOS`）| file_read 读 attachment.URL → 走 COS 公开 URL（pre-signed if needed）| 现成 |

**关键发现**：S0 计划"如 file upload 基础设施不存在则拆 sub-feature"这条路径不需要 — attachment.UploadService 已存在（POST /v1/agent-attachments），允许 image/* + application/pdf + text/plain + text/markdown，文件存 Tencent COS。本 feature 直接复用，**不拆 sub-feature**。

### 3.2 技术风险与缓解

| # | 风险 | 缓解方案 |
|---|------|---------|
| R1 | **web_search provider 选型**——Tavily（最 LLM-friendly，含 snippet 摘要，每 1000 query $5）、Serper（基于 Google，质量稳定，$50/月）、Bing（合规友好，国内访问慢）、DuckDuckGo（免费但易被 ban）| S2 spec 必须独立 reviewer 审议选型理由 + 测试 latency / quota / cost 比较 |
| R2 | **ask_user_question state machine 改造**——首次"暂停 run 等用户"，旧 13 TerminalReason 都是真终态，第 14 个是 semi-terminal（loop 退出但 run 不结束）| state.go 加 `TerminalWaitingForUserChoice` 同 `IsTerminal()` 返 true，但 agent_run.state 用单独字段 `is_resumable=true` 区分；S5 写专项 unit test 验证状态机覆盖 |
| R3 | **BudgetTracker 在 yield-turn 的暂停语义**——若不暂停，"等用户答 30 秒"会被算成 agent 耗时（credit 多扣或时间预算超限）| state machine yield 时调 `BudgetTracker.Pause()`，user answer endpoint inject 后调 `Resume()`；S2 spec 必须画 BudgetTracker 时序图 |
| R4 | **Langfuse trace 链路连贯性**——yield-turn 导致 ReAct loop 分两段（pre-yield + post-resume），原 trace 可能被切断 | trace 在 run 开始时创建，run.langfuse_trace_id 持久化；user answer endpoint 取出 trace_id，在 resume 时延续同一 trace（CreateSpan("user_resume") 标记中断点）|
| R5 | **跨账户文件隔离**——子账户上传的 file 是否能被同父账户下其他子账户读？| 默认 user_id-strict：file_read input.file_id 校验 `file.user_id == ctx.user_id`，不复制蓝本"父账户可见所有子账户"语义；S2 若要放宽再讨论 |
| R6 | **web_fetch 的 SSRF**——恶意 user 发 `http://localhost:6379` 或 `http://169.254.169.254/latest/meta-data/` | 实现：(1) 解析 URL → DNS resolve → check IP 是否 private/loopback/link-local；(2) 拒绝非 http/https scheme；(3) 拒绝 .local TLD；(4) 整体 timeout 30s |
| R7 | **docx 解析**——`unioffice` 是重型库（~10MB binary inflation），且现有 attachment.UploadService 不允许 docx mime type | v1 **不做 docx 支持**；file_read 仅 PDF + image + text/markdown；docx 后续 sub-feature 单独评估 |
| R8 | **与 `agent-mode-configurator-relocate` (S0 并发) 文件交集**——后者改 web-v3 ConfigLayout/router/views | 本 feature 改 numind-web-v3 的 `views/agent/AgentChatView.vue` + 新增 `components/agent/QuestionPrompt.vue`，与 configurator-relocate 的 `views/config/agents/` 无交集；S3 plan 阶段确认 |
| R9 | **manifest.yaml 并发编辑冲突**——多 session 都改 manifest 顶部 features 列表 | 每个 session 在自己 worktree 编辑，merge 到 develop 时由 rebase 解决；冲突时手工选 ours（保持 features 列表合并）|

### 3.3 涉及仓库

- [x] **numind-server** — 4 个 tool 文件 + state.go + runner.go + answer endpoint + 配置 + migration + tool_definition seed
- [x] **numind-web-v3** — QuestionPrompt.vue 组件 + AgentChatView.vue 集成 + SSE handler + api/agent.ts 答案接口
- [ ] **numind-admin-web** — 0 改动（不需要 admin CRUD UI；tool_definition 已有 admin 管理界面）

### 3.4 AI 可观测性

- [x] 涉及 LLM 调用：**是**（file_read 的 PDF 解析走 qwen-long）
- **Trace 起点**：`biz/agent/runner.go::Run` 已有 CreateTrace（run.langfuse_trace_id 入库）
- **Generation 点**（仅 file_read PDF 解析）：
  - Generation name: `tool.file_read.qwen-long.parse`
  - Model: `qwen-long`
  - Input: { file_url, prompt }
  - Output: { content_md, page_count }
  - Usage: prompt_tokens + completion_tokens（从 dashscope response 拿）
- **Span 点**（非 LLM 但要可观测）：
  - `tool.web_search.execute` — 含 provider / query / results_count / cache_hit
  - `tool.web_fetch.execute` — 含 url / status_code / content_length / mime_type
  - `tool.ask_user_question.yield` — 含 question / options / multiSelect；run 进入 waiting_for_user_choice
  - `tool.ask_user_question.resume` — user answer endpoint 调用时；含 user_answer
  - `tool.file_read.execute` — 含 file_id / mime_type / byte_size / truncated
- **关键元数据**（trace level）：
  - user_id, agent_run_id, agent_definition_id（已有）
  - 本 feature 新增：tool_calls_count.web_search / web_fetch / ask_user_question / file_read（在 trace metadata 累加）

### 3.5 性能考量

- web_search: provider API 多 1-2s 延迟；in-memory TTL cache（5 min）减少重复 query
- web_fetch: HTTP fetch + HTML 解析常 1-3s；超时 30s 硬截
- file_read PDF via qwen-long: ~5s/page，长 PDF 会慢；超长截断到 200KB（约 60 页）
- ask_user_question: 服务器侧 <100ms，瓶颈在用户答题时间（与系统性能无关）

---

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 4.1 用户故事 + 验收标准

#### US-1: 学员让 agent 搜最新信息

**故事**：作为学员，我希望 agent 在我问时效性强的问题时主动搜索网络，并把搜到的内容自然融入回答。

**验收**：
- [ ] AC-1.1: 给 agent 发"最近教育部出的高考新政"——agent 工具调用历史显示 web_search 被调，query 含"教育部 高考 新政 2026"或同义
- [ ] AC-1.2: agent 最终回答包含至少 1 个来源 URL（与 search 结果之一一致）
- [ ] AC-1.3: 同一 query 5 分钟内再问，缓存命中（Langfuse Span cache_hit=true，未走 provider）
- [ ] AC-1.4: provider quota / 网络故障 → web_search 返回 error，agent narration 输出"网络搜索暂不可用，按我已知信息回答"，不 crash run

#### US-2: 学员发 URL 让 agent 读

**故事**：作为学员，我希望发给 agent 一个网页链接（教育部公告 / 知乎专栏 / 微博文章），agent 能读到内容。

**验收**：
- [ ] AC-2.1: 学员消息含 URL（"看这个 https://www.moe.gov.cn/xxx"）→ agent 调 web_fetch
- [ ] AC-2.2: agent 回答中提到该网页的具体内容（验证：用 e2e 模板页测试，固定 fixture URL）
- [ ] AC-2.3: 输入 `http://localhost:80`、`http://192.168.1.1`、`http://169.254.169.254/`、`file:///etc/passwd` → web_fetch 拒绝并返回 error，无任何 outbound 流量
- [ ] AC-2.4: 超时 30s 后返回 error，agent narration 输出"网页拉取超时"
- [ ] AC-2.5: 内容 > 100KB → 截断 + 返回 truncated=true，agent narration 提示"内容太长，已截断"

#### US-3: agent 遇到歧义反问学员

**故事**：作为学员，我希望 agent 不懂我说什么时礼貌问我，而不是瞎猜。

**验收**：
- [ ] AC-3.1: agent 收到歧义消息（如"我表姐想报国防"）→ 调 ask_user_question
- [ ] AC-3.2: 前端 chat 区域渲染 QuestionPrompt 组件，含 question 文本 + 2-4 个选项按钮 + 可选 free_text 输入
- [ ] AC-3.3: 学员点击选项后 → POST `/v1/agent/sessions/:run_id/answer` 成功 → agent 继续 run，narration 输出新内容
- [ ] AC-3.4: 学员点击页面其他地方 / 刷新页面 — agent run 保持 `state_reason=waiting_for_user_choice`，重新进入 chat 仍能看到 QuestionPrompt（持久化）
- [ ] AC-3.5: 学员关闭页面 → 10 分钟后回来 — QuestionPrompt 仍显示，仍可作答（无 TTL v1）
- [ ] AC-3.6: BudgetTracker 在 yield 时调 Pause()，resume 时调 Resume()；用户答题等待时间不计入 agent credit 耗时
- [ ] AC-3.7: 用户没答的 run 不能被 agent loop 继续触发（run.state_reason 守门）

#### US-4: 学员让 agent 读上传的文件

**故事**：作为学员，我希望上传 PDF / 图片 / 试卷后，agent 能"读"到完整内容，不只是 RAG 检索片段。

**验收**：
- [ ] AC-4.1: 学员通过 chat 附件按钮上传 PDF → POST `/v1/agent-attachments` 返回 attachment URL → URL 进入 agent run 的 attachment_urls 字段
- [ ] AC-4.2: agent 收到含 attachment_urls 的请求 → 调 file_read(file_url=...) → 返回 content + page_count
- [ ] AC-4.3: PDF 用 qwen-long 解析，每页 ~5s；total wait 在 e2e 时长内
- [ ] AC-4.4: 图片用阿里 OCR，返回提取的文本 + 检测到的物体（如可）
- [ ] AC-4.5: text/plain + text/markdown 直接返回内容（无 parser）
- [ ] AC-4.6: 不支持的 mime type（如 docx / xlsx）→ 返回 error，agent narration 提示"暂不支持该文件类型"
- [ ] AC-4.7: 文件 > 200KB → 截断 + 返回 truncated=true
- [ ] AC-4.8: 子账户 A 上传的文件，子账户 B（同父账户下）调 file_read(B 的 token + A 的 file_url) → 拒绝（user_id 不匹配）

#### US-5: 4 工具默认所有 agent 可见

**故事**：作为父账户，我希望新建 / 编辑 agent 时 4 个新工具自动出现在可用工具列表里，不用手动启用。

**验收**：
- [ ] AC-5.1: 4 个新工具在 `tool_definition` 表存在（migration seed 自动）
- [ ] AC-5.2: 父账户在 web-v3 `/config/agents/new` 见到 tool registry list 含这 4 个工具
- [ ] AC-5.3: 不勾选时新建 agent → agent_definition.allowed_tools 不含这 4 个 → run 时 LLM 看不到这 4 个 tool schema
- [ ] AC-5.4: 全勾选 → run 时 LLM 看到这 4 个 tool schema 在 tool_choices 里

#### US-6: 4 工具调用都有 Langfuse trace

**故事**：作为开发者 / 系统运维，我希望 4 工具每次调用都能在 Langfuse 看到。

**验收**：
- [ ] AC-6.1: 触发 web_search → Langfuse trace 含 Span `tool.web_search.execute`，metadata 含 provider / query / results_count
- [ ] AC-6.2: 触发 web_fetch → Span `tool.web_fetch.execute`，metadata 含 url / status_code
- [ ] AC-6.3: 触发 ask_user_question → 2 个 Span：`tool.ask_user_question.yield`（pause）+ `tool.ask_user_question.resume`（user 答完）
- [ ] AC-6.4: 触发 file_read PDF → Generation `tool.file_read.qwen-long.parse` 含 model / usage / I/O
- [ ] AC-6.5: 触发 file_read image → Span `tool.file_read.execute` 含 mime_type / byte_size

#### US-7: BudgetTracker 协同

**故事**：作为系统，我希望 ask_user_question 的 yield 不破坏现有 BudgetTracker。

**验收**：
- [ ] AC-7.1: run 已用 100 credits → ask_user_question yield → 用户答 30 秒 → resume → credits 不变（未在 wait 期间扣）
- [ ] AC-7.2: user wait timeout（v1 无 timeout）— 此 AC 不验，留作 P1 feature
- [ ] AC-7.3: budget exceeded 时 ask_user_question 不被 LLM 选（compliance + tool registry filter）

### 4.2 边界情况

- **空 query** (web_search) → 拒绝 + 返回 ErrInvalidInput
- **无 URL 协议** (web_fetch `foo.com`) → 自动补 https://; 失败时报错
- **0 选项** (ask_user_question `options=[]`) → 拒绝
- **>4 选项** → 拒绝（前端 UI 限定，后端二次校验）
- **multiSelect=true 但只 1 个选项** → 接受（degraded UX 但不 fail）
- **重复 file_id** → 缓存 5 分钟（in-memory）
- **同 run 内多次 ask_user_question** → 支持（每次 yield + resume 形成完整循环），不限次数
- **yield 后 user 永不答** → run 状态停在 waiting_for_user_choice；admin 可手动 cancel；后端无自动 cleanup（v1）
- **provider 限流（web_search 429）** → 退避 + 重试 3 次 + 失败时返回 fallback error，narration 友好提示

### 4.3 权限规则

- 4 工具均需 user_token（user 端 endpoint）
- file_read.file_url 必须属于当前 user（user_id-strict）
- ask_user_question 的 answer endpoint 校验 run_id 属于当前 user
- compliance audit：4 工具都默认进 compliance gate 的 hook chain（compliancegate.WrapHooks）
- 父账户为子账户配 agent 时勾选哪些工具可见——遵循现有 allowed_tools 机制，本 feature 不破坏

### 4.4 UI 行为规格 [仅 ask_user_question + 可能的 file_read 上传]

#### QuestionPrompt.vue（新组件）

- **页面位置**：渲染在 AgentChatView 的消息流末尾，作为一个特殊 message bubble
- **布局**：question 文本 14pt + 选项按钮（2-4 个，垂直堆叠 or 横排 4 列自适应）+ 可选 free_text textarea（multiSelect 时）+ "提交"按钮
- **交互**：
  - 单选模式：点击选项立即 submit
  - 多选模式：复选框 + 必须点"提交"
  - 提交后按钮 disabled，状态进入 loading
  - 后端 200 → emit `answer-submitted`，chat 继续渲染 agent narration
  - 后端 4xx/5xx → 显示 error toast，可重试
- **状态处理**：
  - loading（提交中）→ 按钮 disabled + spinner
  - empty / error / success — 此组件无 empty 态；error → toast
- **可访问性**：键盘 Tab + Enter 选项；ARIA label

#### AgentChatView.vue（改造）

- 监听 SSE event `tool_call_yield` → 渲染 QuestionPrompt
- run.state_reason = `waiting_for_user_choice` → 输入框 disabled，提示"agent 等你回答"
- run.state_reason = `running` → 输入框 enabled

#### AttachmentUploader（已存在，sanity-check）

- AttachmentUploader 已有（agent-mode-student-ux #11 落地），本 feature 不改

---

## §5 上下文 / 关联

- 本 feature 是 agent-mode 14-feature 完成后的**基线扩展**
- 蓝本 `docs/agent-mode/architecture-v1.md`（本地草稿）§4.2.1 ToolRegistry 设计未明确这 4 工具，但 §4.2.4 "Future tools" 列了相关方向
- 与并发 feature 关系：
  - `agent-mode-configurator-relocate`（S0 并发）— 无文件交集
  - `agent-mode-compliance-3layer`（S5 ACCEPTED）— 提供 compliance gate；本 feature 4 工具自动走同 gate
- 本 feature 不更新 architecture-v1.md（本地草稿，非 git）；S6 完成后将工具清单更新到 8→12（如该文件后续入 git）

---

## §6 决策点（提案级）

| # | 决策 | 选项 | 建议 |
|---|------|------|------|
| D1 | web_search provider | Tavily / Serper / Bing / DuckDuckGo | **Tavily**——LLM-friendly snippet 摘要 + 国际访问 OK + 价格透明 $5/1000 query；S2 spec 锁定 |
| D2 | file_read docx | 支持 / 不支持 / 后续 | **不支持 v1**——unioffice 重型库 + attachment 不允许 docx；docx 留 sub-feature |
| D3 | yield-resume timeout | 5 分钟 / 1 小时 / 无限 | **无限 v1**——简化逻辑；watchdog 留 P1 |
| D4 | 跨子账户文件可见 | parent 范围 / user 严格 | **user 严格 v1**——避免父账户视角 LLM 误读子账户隐私文件 |
| D5 | web_search cache 介质 | in-memory / Redis / DB | **in-memory v1**——避免新依赖；后续可平迁 Redis |
| D6 | 工具默认勾选 | 默认勾 / 默认不勾 | **默认勾 4 个**——降低父账户配置成本，"开箱即用"语义 |
| D7 | ask_user_question 是否计 credit | 计 / 不计 | **不计 v1**——yield 仅记数据库行变动 + 推送 SSE，无 LLM 调用 |

---

## §7 验收门禁（用户确认进 S2）

- [x] **S0 → S1 已完成**：requirement card commit e02cdf95 on develop（自我推进，无停顿per agent-mode autopilot）
- [ ] **S1 → S2 gate**：spec 阶段 brainstorming 启动；客户隐性确认（per autopilot 规则，不阻断进入 S2）；提案 §6 中 D1-D7 在 S2 spec 中逐项闭环

---

*S1 完成。进入 S2：技术 spec — 完整 API 契约 + state machine yield 协议 + provider 选型理由 + trace topology + DB schema 变更。*
