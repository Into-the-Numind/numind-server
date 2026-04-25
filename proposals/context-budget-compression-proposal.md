# Context Budget & Compression — 提案

## §1 方案概述 [客户可见]

本功能要给莫小派建立一套通用的「上下文预算与压缩」能力。它的核心不是给某个 SOP 模板写特殊裁剪规则，而是在所有 LLM 调用前，把系统提示、用户输入、历史对话、附件、知识库片段、工具结果等内容统一映射成 `ContextFragment`，再由一个通用的 Context Budget Manager 决定：

- 本次模型调用大约会消耗多少输入 token，需要预扣多少积分。
- 当前模型的上下文窗口是否足够安全，不把窗口用到 100%。
- 哪些内容必须保留，哪些内容可摘要、可引用、可删除。
- 超出预算时如何先智能压缩，再做确定性降级，最后才给用户可理解的错误。

预期效果：

- LLM 请求因上下文超限导致的失败率显著下降。
- credits Reserve 预扣更接近真实消耗，Reconcile 大额多退少补减少。
- admin web 能维护模型上下文能力、输出上限、token 估算参数，并在保存前做校验。
- 用户端不暴露复杂 token 概念，普通输入只显示 `x / 40000` 字符计数；附件、知识库、历史上下文由后端预算系统统一治理。
- 后续 SOP workflow、chatbot、SalesRAG、admin AI tools、文档处理、agent/tool-use 场景可以复用同一套 fragment 策略。

本方案的硬边界：

- Context Budget Manager 只接收通用 `ContextFragment` 列表，不读取 `sop_template_id`、`node_id`、`stage_name`、`template_id` 来决定裁剪策略。
- SOP 只能作为 fragment producer，把自身上下文映射为 fragment metadata；不能成为 budget/compression 策略里的特殊分支。
- 当前请求、当前任务指令、系统/权限/格式约束、用户明确要求和关键事实不直接 drop。
- 不自动切换模型。
- `max_output_tokens` 是模型能力上限，不等于每次调用都完整预留输出。

## §2 报价与周期 [客户可见]

- 预估工作量：10-14 个工作日
- 报价：按 Standard Track 复杂后端 + 管理端配置 + 计费链路改造评估，建议按 2 周交付包报价
- 交付时间线：
  - 第 1-2 天：S2 技术设计，冻结数据结构、API 契约、trace topology、压缩策略
  - 第 3 天：S3 实施计划和任务拆分
  - 第 4-10 天：S4 后端核心、admin 配置、用户端输入计数、测试
  - 第 11-12 天：S5 本地自动验收、E2E、可观测性验证
  - 第 13-14 天：S6 dev 环境人工验收与修复缓冲

说明：如果 S2 数据审计发现现有 LLM 调用入口比预期更多，或需要覆盖文档处理/agent tool-use 的首版 producer，周期应上浮 2-4 天。首版建议先覆盖现有高频 LLM 文本路径，并保证抽象可扩展。

## §3 技术可行性 [AI 内部]

### 产品级思考

这个功能真正要解决的不是「某次 prompt 太长」这个局部问题，而是三条链路尚未共享同一个预算口径：

- 模型能力：`context_window` / `max_output_tokens`
- 计费：调用前 Reserve 估算、调用后 Reconcile 对账
- 上下文组装：系统提示、历史、附件、RAG 证据、工具结果

如果继续在每个业务入口里手写裁剪规则，短期能止血，长期会形成不可维护的模板/节点例外表。正确抽象是把业务语义压缩为 fragment metadata，让通用策略做预算和保留顺序。业务模块只负责「这段内容是什么、重要性如何、能否压缩」，不负责「模型窗口快满时该怎么裁」。

需要避免的误区：

- 不把 `max_output_tokens` 当成每次调用都要预留的输出长度。1M context / 384K output 的模型如果每次预留 384K，会浪费大量输入空间。
- 不用单一 tokenizer 当跨模型真相。官方 usage 是事后对账和校准依据，Reserve 必须调用前可运行。
- 不把用户端 40000 字符上限误当成 token budget。它只是输入框 UX 限制，附件和知识库走后端 fragment budget。
- 不让 LLM 自己决定删什么。程序规则决定边界，LLM 只生成摘要内容。

### 现有功能复用

