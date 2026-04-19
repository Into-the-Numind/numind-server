# AI 服务统一化管理（AI Service Manager）— 提案

> 本提案基于 S0 requirement card（`numind-server/requirements/ai-service-manager.md`）+ 两份 inventory（AI 调用穷举 + llm-model-switch 产出盘点）编写。

---

## §1 方案概述 [客户可见]

### 要解决的问题

莫小派当前的 AI 能力（大模型对话、向量嵌入、重排、图像识别、OCR、语音识别）分散在 6 个独立封装层，各层质量参差不齐：

- 部分调用**无法观测**（火山引擎的流式聊天、百度 OCR、语音识别都没有追踪数据）
- 部分调用**无法计费**（OCR 和语音识别没进账单）
- 新增模型需要**改代码 + 发版**（运营无法自助上下架模型）
- 之前做的"用户选模型"功能（`llm-model-switch`）已落地一半基础设施，但只覆盖 SOP 和 ChatBot，SalesRAG 等业务还在走老路

### 解决方案

建立**统一的 AI 服务管理中心**：

1. **一个入口**：所有业务代码调 AI 服务都走同一套接口，不再各自连阿里云/火山/百度
2. **一张表**：在管理后台可以看到所有 AI 服务（包括模型、OCR、语音识别），可以新增、下架、改价格，无需发版
3. **一种配置**：每个业务场景（如"SOP 执行"、"销售问答"）声明自己需要什么能力，绑定到合适的 AI 服务
4. **全透明**：所有 AI 调用自动被追踪（进 Langfuse）、自动计费（进账单），错误有完整日志
5. **自动容灾**：主服务挂了自动切到备用服务，不影响业务

### 预期效果

- 新增一个模型从"改代码 + 发版 + 等发布"（半天以上）降到"管理端点几下"（5 分钟以内，前提是同一家厂商）
- 所有 AI 调用 100% 可追踪、可计费、有错误日志
- 管理员绑定模型到任务时，系统自动校验能力匹配（防止把"纯文本模型"绑给"图像任务"这类错误）
- 为未来的成本优化（语义缓存、智能路由）打好地基

### 本期不做的事（明确边界）

- **C 端用户的模型选择界面不改**：现有的前端下拉选择器继续用
- **不做语义缓存、智能成本路由**：留到下期
- **不接入新的 AI 服务商**：只收编现有的 5 个（阿里云、火山、DMXAPI、百度、FunASR 本地）

---

## §2 报价与周期 [客户可见]

- **预估工作量**：**正常 11 ~ 12 天**（含 1 天意外 buffer）；**最坏 14 天**（若下述风险同时触发）
- **报价**：内部功能，无外部报价
- **交付时间线（按阶段）**：
  - S1 客户确认：0.5 ~ 1 天（当前阶段）
  - S2 技术设计：1 ~ 1.5 天
  - S3 任务规划：0.5 天
  - S4 编码：5 ~ 7 天（Gateway + 5 个服务商适配 + 数据表 + 管理端 + 迁移 17 个老调用点）
  - S5 验收：1 ~ 1.5 天
  - S6 部署：0.5 天
  - S7 收尾：0.5 天
  - 小计：9 ~ 12 天
  - Buffer：1 天（可消化小意外，如某 provider 流式格式差异）

**横向对照**：前置功能 `llm-model-switch`（单一 DMXAPI 路由 + SOP/ChatBot 选型）约 7 天做到 S6；本功能范围约其 2 倍（5 provider 并存 + OCR/ASR 新增 + 17 个调用点迁移 + 管理端从零），11-12 天在量级上合理。

**风险追加缓冲**：若火山流式 token 采集兜底策略复杂、或 llm-model-switch schema 改名/VIEW 过渡在 dev 压测暴露问题，总工时再 +2 ~ 3 天 → 最坏 14 天。

---

## §3 技术可行性 [AI 内部]

### 现有功能复用（**好消息：基础设施已就位 75%**）

根据 `llm-model-switch` inventory，以下已可直接复用或演进：

