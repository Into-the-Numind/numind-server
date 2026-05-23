# 有数 Agent Mode V1.5 共享上下文

> 所有 task spec subagent 必须先读这个文件，再开始写自己负责的 task。

## 1. 有数项目背景

- **产品名**：**有数**（Numind）—— AI 工作台产品。**注意：莫小派是有数的一个客户，不是产品名本身。**
- **核心能力**：Agent Mode（多模型可切换 ReAct agent）+ SOP 工作流引擎 + 销售知识库 SalesRAG（**SalesRAG 是 agent mode 的一种应用，不是全部**）+ 监控分析 + OCR / ASR 等
- **业务模式**：B2B2C（父账户帮子账户开通会员）
- **场景多元**：销售助理、SOP 执行（如制造业质检流程）、数据分析、监控值班、PPT 制作、知识检索等 — **不要假定 agent mode 用户都是销售员**。spec 里举例时应当用多元场景，销售场景只是其中一种

## 2. 三仓库技术栈

| 仓库 | 语言 | 框架 | 路径 |
|---|---|---|---|
| `numind-server` | Go 1.24 | Gin + GORM + MySQL 8.0 + Redis + JWT + Viper + Zap | 后端 API |
| `numind-web-v3` | TypeScript | Vue 3.4 + Vite 5 + Pinia 2 + Vue Router 4 | 用户端前端 |
| `numind-admin-web` | TypeScript | Vue 3 + 类似 stack | 管理端前端 |

## 3. 现有 aiservice 架构（已上线）

**所有 AI 调用统一入口** `aiservice` package：
- `aiservice.Chat(ctx, profile, req)` / `aiservice.ChatStream(...)`
- `aiservice.Embed(...)`, `aiservice.Rerank(...)`, `aiservice.OCR(...)`, `aiservice.ASR(...)`

**已接入 provider**（DB Registry 管理）：
- 阿里 DashScope: qwen-turbo, qwen-plus, qwen-long, qwen3-vl-flash, text-embedding-v4
- 火山引擎 Ark: deepseek-v3-2, glm-4-7, doubao 系列
- DMXAPI: deepseek-v3-2, qwen-turbo-latest, qwen3-rerank
- 百度: OCR
- 百炼 Bailian: 文件上传 / 管理

**Task Profile**（21 个 task ID）：
- `sop.text` / `sop.vision` / `chatbot.stream`
- `salesrag.intent` / `salesrag.chat` / `salesrag.rerank` / `salesrag.embed` / `salesrag.tagging` / `salesrag.profile` / `salesrag.chatstyle`
- `monitor.briefing` / `monitor.analyze` / `monitor.transcribe`
- `ocr.baidu`
- `agent.run` / `agent.embed` / `agent.sync_turn` / `agent.compact` / `agent.narration_fallback` / `agent.injection_check` / `agent.permission_check`

**DB Registry 表**（修改 capability 改这里）：
- `ai_service`: id / model_key / display_name / service_type / capability_json / is_thinking / supports_thinking ...
- `ai_service_route`: model_id / provider_id / priority / is_active / pricing_unit ...

**关键 invariants（不要破坏）**：
- aiservice 是 LLM 调用**唯一入口**（禁止业务代码直接 `import` 任何 provider 包或裸 HTTP 调 LLM）
- 21 个 task profile 是稳定的，不要轻易加（如果要加必须更新 `constants.go::allTaskIDsList`）

## 4. Agent Mode V1.0 现状（已上线）

Agent Mode 已经跑起来了，核心组件：

- `internal/numind/biz/agent/runner.go` - AgentRunner + Eino integration
- `internal/numind/biz/agent/adapter.go` - aiserviceAdapter (Eino model.ToolCallingChatModel)
- `internal/numind/biz/agent/callctx/` - 每次 chat 调用的 callID ctx 注入
- `internal/numind/biz/agent/budgetgate/` - BudgetTracker hook
- `internal/numind/biz/agent/compliancegate/` - L0/L1 compliance hook
- `internal/numind/biz/agent/bashvalidator/` - Bash 安全检查器