- `ai_service.capability_json` 已包含 LLM 能力字段，`profile.ServiceCapability` 已定义 `ContextWindow` 和 `MaxOutputTokens`，可作为模型窗口能力的入口。
- `registry.ResolvedRoute` 已把服务能力、provider、provider model id 解析到调用侧，可扩展为 Context Budget Manager 的模型能力来源。
- `numind-admin-web` 已有 AI Service 管理页面、capability schema、路由配置、pricing rule 提示，可在同一模型编辑流里加入上下文能力校验。
- credits 系统已有 `ICreditService.CheckAndEstimate`、`Reserve`、`Reconcile`、`FinalizeReservation`，可把「字符数估算」升级为「token budget estimate → pricing → credits」。
- `credit_estimation_coefficient` 和 admin 估算系数页面可复用版本化/审计思路，但它目前偏 cost coefficient，不能完整承载模型级 token profile。建议新增或演进为独立 token estimation profile，避免把 context budget 强塞进旧字段。
- Gateway 路径已有 `aiservice.ChatStream` 和 usage 回传，调用后可用 `usage.prompt_tokens/completion_tokens` 做 Reconcile 与 token profile calibration。
- Langfuse 已在 SOP/chatbot 路径接入 trace/generation，适合记录 budget/compression metadata。

### 建议技术方案

#### 1. 通用 ContextFragment

所有业务入口在调用 LLM 前先产出 fragment 列表。最小字段：

- `id`
- `role`: `immutable` | `recent` | `durable` | `evidence` | `working` | `discardable`
- `source`: `system` | `user` | `assistant` | `tool` | `file` | `kb` | `db` | `web` | `internal`
- `content_type`: `text` | `attachment` | `tool_result` | `reasoning` | `summary` | `structured_data`
- `importance`: 0-100
- `order` / `recency`
- `compressibility`: `none` | `summarize` | `reference` | `drop`
- `token_estimate`
- `parent` / `source_reference`
- `critical`: bool 或由 role + source + explicit flags 派生

业务模块可以把 SOP node id、chat session id、document id 放进 metadata 供日志追踪，但 Budget Manager 不允许用这些业务字段做策略判断。

#### 2. Token Estimation Profile

新增模型级 token estimation profile，目标是调用前可估、调用后可校准：

- provider/model/service 维度的估算参数。
- 文本类型权重：中文、英文、代码、JSON、Markdown、符号混合。
- `safety_multiplier`：防低估保护。
- `calibration_multiplier`：基于 provider usage 的动态校准。
- fallback profile：exact model 缺失时走 provider/model family 默认 profile，并提高 safety multiplier。

验证指标建议：

- P50 absolute error <= 5%
- P90 absolute error <= 10%
- P99 不允许系统性低估；低估样本必须被 safety multiplier 覆盖
- 评估集覆盖中文、英文、代码、Markdown 表格、JSON、符号、混合文本、附件引用标签、RAG 片段

#### 3. Context Budget 公式

模型能力口径：

- `context_window`：输入 + 输出总窗口能力。
- `max_output_tokens`：模型单次最大输出能力上限。
- `reserved_output_tokens`：本系统为一次调用预留的输出预算。
- `safe_ratio`：默认 85%。
- `fixed_overhead_tokens`：system/developer prompt、message envelope、tool schema、provider adapter 包装等固定开销。

预算公式：

```text
safe_input_budget = floor((context_window - reserved_output_tokens - fixed_overhead_tokens) * 0.85)
```

默认策略建议：

- 首版 `safe_ratio = 0.85`。
- `reserved_output_tokens` 由 operation type 决定，不绑定 SOP stage/node/template。
- 首版普通长文本生成可默认 16384，并被 `max_output_tokens` 和 `context_window` 上限约束。
- compression/self-summary 这类内部短输出 operation 可使用更小默认值，例如 4096，S2 再结合现有输出分布确认。
- 如果计算后 `safe_input_budget <= 0`，admin 保存配置或运行时调用应失败并给出明确错误。

#### 4. 预扣费链路

Reserve 调用前统一使用 Context Budget Manager 的估算结果：

1. producer 生成 fragments。
2. token estimator 估算每个 fragment + fixed overhead + reserved output。
3. Context Budget Manager 预算检查并必要时压缩。
4. 使用估算 prompt tokens + reserved/estimated completion tokens 调 pricing calculator。
5. credits Reserve 预扣。
6. provider 返回 usage 后，Reconcile 按实际 cost 多退少补。
7. usage 缺失时使用估算值完成容错，并记录 calibration skipped。

这会替代当前仅按 `promptChars` 粗估的主路径。旧 `credit_estimation_coefficient` 可以在迁移期保留给未接入 ContextFragment 的路径或作为 fallback。

#### 5. 智能压缩

压缩边界由程序规则决定：