| 已就位 | 复用方式 |
|---|---|
| `llm_provider` 表（供应商凭据 DB 化） | 扩展：加 `provider_type` 字段（llm/ocr/asr），容纳非 LLM 服务商 |
| `llm_model` 表（模型定义） | 扩展为 `ai_service`（改名）+ 加 capability 字段 + 加 service_type |
| `llm_model_provider` 表（模型×供应商路由） | 复用，改名 `ai_service_route`；保留 pricing 字段 |
| `user_model_preference` 表 | 保留不动（C 端选择仍用它） |
| `internal/pkg/llm/dmxapi_client.go` | 复用 |
| `internal/numind/biz/llmrouter/` 路由+failover 逻辑 | 扩展为 `aiservice` 包，支持多 service_type |
| 15 个 REST API（3 用户 + 12 管理端） | 保留现有契约 + 新增 Task Profile / OCR / ASR 相关端点 |
| `numind-web-v3` ModelSelector.vue | **不动** |
| `numind-admin-web/src/api/llm.ts`（API 客户端已定义）| 复用，扩展新端点 |

### 现状问题需解决

- `llm_provider` 表和 `llm_model` 表里**没有 capability 字段**，需要加 `capability_json` 列
- **没有 Task Profile 表**（任务 → 允许模型的绑定），需要新建
- **没有 OCR/ASR 相关表或接口**，需要拓展
- SalesRAG（7 个调用点）、Ali/Volc 直连代码（火山流式/视觉）**都没走 llmRouter**，迁移工作量最大
- 管理端 UI **还没有页面**（API 已就绪），需要从零做

### 技术风险

| 风险 | 缓解 |
|---|---|
| **OCR/ASR 抽象差异**（无 token 语义、无 prompt、输入是图/音频不是文本） | 在 `ai_service` 表用 `service_type` 枚举字段（llm/ocr/asr）区分；不同类型对应不同 capability 字段子集；业务层用多 method 入口（`ai.Chat / ai.OCR / ai.ASR`，详见 §5.7） |
| **火山流式 token 采集丢失** | S2 定策略：有 `usage` 字段就用；客户端中断走估算兜底（按字符长度 × 系数）；后台异步对账 |
| **迁移期间双路径双记账** | 迁移期 `UsageRecord` 只在 Gateway 中间件写入；老封装层 billing 调用关闭；每模块单独灰度 |
| **pricing 单位混乱**（LLM 按 token/百万，OCR 按次，ASR 按秒） | UsageRecord 加 `unit` 字段 + 3 个 nullable 计量字段（tokens_input/output、call_count、duration_seconds），按 service_type 选择使用 |
| **Langfuse 故障阻塞主请求** | Langfuse SDK 调用必须超时 + 异步 flush；失败降级为无追踪，主流程继续 |
| **llm-model-switch schema 演进风险** | migration 使用 `ALTER TABLE ADD COLUMN`（不重建）；改名通过 VIEW 兼容过渡；所有现有用户端/管理端 API shape 完全保留 |
| **双层 retry 叠加** | 迁移时关闭 adapter 层 retry，仅保留 Gateway 单一 retry 源 |
| **fallback 级联失控** | 硬规则：最多 1 次 fallback 跳转（主→备，总共 2 次 upstream 调用）；fallback 目标必须在 `allowed_service_ids` 白名单 |
| **Provider rate limit** | 本期**不做**专用限流中间件；依赖 provider 返回 429 → Gateway retry 中间件重试 1 次 → 仍失败触发 fallback。S5 压测若暴露需求，Phase 2 加专用限流/令牌桶 |

### 涉及仓库

- [x] **numind-server**（Gateway 扩展、schema migration、迁移 17 个调用点、新增 API）
- [ ] numind-web-v3（**不动**）
- [x] **numind-admin-web**（新增 4 个管理页：服务列表/服务编辑/任务列表/任务编辑）

### AI 可观测性

- [x] 涉及 LLM 调用：**是**（本身就是重构所有 AI 调用）
- **Trace 起点**：每个 Task Profile 被调用时，Gateway 入口（`ai.Chat` / `ai.OCR` 等）创建 trace
- **Generation 点**：
  - LLM chat/vision：`CreateGeneration` 记录 model + input + output + token usage
  - Embedding：`CreateGeneration` 记录 model + input 长度 + dimension
  - Rerank：`CreateSpan` 记录 query + doc 数 + topN（无 token 概念）
  - OCR：`CreateSpan` 记录 image 尺寸 + 识别文字长度
  - ASR：`CreateSpan` 记录 音频时长 + 识别文字长度