**Hook chain 顺序（固定，外→内）**：compliance → permission → budget → sandbox → narration

**State machine**：
- 19 个 TerminalReason（completed / model_error / context_exhausted / image_error / waiting_for_user_choice / refusal / ...）
- 19 个 LoopEvent（next_turn / collapse_drain_retry / reactive_compact_retry / max_output_tokens_escalate / token_budget_continuation / LLMErrPTL / ...）
- 5 个 HookAction（continue / block / yield / cancel_run / cancel_tool_call）

**System prompt 6 段顺序（I3 - 固定）**：(1) Persona (2) Tools (3) Memories (4) User context (5) System reminders (6) Custom appends

**Eino 框架**（v0.8.13）：
- 用 `react.NewAgent` + `compose.ToolsNodeConfig`
- `MaxStep: 30`（最大 ReAct 步数）
- 当前 tool error 处理是 fatal NodeRunError（这是个限制，方案 B+ 需要 workaround）

## 5. 当前 agent run 涉及的关键表

```sql
-- agent_run
id BIGINT PRIMARY KEY
user_id BIGINT
session_id VARCHAR(64)              -- UUID 字符串
agent_definition_id BIGINT
status VARCHAR(32)                  -- running / terminated / pending
messages JSON                       -- [{role, content, tool_call_id?, tool_calls?, reasoning_content?}]
state_reason VARCHAR(64)
started_at / ended_at / created_at / updated_at

-- agent_attachment（已存在）
id BIGINT PRIMARY KEY
user_id BIGINT
url TEXT                            -- COS 公开 URL
filename / mime_type / size BIGINT
created_at

-- agent_definition
id BIGINT
parent_user_id BIGINT
generated_skill_body / custom_skill_body
tool_flags JSON
advanced_mode BOOL
credit_cap_per_session INT
is_active BOOL
```

## 6. NDF 流程（必须遵守）

每个 task spec 必须覆盖这些阶段：

- **S0**: requirement card（已在板块 README 里）
- **S1**: PRD + proposal（产品需求 + 实现提案）
- **S2**: detailed spec（详细规格 - schema / API contract / 算法 / 文件改动）
- **S3**: task plan（拆分到具体可执行的子任务 + 工期）
- **S5**: 验证策略（怎么测 - playwright / unit / gstack /qa）

**Bug-from-customer 规则**：第一个 commit 必须是失败的复现测试。但本项目是新功能开发，不适用。

**Hotfix vs Standard**：本项目全部走 Standard track（涉及 DB schema 变更 / 多 task 协作）。

## 7. 硬约束 - 这些绝对不要破坏

- ❌ 不要修改 `config_prod.yaml`
- ❌ 不要 SSH 到 prod 机器
- ❌ 不要打 `v*` git tag（那会触发 prod 部署）
- ❌ 不要在代码中硬编码 API 密钥 / 数据库密码
- ❌ 不要在 controller 层写业务逻辑（biz 层职责）
- ❌ 不要打破 aiservice 唯一入口（不要 import provider 包）
- ❌ 不要绕过 hook chain
- ❌ 不要新增 TerminalReason / LoopEvent / HookAction（19+19+5 是固定的）
- ❌ 不要破坏 system prompt 6 段顺序
- ❌ 不要破坏 21 个 task profile 列表（动态加 task profile 在 DB Registry 里做）

## 8. 当前要实施的方案 — 方案 B+

方案 B（均衡派）+ 5 个 Hermes/OpenHuman 加法。三大板块：

### 板块 1：多模态 fallback（Multimodal Track）
让 agent 在切换不同 modality 模型时（GLM 5.1 / MiniMax M2.7 / Qwen 3.7 Max 等单模态 + qwen-vl 等多模态）能正确处理用户上传的图片 / PDF。

参考开源：WeKnora 三层兜底 + DeerFlow Tool Gating

### 板块 2：上下文管理（Context Management Track）
让 agent 长会话 + 多轮 ReAct + 大 tool result 不爆 context。

参考开源：ClaudeCode 4 层 compact + ClaudeCode tool result 写盘 + Hermes 12 段固定模板 + OpenCode 双轨 prune + 加 Streaming Scrubber