- 永不 drop：`immutable`、critical、当前用户请求、当前任务指令、系统/权限/格式约束。
- 优先 summarize：高价值但可压缩的 `durable`、较早 `recent`、长 `assistant` 输出、长 `tool_result`。
- 优先 reference：附件原文、文件内容、知识库命中片段，在可回溯时替换为引用标签和摘要。
- 优先 drop：低 importance、`discardable`、过期 working scratch、可再检索证据。

LLM compression 只负责在给定 fragment 集合和保留要求下生成摘要。摘要必须记录来源 fragment id、压缩前后 token、生成模型、checksum，便于审计和回溯。

#### 6. 后台无感压缩

同时支持两种触发：

- 同步触发：LLM 调用前 `estimated_tokens > safe_input_budget` 时立即压缩。
- 异步触发：一次 LLM 调用完成后，如果 run/session/thread 累积上下文超过 soft threshold，则后台生成或更新 summary cache。

阈值建议：

- `soft_threshold = safe_input_budget * 70%`
- `hard_threshold = safe_input_budget * 85%`

后台压缩失败不阻断用户主流程；记录日志、trace event、重试状态。下次同步触顶时再走兜底压缩。

#### 7. 失败兜底

- 压缩后仍超限：按 fragment role/importance/recency 继续确定性降级，直到只剩 `immutable` + critical + minimal `recent`。
- 当前请求自身超限：不截断，返回用户可理解错误，提示减少当前输入或附件。
- token profile 缺失：使用 provider/model family 默认 profile + 更高 safety multiplier。
- usage 缺失或 provider 异常：Reserve 使用估算值，Reconcile 按现有容错策略处理，并记录 calibration skipped。
- compression LLM 失败：不让 LLM 决策越界，回退到规则型 reference/drop；若仍超限则返回明确错误。

#### 8. Admin Web 配置

admin web 需要纳入以下范围：

- AI Service 编辑页展示并编辑 `context_window`、`max_output_tokens`。
- 展示 `reserved_output_tokens` 默认策略说明和实时 `safe_input_budget` 预览。
- 管理 token estimation profile：估算参数、`safety_multiplier`、`calibration_multiplier`、fallback 状态、最近校准误差。
- 保存校验：
  - `max_output_tokens < context_window`
  - `reserved_output_tokens < context_window`
  - `safe_input_budget > 0`
  - `safe_ratio` 在允许范围内，默认 0.85
  - profile 参数必须为正数，且缺失时明确展示 fallback
- 风险提示：修改模型能力或 token profile 会影响预扣费、上下文裁剪和失败率。
- 管理端按现有约束使用 DataTable 和紧凑表单，不引入外部 UI 框架。

#### 9. 用户端输入计数

用户端普通文本输入框显示 `x / 40000` 字符计数：

- `40000` 是 UX 字符上限，不等同于模型 token budget。
- 超过上限禁止提交或给出 inline error。
- 附件、知识库、网页、数据库结果不计入这个字符上限，进入后端 fragment budget。

### 技术风险

- 风险：token 估算低估导致仍然超窗或 Reserve 不足。
  - 缓解：模型级 profile + safety multiplier + 85% safe ratio + provider usage 校准 + P90/P99 评估集。

- 风险：压缩误删关键业务事实。
  - 缓解：程序规则锁定 critical fragment；LLM 只生成摘要；摘要保留 source ids；当前请求不裁剪。

- 风险：改造 LLM 调用入口范围过大。
  - 缓解：先做通用 ContextFragment 和 manager，再按 producer 接入；首版优先接高频 gateway 文本路径，其他路径保留 fallback 并登记后续任务。

- 风险：admin 配置错误造成生产调用失败。
  - 缓解：前后端双重校验；保存前预览 safe budget；危险字段修改写 audit log；不允许保存明显无效配置。

- 风险：后台压缩增加额外 LLM 成本。
  - 缓解：soft threshold 触发、summary cache、checksum 去重、限制重试；compression operation 使用较小 reserved output 和可观测成本记录。

- 风险：现有 `credit_estimation_coefficient` 与新 token profile 语义重叠。
  - 缓解：S2 明确迁移边界；建议新建 token profile 表，旧 coefficient 仅作为迁移期 fallback 或历史兼容。

### 涉及仓库

- [x] numind-server
- [x] numind-web-v3
- [x] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：是
- Trace 起点：
  - SOP：现有 SOP node run / Gateway 调用入口，S2 需明确以哪个 biz 函数创建或复用 trace。
  - Chatbot：`chatbot.ChatStream` 当前已有 `chatbot-chat` trace，可追加 budget/compression metadata。
  - Compression：后台或同步摘要生成必须作为独立 generation/event 记录。