- **关键元数据**：`task_id`（profile id）、`service_id`、`user_id`、`feature_ref`（如 sop_id / chatbot_id / kb_id）

---

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

#### 管理员（新功能）
- 作为**管理员**，我需要**在管理端新增/下架 AI 服务**，以便无需发版就能引入新模型或下线老模型
- 作为**管理员**，我需要**编辑服务的能力字段**（是否支持多模态、上下文长度、pricing 等），以便系统准确路由
- 作为**管理员**，我需要**为每个业务任务（如 SOP 步骤执行）绑定默认模型 + 备用模型 + 允许用户选择的模型集合**，以便业务按配置执行
- 作为**管理员**，我在绑定模型到任务时，**如果模型能力不满足任务需求**（如把纯文本模型绑给多模态任务），**系统应拒绝保存并提示具体原因**
- 作为**管理员**，我需要**看到每个 AI 服务的使用量、费用和错误率**（可点到 Langfuse 详情）
- 作为**管理员**，我需要**修改 pricing 不影响历史账单**（历史 UsageRecord 保留当时的 pricing 快照）

#### 开发者（隐式契约）
- 作为**业务模块开发者**，我需要**只通过 Gateway 调用 AI**，不应该直接 `import` ali / volc / baidu / dmxapi 包
- 作为**业务开发者**，我需要**一个清晰的入口**：`ai.Chat(ctx, "sop.step", req)` / `ai.OCR(ctx, "monitor.ocr", req)`，Gateway 自动选模型、自动追踪、自动计费
- 作为**业务开发者**，我需要**fallback 对业务透明**（主模型挂了我不需要处理）

#### 运营（衍生价值）
- 作为**运营**，我需要**按任务维度看成本**（单次 SOP 运行花了多少钱、SalesRAG 单条问答成本）

### 验收标准

**功能完整性**
- [ ] 所有 17 个 AI 调用点（见 §5.1 Task Profile 清单）都通过 Gateway 调用
- [ ] 静态扫描：`grep -rn "http.Post\|http.NewRequest" internal/numind/biz internal/service | grep -v _test.go` 无 AI 相关裸调用
- [ ] 静态扫描：`internal/numind/biz/` 下无 `import` `biz/ali` / `biz/volc` / `biz/baidu` / `biz/salesrag/adapter/dmxapi_client`（Gateway 自身除外）
- [ ] 所有 AI 调用在 Langfuse 可见：S5 跑完关键路径，Langfuse count(observations) ≥ count(UsageRecord)
- [ ] 所有 AI 调用的失败有 generation/span error 记录
- [ ] Token usage（LLM）/ 调用次数（OCR）/ 音频时长（ASR）正确进入 UsageRecord

**管理端功能**
- [ ] 4 个页面可用：服务列表、服务编辑、任务列表、任务编辑
- [ ] 新增服务 → 保存后立即生效（不重启服务）
- [ ] 下架服务（软删）→ 正在使用该服务的任务会告警或自动切 fallback
- [ ] 绑定能力不匹配时保存被拒 + 显示具体原因（手测通过）
- [ ] pricing 修改不影响历史 UsageRecord（对比前后一条数据验证）
- [ ] 关键操作（删除、改 pricing）有二次确认

**容灾**
- [ ] 主服务挂了自动切 fallback（模拟 provider 返回 5xx 验证）
- [ ] Langfuse 挂了主流程不中断（手测或单元测试）
- [ ] Fallback 最多 1 次跳转（代码 assert + 单测）

**兼容性**
- [ ] 现有 `/v1/llm/models` / `/v1/llm/preference` 等 15 个端点 shape 保持不变
- [ ] 现有 `ModelSelector.vue` 无需改动即可工作
- [ ] SOP / ChatBot / SalesRAG 全部业务功能回归测试通过

**测试覆盖**
- [ ] Gateway 中间件（tracing/billing/retry/fallback）每类至少 1 个 unit test
- [ ] **billing 中间件必须覆盖 LLM / OCR / ASR 三种 service_type 各 1 个 test**，验证 pricing snapshot 写入正确 + UsageRecord 字段按 unit 正确归类（token / call / second）
- [ ] 每个 provider adapter 至少 1 个 roundtrip test（mock httpclient）
- [ ] Capability matching 逻辑有单测覆盖（兼容/不兼容各 1 条）
- [ ] Fallback 最多 1 次跳转的 assert 有单测（强制主失败 → 验证调用 2 次 upstream 后终止）