### 板块 3：记忆管理（Memory Management Track）
让 agent **跨会话记住"使用者本人"** 的偏好 / 画像 / 历史关键事实，给个性化体验。

**重要范围界定 — V1.5 只做 Layer A，不做 Layer B**：

- **Layer A（使用者本人画像，本 V1.5 实施）**：对**真正使用 agent 的 user**（销售员、文员、分析师、操作员等）建画像。dialectic 输出是"该使用者是谁 + 该怎么个人化对待"。
- **Layer B（使用者关注对象的画像，V1.5 不做，V2 扩展）**：对**会话里讨论 / 处理的客观对象**（销售客户、PPT 观众、数据集、产线等）建画像。dialectic 输出是"这次讨论的对象是谁 + 该怎么对待这个对象"。

**V1.5 schema 设计要预留 Layer B 扩展点**：在 `user_memory_facts` 表加 `subject_id VARCHAR(64) NULLABLE` 字段（**V1.5 全部为 NULL**，V2 启用此字段）。

参考开源：ClaudeCode CLAUDE.md cascade（只激活 2 层）+ DeerFlow memory.json + 加 dialectic 推理（对**使用者本人**）+ FTS5 中文搜索 + trivial 短路 + 分层时间感知

## 9. 输出 spec 的格式要求（所有 subagent 遵守）

每个 task spec markdown 文件包含以下 sections（**严格用这些标题**）：

```markdown
# Task X.Y: [任务名]

## 概要
(50-100 字)

## 依赖
- 前置依赖：[哪些 task 必须先完成]
- 被依赖：[哪些 task 等这个完成]

## 输入 / 输出契约
- 函数签名 / DB schema / API endpoint / 配置项
- 用 Go / SQL / TypeScript 代码块表达

## 设计要点
- 核心算法
- 关键参数（常量值 / 阈值）
- 边界 case 处理

## 实施步骤
- 按"文件级别"分解
- 每个 step 标"哪个仓库 / 哪个文件 / 改什么"
- 顺序步骤（不是并行）

## 验证策略（S5）
- 单元测试用 case 列表
- 集成测试方法
- 手动 dev 验证步骤
- 可选：gstack /qa 浏览器验证场景

## 工期估算
- 总工期：X 天
- 分项工期（DB migration / 后端 / 前端 / 测试）

## 风险 / 待决策项
- 列出实施中可能遇到的不确定 / 需要找用户拍板的设计选择
```

## 10. Provider 列表（要支持的）

要在同一会话切换的 5 个新模型：
- MiMo V2.5 Pro（小米，**多模态**）
- Kimi K2.5 / K2.6（月之暗面，**多模态**）
- GLM 5.1（智谱，**单模态文本 only**）
- MiniMax M2.7（**单模态文本 only**）
- Qwen 3.7 Max（**单模态文本 only**）

加上已接入的：qwen-vl / qwen-turbo / qwen-plus / qwen-long / deepseek-v3 / glm-4-7 / doubao 等。

## 11. 9 个关键设计决策（**已由产品 owner 拍板，不要再讨论**）

| # | 决策 | 实施约束 |
|---|---|---|
| D1 | 新加 `profile.attachment.vision_describe` | task 1.2 用此新 profile |
| D2 | **要做放大镜**：实现 `analyze_image` / `annotate_image` vision 工具 | task 1.4 必须实现，不是 metadata 占位 |
| D3 | **平行重做**：新建 `internal/numind/biz/compactv2/` 包，**V1 `compact/` 完全保留不动** | 板块 2 全部 task 写新包；agent mode 通过 feature flag 走 V2；其他场景（SOP/SalesRAG/监控）继续 V1；DB 新增 `compact_state_v2` 字段不动现有 `compact_state` |
| D4 | dialectic / autocompact 等用 `qwen-plus` 或 `deepseek-v3-2`（**不要 thinking model**） | profile 默认配置 |
| D5 | autocompact 摘要用 `<reference-only data-internal="true">...</reference-only>` XML 包裹 | task 2.4 实施 |
| D6 | AGENT.md cascade **只激活 2 层**：(1) `/etc/numind/AGENT.md` 部署级 (2) `<user_data>/users/<user_id>/AGENT.md` 用户全局 | task 3.1 简化，路径 3-6 不实施（spec 里只留扩展接口） |
| D7 | B2B2C 父子账户 memory **完全隔离**（per user_id 独立） | 全板块 3 schema 设计 |
| D8 | 25 个 task profile（原 21 + 新增 22-25） | 4 个新 profile 加进 `constants.go::allTaskIDsList` |
| D9 | 中文搜索先用 MySQL 8 FULLTEXT + ngram parser，量大再升 ES | task 3.5 |