- Generation 点：
  - `sop.llm.call`：业务 LLM 调用。
  - `chatbot.llm.call`：chatbot LLM 调用。
  - `context.compression.summary`：智能摘要压缩调用。
  - 其他 producer 接入时按 operation type 命名，不按 SOP stage/node/template 命名。
- 关键元数据：
  - `model` / `provider`
  - `context_window`
  - `max_output_tokens`
  - `reserved_output_tokens`
  - `safe_ratio`
  - `fixed_overhead_tokens`
  - `safe_input_budget`
  - `estimated_before`
  - `estimated_after`
  - `actual_prompt_tokens`
  - `actual_completion_tokens`
  - `reserve_amount`
  - `reconcile_delta`
  - `compression_actions`
  - `dropped_fragment_count`
  - `summarized_fragment_count`
  - `critical_fragment_count`
  - `calibration_ratio`
  - source metadata 可带 `run_id/session_id/document_id`，但不得驱动策略

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为终端用户，我需要系统在上下文很长时尽量自动处理，而不是直接报模型超限错误，以便长 SOP、长聊天、附件和知识库场景仍能稳定完成任务。
- 作为终端用户，我需要当前输入和当前任务指令不被系统偷偷裁剪，以便输出仍然围绕我刚刚提出的要求。
- 作为终端用户，我需要普通输入框显示清晰的 `x / 40000` 字符计数，以便知道能否提交，而不是被复杂 token 概念打断。
- 作为管理员，我需要在 admin web 配置模型的 `context_window`、`max_output_tokens` 和 token 估算 profile，以便新增模型时能控制上下文预算和预扣费风险。
- 作为管理员，我需要保存模型配置前看到校验和风险提示，以便避免错误配置导致线上大面积 LLM 调用失败。
- 作为财务/运营，我需要 Reserve/Reconcile 的偏差更可控，以便积分扣减更稳定、争议更少。
- 作为开发者，我需要 SOP、chatbot、SalesRAG、admin AI tools 能复用同一个 ContextFragment budget manager，以便后续功能不再重复写裁剪策略。
- 作为运维/开发者，我需要看到每次预算和压缩的可观测数据，以便排查超限、误压缩、估算漂移和扣费异常。

### 验收标准

- [ ] Context Budget Manager 的公开输入为 `[]ContextFragment` + model/operation budget config；核心策略代码不得读取 `sop_template_id`、`node_id`、`stage_name`、`template_id` 来决定裁剪。
- [ ] `ContextFragment` 支持 role/source/content_type/importance/order/compressibility/token_estimate/source_reference 等通用 metadata。
- [ ] 后端使用 `context_window`、`max_output_tokens`、`reserved_output_tokens`、`fixed_overhead_tokens`、`safe_ratio=0.85` 计算 `safe_input_budget`。
- [ ] `max_output_tokens` 不被实现为每次调用完整预留；每次调用使用 operation-level `reserved_output_tokens`，并校验不超过模型能力。
- [ ] token 预估在 LLM 调用前完成，并同时驱动 context budget 判断和 credits Reserve。
- [ ] provider usage 只用于调用后 Reconcile 和 token profile calibration；没有 usage 时不得阻断主流程，需记录 calibration skipped。
- [ ] token profile 缺失时使用 provider/model family fallback profile，并提高 safety multiplier；日志和 trace 标记 fallback。
- [ ] 评估集覆盖中文、英文、代码、Markdown 表格、JSON、符号、混合文本；S5 报告包含 P50/P90/P99 估算误差。
- [ ] 同步压缩在 `estimated_tokens > safe_input_budget` 时触发，压缩后重新估算，直到低于预算或进入兜底错误。
- [ ] 异步后台压缩在上下文累积超过 soft threshold 时触发，失败不阻断主流程，下次同步触顶可兜底。
- [ ] critical/current request/immutable fragments 不被直接 drop；如必须压缩，只能生成带 source ids 的保护性摘要。
- [ ] 摘要结果记录来源 fragment id、压缩前后 token、生成模型、checksum。
- [ ] 压缩后仍超限时，系统继续确定性降级低价值 fragment；若当前请求自身超限，返回用户可理解错误，不截断当前请求。
- [ ] Admin AI Service 配置支持或清晰入口跳转到 `context_window`、`max_output_tokens`、token estimation profile。
- [ ] Admin 保存配置校验 `max_output_tokens < context_window`、`reserved_output_tokens < context_window`、`safe_input_budget > 0`。
- [ ] Admin 修改上下文能力或 token profile 时展示风险提示，并保留 audit log。
- [ ] 用户端普通文本输入框显示 `x / 40000`；超限时阻止提交或展示 inline error。
- [ ] 每次预算/压缩记录 model/provider/window/reserved/safe/estimated/actual/reserve/reconcile/compression/calibration 等 metadata。
- [ ] 不自动切换模型。