### 边界情况

- **Task Profile 存在但 default_service 被下架**：调用时自动用 fallback；若 fallback 也被下架返回明确错误
- **同一任务并发调用，默认 service 刚好挂**：所有请求都走 fallback，fallback 也跟着被打爆 → 通过 rate limit + retry backoff 缓解（S2 定）
- **客户端中途断开流式响应**：token usage 数据丢失 → 用字符数估算兜底写 UsageRecord（记录 is_estimated=true）
- **管理员删除正在用的服务**：软删除而非硬删；依赖该服务的 Task Profile 标记警告；实际调用走 fallback
- **新模型能力字段不全**（管理员填漏了）：默认保守（supports=false），Capability Matching 会把它排除出候选
- **pricing 为 0 或 null 的服务被调用**：允许（内部测试服务），UsageRecord 记 0 成本
- **ASR 音频超长（> 1 小时）**：S2 定策略，本期可拒绝超长音频

### 权限规则

- **C 端用户**：只能调用 Gateway（通过业务层间接），不能直接访问 `/v1/admin/ai/*`
- **管理员**：所有 `/v1/admin/ai/*` 端点（扩展自现有 `/v1/admin/llm/*`）
- **超级管理员**：额外能力 — override capability matching（强制保存不兼容绑定，需二次确认）
- **软删除的服务**：普通管理员不可见，超级管理员可恢复

### UI 行为规格（仅管理端 numind-admin-web）

**页面 1：AI 服务列表**
- 位置：管理端侧边栏新增"AI 服务管理 > 服务列表"
- 布局：**DataTable**（非卡片 — 遵守 `.claude/rules/ui-ux.md §硬规则 1`）
- 列：服务 ID、名称、类型（llm/ocr/asr）、供应商、pricing、状态（active/deprecated）、操作
- 筛选：按 service_type、provider、status
- 异步状态：loading skeleton / empty（引导去新增）/ error + retry

**页面 2：AI 服务编辑**
- 进入：列表页"新增"或"编辑"按钮
- 表单 + 能力字段组：
  - 基础信息：id / display_name / provider（下拉，关联 llm_provider 表）/ family
  - 类型相关字段（动态显示）：
    - 若 service_type=llm：input/output modalities（多选）、context_window、max_output_tokens、features（tool_use / json_mode / streaming / vision 等 checkbox）
    - 若 service_type=ocr：支持的图像格式、分辨率上限
    - 若 service_type=asr：支持的音频格式、最大时长、支持语言
  - Pricing：unit 选择（per 1M tokens / per call / per second）+ input/output 单价（LLM only）或单次价格
  - 运营：latency_tier / quality_tier / tags（多选）/ status
- 验证：**blur 时触发**（遵守 `.claude/rules/ui-ux.md §硬规则 3`）
- 保存：后端写入 + capability matching 校验（若此服务已被任务绑定，新能力集合需继续满足现有绑定）

**页面 3：Task Profile 列表**
- 位置：侧边栏"AI 服务管理 > 任务列表"
- 布局：**DataTable**
- 列：task_id、display_name、service_type、default_service、fallback（列出数量）、allowed（列出数量）、操作

**页面 4：Task Profile 编辑**
- 表单：
  - task_id（只读，后端枚举）
  - display_name、description
  - requirements（能力需求）：同服务编辑的能力字段结构，但是"需要至少满足"的语义
  - default_service_id（下拉，过滤后只显示满足 requirements 的服务）
  - fallback_service_ids（多选，同样过滤）
  - allowed_service_ids（多选，同样过滤）—— 允许 C 端用户从中选
- **Capability Matching 实时提示**：选中不匹配的服务时，下拉项灰色 + 提示"该服务不支持图片输入"
- **销毁性操作确认**：解绑默认服务时用 ConfirmModal（遵守 `.claude/rules/ui-ux.md §硬规则 4`）

---

## §5 关键设计决策（回答 S0 的 7 条前置任务）

### §5.1 完整 Task Profile 清单（S0 #1 的答案）