## 12. Task Profile 完整列表（V1.5 = 25 个）

**已有 21 个**（在 `internal/pkg/aiservice/profile/constants.go`）：

| Profile ID | 场景 |
|---|---|
| `sop.text` | SOP 文本执行（LLM + 工具调用）|
| `sop.vision` | SOP 视觉执行（带图片输入）|
| `chatbot.stream` | 实时聊天 streaming |
| `salesrag.intent` | SalesRAG 意图分类 |
| `salesrag.chat` | SalesRAG 对话答案 |
| `salesrag.rerank` | SalesRAG 文档重排序 |
| `salesrag.embed` | SalesRAG embedding（2048d）|
| `salesrag.tagging` | SalesRAG 实体标签 |
| `salesrag.profile` | SalesRAG 客户画像（带 vision）|
| `salesrag.chatstyle` | SalesRAG 风格分析（带 vision）|
| `monitor.briefing` | 监控日报 |
| `monitor.analyze` | 监控数据分析 |
| `monitor.transcribe` | 监控音频转录（ASR）|
| `ocr.baidu` | 百度 OCR |
| `agent.run` | Agent ReAct 主调用 |
| `agent.embed` | Agent memory L1/L2 检索 |
| `agent.sync_turn` | Agent 会话轮次摘要 |
| `agent.compact` | Agent V1 上下文压缩（**保留不动，V2 用新 profile**）|
| `agent.narration_fallback` | narration 动态生成 |
| `agent.injection_check` | compliance 注入检查 |
| `agent.permission_check` | 权限 L3 auto-mode |

**V1.5 新增 4 个**：

| Profile ID | 场景 | 适用板块 |
|---|---|---|
| `attachment.vision_describe` | 上传时跑 VLM 生成图片画面描述 | 板块 1 task 1.2 |
| `agent.memory_extract` | 从对话异步抽取 user facts（Layer A）| 板块 3 task 3.3 |
| `agent.memory_select` | 选 top-5 最相关 fact 注入 prompt | 板块 3 task 3.4 |
| `agent.dialectic` | 基于 facts 推理使用者画像 + 个人化建议（**Layer A**，V1.5 范围）| 板块 3 task 3.7 |
| `agent.digest` | 日/周/月/季 digest 生成 | 板块 3 task 3.8 |

**注意**：上面表列出 5 个新增（含 `attachment.vision_describe`），合计 21 + 5 = **26**。如果把 `agent.dialectic` 和未来 V2 `agent.dialectic.subject` 合并算"dialectic 家族"，V1.5 实际新增 5 个 profile，列表总数 26。**最终敲定：V1.5 后 profile 总数 = 26**。

## 13. Agent Mode V2（Layer B）扩展预留 — 仅 schema 层面，不实施

V1.5 不实施但要 schema 兼容：

- `user_memory_facts.subject_id VARCHAR(64) NULLABLE` — V1.5 全 NULL
- 未来 V2 启用：subject 可以是客户 ID / 数据集 ID / 文档 ID / 产线 ID 等业务实体 ID
- V2 会新增 `agent.dialectic.subject` profile（对会话关注对象的画像）
- V2 会新增 `subject_card` 表（per-subject 画像 cache）

**V1.5 dialectic profile 描述统一**：「基于该使用者的 facts，推理出'该使用者是谁 + 应当怎么个人化对待该使用者'。**画像对象是使用 agent 的真实 user，不是 user 关注的客户/对象**」。