### 边界情况

- 模型缺少 `context_window` 或 `max_output_tokens`：admin 配置视为不完整；运行时使用保守 fallback 或拒绝调用，S2 定义具体错误码。
- `max_output_tokens >= context_window`：admin 保存失败。
- `reserved_output_tokens + fixed_overhead_tokens >= context_window`：admin 保存或运行时预算失败。
- `safe_input_budget <= 0`：调用前失败，不进入 LLM。
- 当前用户输入单独超过 safe budget：不截断，返回可理解错误。
- 所有可压缩历史和证据都已摘要/引用/drop 后仍超限：返回可理解错误，日志标记 `context_budget_exhausted`。
- compression LLM 超时/失败：同步路径回退规则型降级；后台路径记录失败并结束，不影响主请求。
- provider 未返回 usage：使用估算 cost 完成 reservation finalization；跳过校准。
- provider 返回 usage 与估算严重偏离：记录 calibration outlier，S2 定义是否自动更新 multiplier 或仅进入 admin review。
- 多个请求并发更新同一 summary：通过 checksum/version 或 upsert 幂等避免覆盖较新摘要。
- 附件/知识库内容可引用但原文不可再取：不得只保留引用，必须保留足够摘要。
- tool schema 很大：进入 fixed overhead 或 tool fragment，不得漏算。

### 权限规则

- 终端用户不直接配置 context budget；只受到输入长度、余额检查、上下文治理结果影响。
- Admin 才能维护模型能力、token estimation profile、reserved output 默认策略。
- Admin 配置变更必须走现有 admin token 鉴权和 audit log。
- 压缩与摘要只能读取当前业务请求已有权限可访问的内容；不得因后台压缩跨用户、跨父子账户读取上下文。
- B2B2C、legacy tier、credits 双制规则不改变；本功能只影响 credits 模式下的预估/预扣与 LLM 调用上下文。

### UI 行为规格

#### Admin Web

- 页面位置：
  - AI Service 编辑页：模型能力配置区展示 `context_window`、`max_output_tokens`、safe input budget 预览。
  - AI 服务组下新增或复用「估算系数/Token Profile」页面：管理 token estimation profile。
- 布局要求：
  - 管理端使用 DataTable 展示 profile 列表。
  - 编辑使用紧凑表单；避免卡片网格。
- 交互模式：
  - 输入 context window/max output/profile 参数后，在 blur 或保存前校验。
  - 保存前展示风险提示；严重无效配置禁止保存。
  - profile 历史版本可查看，active 版本唯一。
- 状态处理：
  - loading：表格/表单加载 skeleton 或现有 loading 状态。
  - empty：提示暂无 profile，可新建 fallback 或模型专属 profile。
  - error：展示可重试错误。
  - success：保存后 toast，并刷新 safe budget 预览。

#### User Web

- 页面位置：
  - 普通文本输入框：SOP 当前步骤输入、chatbot 输入、SalesRAG 输入等高频文本入口。
- 布局要求：
  - 右下或输入框附近显示 `x / 40000`。
  - 接近上限时用现有中性/警示样式，不展示 token 估算。
- 交互模式：
  - 输入超过 40000 字符时阻止提交或显示 inline error。
  - 附件、知识库、网页内容不混入该字符计数。
- 状态处理：
  - compression 后台处理对用户无感。
  - 若当前请求自身超限，展示可理解错误，建议减少当前输入或附件。

### 非目标

- 不为具体 SOP template/stage/node 写特殊裁剪策略。
- 不自动切换到更大 context window 的模型。
- 不引入强依赖官方 tokenizer 的运行时主路径。
- 不把所有文档/附件简单塞进用户端字符上限。
- 不在 S1 阶段写代码。

### S2 需要冻结的问题

- token estimation profile 是新增表，还是在保留 `credit_estimation_coefficient` 的同时建立兼容映射。
- 首版接入的 producer 范围：建议 SOP Gateway 文本路径 + Chatbot 文本路径 + user web 输入计数 + admin profile 配置；SalesRAG/admin tools/document processing 作为同一抽象的后续 producer。
- `reserved_output_tokens` 的 operation type 默认值，需要基于现有 usage_record / node_run 输出分布抽样确认。
- `fixed_overhead_tokens` 的估算口径：message envelope、system prompt、tool schema、provider adapter 包装如何分别计入。
- compression summary 的存储模型：summary cache 表、context budget event 表、是否与 usage_record metadata 关联。
- calibration multiplier 的更新策略：自动滑动窗口更新还是 admin review 后生效。