基于 biz/ inventory 的 17 个调用点，最终 Task Profile 清单：

| # | task_id | 能力类型 | 业务场景 | 当前模型 |
|---|---|---|---|---|
| 1 | `sop.text` | chat-stream | SOP 节点 LLM 调用（纯文本）；Capability Matching 不要求 vision | deepseek-v3 / qwen-turbo / Claude |
| 1b | `sop.vision` | vision-stream | SOP 节点 LLM 调用（含图片输入）；Capability Matching 要求 input_modalities 含 image | qwen-vl / doubao-seed-1-8 |
| 2 | `chatbot.stream` | chat-stream | ChatBot 智能体对话（独立于 salesrag.chat） | 用户选择 |
| 3 | `salesrag.intent` | chat | SalesRAG 意图分析（RAG 前置步骤） | qwen-turbo-latest |
| 4 | `salesrag.chat` | chat-stream | SalesRAG 主回答生成 | 用户选择 |
| 5 | `salesrag.rerank` | rerank | SalesRAG 检索结果重排（两路：kb 和观点库） | qwen3-rerank |
| 6 | `salesrag.embed` | embedding | SalesRAG 入库文档向量化 | qwen-large / doubao-embedding |
| 7 | `salesrag.tagging` | chat | SalesRAG 文档切片打标 | qwen-turbo-latest |
| 8 | `salesrag.profile` | vision-stream | 客户档案多模态分析（文本+图片） | doubao-seed-1.8 / qwen-vl |
| 9 | `salesrag.chatstyle` | chat-stream / vision-stream | 聊天风格分析（文本+图片） | qwen-turbo + qwen3-vl-flash |
| 10 | `monitor.briefing` | chat | 监控简报生成（嵌套 analyze 子调用） | deepseek-v3 |
| 11 | `monitor.analyze` | chat | 单条笔记分析（作为 briefing 子 span，独立 profile 因为有独立调用入口） | deepseek-v3 |
| 12 | `monitor.transcribe` | asr | FunASR 视频音频识别 | FunASR 本地推理 |
| 13 | `ocr.baidu` | ocr | 百度高精度 OCR | 百度 OCR 含位置版 |

**相对 S0 示例清单的变化**：
- ❌ 删除 `card.generate`（实为 Markdown→图片，无 AI 调用）
- ❌ 删除 `file.parse`（实为本地解析，embedding 已计入 salesrag.embed）
- ✅ 保留 `sop.text` 与 `sop.vision` 为两个独立 profile（修正 S1 初稿的错误合并）——代码虽是同一 `StreamChat` 入口，但 Task Profile 的 `requirements` 不能既不要求 vision（对纯文本 SOP）又要求 vision（对图文 SOP）；executor 根据 input 是否含图片选 taskID
- ➕ 新增 `salesrag.intent`、`salesrag.tagging`、`salesrag.profile`、`salesrag.chatstyle`
- ✅ 保留 `monitor.analyze` 作为独立 profile（代码验证：除嵌套在 briefing 外，`POST /monitor/notes/:id/analyze` 是独立对外入口）

**总计 14 个 Task Profile**。

### §5.2 llm-model-switch 吸收方案（S0 #2 的答案）

**全部保留 + schema 演进（不 breaking）**：

| 产出 | 处理 |
|---|---|
| `llm_provider` 表 | 加 `provider_type` 字段（默认 llm，新加 ocr/asr 枚举值）；数据迁移：把 ali/volc/baidu/bailian/funasr 作为新 provider 行插入 |
| `llm_model` 表 → 改名 `ai_service` | `ALTER TABLE llm_model RENAME TO ai_service` + 加 `service_type`（default='llm'、NOT NULL）、`capability_json`、`latency_tier`、`quality_tier`、`tags` 字段；**VIEW `llm_model` 仅用于读兼容**，不作为写入口；所有 admin CRUD 的 GORM model 在 S4 直接指向 `ai_service`（带 `service_type='llm'` 过滤），避免 MySQL updatable view 的边界陷阱 |
| `llm_model_provider` 表 → 改名 `ai_service_route` | 同上方式；pricing 字段保留，加 `pricing_unit` 字段（enum：`per_1m_tokens` / `per_call` / `per_second`，default `per_1m_tokens`）；VIEW `llm_model_provider` 读兼容 |
| `user_model_preference` 表 | **不动**（C 端 UI 不改） |
| `/v1/llm/models` | **保持不变**，内部实现改为从 `ai_service WHERE service_type='llm' AND status='active'` 读 |
| `/v1/llm/preference` | **完全不变** |
| `/v1/admin/llm/*` 12 个 API | **保持不变**；新增并行的 `/v1/admin/ai/services` / `/v1/admin/ai/tasks` 等端点 |
| `llmrouter` 包（type `Router`）| **不改名、不修已有 API**。新建 `internal/pkg/aiservice` 包，Gateway 入口、Task Profile 路由、OCR/ASR 能力都在新包实现。`llmrouter` 继续服务 SOP/ChatBot 现有 `/v1/llm/*` 代码路径，作为 LLM 子能力的 shim 过渡：S4 阶段逐步把 llmrouter 的调用方切到 aiservice，最后 `llmrouter` 要么保留为内部实现细节，要么在 S7 前拆除。Go 代码侧**零 breaking change** |
| `DMXAPIClient` | **不动** |
| `ModelSelector.vue` | **不动** |
| admin-web LLM 管理 UI | **未创建**，本期做（扩展到 AI 服务全量） |

**用 VIEW + 双写 + 渐进迁移保证零 breaking change**。

### §5.3 chatbot.stream vs salesrag.chat 归属（S0 #3 的答案）

**两个独立 Task Profile**（inventory 已证实是两条独立代码路径）：
- `chatbot.stream` — 简单向量辅助对话，调用链短，用户可自选模型
- `salesrag.chat` — 完整 RAG（意图分析 + 多路检索 + rerank + 生成），内部服务多样

理由：合并会导致能力 requirements 不一致（salesrag.chat 需要长上下文 + 可能多模态，chatbot.stream 需求更简单）。分开让管理员能精细配。

**ModelSelector（C 端）作用域明确**：`user_model_preference` 表的用户选择**只作用于** `chatbot.stream` 和 `salesrag.chat` 两个 profile 的 default 覆盖。其他 salesrag.* 子 profile（intent/rerank/embed/tagging/profile/chatstyle）和 sop.* 系列的绑定由管理端统一控制，不向 C 端暴露。

### §5.4 OCR/ASR 的 Registry schema 子集设计（S0 #4 的答案）

**单表 + service_type 字段 + JSON capability 字段**（不用多张表）：

```
ai_service 表结构（concept，S2 定 SQL）：
- id, display_name, service_type (llm/ocr/asr), provider_id, ...
- capability_json JSON — 按 service_type 存不同字段
  - LLM: {input_modalities, output_modalities, context_window, max_output_tokens, features: {...}}
  - OCR: {image_formats, max_resolution, max_file_size_mb}
  - ASR: {audio_formats, max_duration_sec, languages, realtime: bool}
```

为什么用 JSON 而不是固定列：不同类型的能力维度差异大，硬拆列会让表又宽又稀疏；JSON 字段足够（管理端表单按 service_type 动态渲染，校验在应用层做）。

**查询性能声明**：Capability Matching 在应用层内存过滤（全表加载 ai_service 后筛）。当前服务规模 ~20 条、预期 < 100 条前不需要 JSON 索引或加入宽列。超过 100 条规模再单独评估，**不在本期讨论**。

**Schema 一致性保障**：能力字段的校验规则（如"input_modalities 必须是 ['text','image','audio'] 的子集"）统一在 Go 代码 `aiservice/capability` 模块定义，管理端和 Gateway 共用同一份 validator；禁止在两侧各写一套规则导致 drift。

### §5.5 Pricing 单位统一 + UsageRecord schema 扩展（S0 #5 的答案）

**Pricing 单位规范**（存 `ai_service_route.pricing_unit` 字段）：
- LLM：`per_1m_tokens`（unified 单位：元/每百万 token，input/output 分列）
- OCR：`per_call`（元/每次调用）
- ASR：`per_second`（元/每秒音频时长）

**UsageRecord schema 扩展**（S2 定精确 SQL）：
```
现有字段：保留
新增字段：
- service_type (llm/ocr/asr)  — 用于路由字段语义
- unit (per_1m_tokens/per_call/per_second)
- tokens_input, tokens_output (nullable, LLM only)
- call_count (nullable, 默认 1)
- duration_seconds (nullable, ASR only)
- pricing_input_snapshot, pricing_output_snapshot (记录调用时的单价，做历史快照)
- is_estimated (bool, stream 中断估算时为 true)
```

**历史 UsageRecord 不 backfill**（保留旧字段不动，新字段 null；新调用走新字段）。

**pricing 读取时机**：Gateway 入口（接到请求时）从 `ai_service_route` 读当前 pricing 并固化到 request context；记账时写入 UsageRecord 的 `pricing_*_snapshot` 字段。管理端改 pricing 对 in-flight 请求无影响（用旧价），对之后新请求立即生效。

**历史数据聚合声明**：按 task_id 聚合成本的运营查询**仅覆盖启用 Gateway 后的新数据**；老数据按原有 feature 维度（sop/chatbot 等）归档，不跨版本合并。若 Phase 2 有强需求，再评估 backfill 轻量 task_id 到历史行的方案。

### §5.6 config_*.yaml 模型配置去留（S0 #6 的答案）

**全部迁入 DB，config 只留凭据和基础设施**：

| config 项 | 处理 |
|---|---|
| `dmxapi.api_key` / `dmxapi.base_url` | **保留在 config**（provider 凭据，敏感） |
| `ali.*.api_key` / `volc.*.api_key` / `baidu.*.api_key` | **保留在 config** |
| `ali.text.model` / `volc.model` / `ali.vision.model` 等默认模型名 | **迁出**，通过 Task Profile 的 default_service 表达 |
| `langfuse.*` / DB 连接 / Redis | **保留在 config**（基础设施） |

**迁移后**：config 只负责"provider 是谁 + 怎么认证"，DB 负责"有哪些服务 + 能力如何 + 给谁用"。单一数据源，无双写。

**遗留 tech debt 不在本期动**：`config_prod.yaml` 的 `langfuse.base_url` 使用内网 IP `110.42.221.25`（S0 调研记录），本期保留现状。**记入 S7 收尾 checklist**，由后续独立小任务处理。

### §5.7 业务层调用入口形态（S0 #7 的答案）

**选择：多 method 入口**，不是单入口。

```go
// 业务层调用示例
resp, usage, err := ai.Chat(ctx, "sop.chat", ai.ChatRequest{Messages: ..., Stream: true})
resp, err := ai.Embed(ctx, "salesrag.embed", ai.EmbedRequest{Texts: ...})
results, err := ai.Rerank(ctx, "salesrag.rerank", ai.RerankRequest{Query: ..., Docs: ...})
text, err := ai.OCR(ctx, "ocr.baidu", ai.OCRRequest{Image: ...})
text, err := ai.ASR(ctx, "monitor.transcribe", ai.ASRRequest{Audio: ...})
```

**为什么不用 `ai.Call(taskID, req any)` 单入口**：
- Go 没有 sum type / union type，`any` + 类型断言会让编译期失去保护
- 调用方必须知道 profile 的能力类型才能构造 req，单入口省不了这份认知
- 多 method 更符合 Go 惯用法（http.Get/Post、sql.Query/Exec），IDE 补全更友好

**代价**：方法数量多（~5-7 个），但每个方法很薄（解析 task → 路由 → 调 adapter → 中间件链）。

**Gateway 内部共享同一套中间件链**（tracing/billing/retry/fallback 是同一份实现，只是被不同入口方法复用）。

**方法细节补充**：
- `ai.Chat` 覆盖纯文本 chat 与 vision chat（通过 `ChatRequest.Messages` 里是否含图片 content 自然支持多模态，无需单独 `ai.Vision` 方法——与 OpenAI SDK 风格一致）
- 流式与非流式不拆方法：`ChatRequest.Stream bool` 控制，返回 `ChatResponse` 或 `<-chan ChatChunk`（Go 里通过返回 interface 或多返回值处理）。具体签名 S2 定
- `taskID` 用字符串而不是枚举类型，换自由度换编译期类型安全——缓解：`aiservice/profile` 包导出所有 taskID 字符串常量（`profile.SopText = "sop.text"`），业务层 import 这个包即获得 IDE 补全和拼写检查

---

## §6 进 S2 前的确认项

在客户确认本 proposal 后，进入 S2 前需要明确的最后几个点（留到 S2 spec 里细化，但 S1 已框定范围）：

1. **migration 顺序**：rename 表 → 加字段 + default → 建只读 VIEW → 插入新 provider → **同一 migration 内必须自带 seed INSERT**：14 条 Task Profile + ali/volc/baidu/bailian/funasr 5 个新 provider 行 + 它们的 ai_service 行 + 现有 llm_model 行补 service_type='llm'。不得依赖部署人事后手工录入数据
2. **中间件链具体顺序**（S2 决）：初步建议 `Tracing → Billing → Retry → Fallback → Adapter`
3. **Task Profile 修改是否需要审计日志**：建议加，S2 确定
4. **Capability Matching 是否允许超管 override**：建议允许，走二次确认 + 审计
5. **Gateway 健康检查端点**：建议 `/healthz/ai`，返回各 provider 最近 1 分钟的错误率
6. **S3 验证策略**：多数 Task Profile 通过 biz 层 unit test 覆盖；端到端用 dev 环境手测（本功能无 C 端 UI 改动，不需要 Playwright）
7. **多租户可见性**：Service Registry 当前全站共享可管理；不做租户隔离（项目目前单租户形态）。S2 明确写入 spec

---

## §7 S0 决策一致性检查

### 一轮 S0 决策
| S0 决策 | proposal 处理 |
|---|---|
| Phase 1 不做语义缓存 / cost-aware 路由 | §1、§3 范围和未纳入项明确 |
| Registry 存 DB | §3、§5.2 |
| C 端 UI 不动 | §1、§3、§5.2 |
| 吸收 llm-model-switch | §5.2 详细方案 |
| OCR/ASR 纳入 | §4、§5.1、§5.4 |
| Capability Matching 为 Phase 1 核心 | §4 验收标准 + §5.4 |
| 10-14 天工时 | §2 报价（明确 11-12 正常 + buffer 到 14 最坏） |

### 二轮 S0 review 追加修订 9 条
| 二轮新增 | proposal 处理 |
|---|---|
| 迁移期双路径双记账风险 | §3 风险表（新 Gateway 单写 UsageRecord，老封装层 billing 关闭） |
| UsageRecord 容纳 token/次/秒三种计量 | §5.5 新增字段清单 |
| 单入口 vs 多 method 决策 | §5.7（决：多 method） |
| config_*.yaml 模型配置去留 | §5.6（决：凭据留 config，模型迁 DB） |
| 中间件 / adapter 测试策略 | §4 验收标准测试覆盖段 |
| Capability Matching 反驳文案修正 | proposal 无反驳段（S0 已修），§5.1 / §5.4 继承结论 |
| OCR/ASR 调用点 ≤2 可一次切换 | §3 风险表 + S2 迁移策略 spec |
| Gateway 健康检查 | §6 第 5 条 `/healthz/ai` |
| S6 部署顺序 | §6 第 1 条 + S0 已写入 |

### S1 review 追加修订
| S1 review | proposal 处理 |
|---|---|
| P0 `sop.chat` 合并违反 Capability Matching | §5.1 拆为 `sop.text` + `sop.vision`（总计 14 profile） |
| P0 VIEW 的 DML 语义 | §5.2 明确"VIEW 只读，写操作走新 GORM model 直指 ai_service" |
| P0 `LLMRouter` 改名 breaking | §5.2 改为"llmrouter 包保留，新建 aiservice 并存" |
| P1 Langfuse 内网 IP 遗留 | §5.6 末段记入 S7 tech debt |
| P1 pricing 时机语义 | §5.5 末段明确 |
| P1 salesrag.chat 用户选择边界 | §5.3 末段明确 |
| P1 JSON 性能声明 | §5.4 明确 < 100 条不建索引 |
| P1 工时 buffer 对账 | §2 明确 11-12 正常 + 1 天 buffer + 2-3 追加 = 最坏 14 |
| P1 billing 覆盖 3 种 service_type | §4 测试覆盖扩充 |
| P1 rate limit 立场 | §3 风险表 Provider rate limit 行 |
| P1 seed 数据责任 | §6 第 1 条 |
| P2 多租户声明 | §6 第 7 条 |
