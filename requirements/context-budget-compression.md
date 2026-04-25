# Context Budget & Compression

## 来源
- 提出人：用户
- 提出日期：2026-04-25

## 需求描述
当前项目需要建立一套通用的上下文窗口治理策略，用于在 LLM 调用前完成 token 预估、预扣费预算、上下文窗口安全控制，以及触顶后的智能压缩/裁剪。该策略不能针对现有 SOP 模板或具体 stage 做特殊设计，必须抽象为通用的 context fragment 机制，未来可覆盖 SOP workflow、chatbot、SalesRAG、admin AI tools、文档处理和后续 agent/tool-use 场景。

用户已确认以下关键约束：
- 数据库已登记模型 `context_window` 和 `max_output_tokens`。
- 预扣分发生在模型调用前，因此必须有调用前 token 预估；官方 usage 只能用于事后对账和动态校准。
- 不希望每接入一个模型都强依赖加载一个官方 tokenizer，但必须按模型保存估算参数和配置文件。
- 预估目标应尽量接近真实 token，理想目标为多数场景接近 95%，但工程上必须优先避免系统性低估。
- context window 不能 100% 放开，默认使用安全比例；用户认可 85% safe ratio。
- 不自动切换模型。
- 当前请求/当前步骤不裁剪。
- 用户端输入计数不要展示“还能输入约 X 字”，而展示 `x/y`；默认 `y=40000` 字符可作为普通文本输入框上限。
- 需要 admin web 纳入模型能力、`max_output_tokens`、token 估算参数、前端配置校验的管理范围。
- 需要智能压缩策略，且必须通用，不得写死 SOP stage、node index、template id 或已有数据库模板结构。

## 业务目标
- 降低 LLM 请求因上下文超限导致的失败率。
- 让积分预扣更接近实际消耗，减少 Reserve/Reconcile 的大额偏差。
- 让管理端可以清晰、可校验地维护模型上下文能力和估算配置。
- 在用户无感或少感知的情况下完成上下文压缩，保护当前请求和关键内容。
- 建立一套可扩展到多模型、多 provider、多业务场景的通用上下文预算系统。

## 通用性硬约束
Context Budget Manager 的核心输入必须是通用 `ContextFragment` 列表，而不是 SOP node/stage/template 结构。SOP workflow、chatbot、SalesRAG、admin AI tools、文档处理等业务模块只负责把自身上下文映射为 fragment metadata。

Context Budget Manager 不允许读取或判断 `sop_template_id`、`node_id`、`stage_name`、`template_id` 等业务字段来决定裁剪策略；这些字段最多作为 source metadata 进入日志和追踪。SOP 只能作为 fragment producer，不能成为裁剪策略的特殊分支。

`ContextFragment` 最小 metadata 建议：
- `id`
- `role`: `immutable` | `recent` | `durable` | `evidence` | `working` | `discardable`
- `source`: `system` | `user` | `assistant` | `tool` | `file` | `kb` | `db` | `web` | `internal`
- `content_type`: `text` | `attachment` | `tool_result` | `reasoning` | `summary` | `structured_data`
- `importance`: 0-100
- `recency` / `order`
- `compressibility`: `none` | `summarize` | `reference` | `drop`
- `token_estimate`
- `parent` / `source_reference`

## Token 预估与计费口径
Token 预估必须同时服务两个调用前决策：
1. credits Reserve 预扣费估算。
2. context window 发送前预算判断。

调用后 provider 返回的 `usage.prompt_tokens` / `usage.completion_tokens` 只能用于 Reconcile 对账和模型级 token profile 动态校准，不得作为 Reserve 的前置依赖。

每个模型必须保存独立 token estimation profile 和校准参数。官方 tokenizer 可作为可选能力或离线 benchmark，但不能成为系统主路径的硬依赖。token profile 缺失时，应使用 provider/model family 默认 profile + 更高 safety multiplier。

95% 相近可作为目标，但 S1/S2 需定义可验证指标，例如 P50 ≤ 5% error、P90 ≤ 10% error，并避免 P99 系统性低估。评估集必须覆盖中文、英文、代码、Markdown 表格、JSON、符号和混合文本。

## Context Budget 定义
定义：
- `context_window`：模型输入 + 输出的总 token 窗口能力。
- `max_output_tokens`：模型能力上限，不等于每次请求实际预留输出。
- `reserved_output_tokens`：本系统针对一次调用预留的输出预算，用于计算输入预算；默认可先评估 16384，但 S1/S2 必须结合现有 LLM 输出分布和成本风险确认。
- `safe_ratio`：默认 85%，作用于扣除 `reserved_output_tokens` 后的输入预算。
- `fixed_overhead_tokens`：system/developer prompt、message envelope、tool schema、provider adapter 包装等固定开销。

公式：

```text
safe_input_budget = floor((context_window - reserved_output_tokens - fixed_overhead_tokens) * 0.85)
```

`max_output_tokens` 不得被实现为每次调用都完整预留，否则 1M context / 384K output 这类模型会被过度扣减输入空间。

## Admin Web 配置范围
Admin Web 必须支持或至少规划以下配置/校验：
- `context_window`：模型总上下文窗口能力。
- `max_output_tokens`：模型单次最大输出能力。
- `reserved_output_tokens` 默认策略：后端可按 operation type 计算，admin 展示解释。
- `token estimation profile`：模型级估算参数、`safety_multiplier`、`calibration_multiplier`。
- 校验：`max_output_tokens < context_window`；`reserved_output_tokens < context_window`；`safe_input_budget` 计算结果必须为正。
- 风险提示：修改 `context_window`、`max_output_tokens`、token profile 会影响预扣费、上下文裁剪和失败率。

## 智能压缩职责边界
裁剪边界必须由程序规则决定，LLM 只能用于生成摘要内容：
- 程序规则决定哪些 fragment 锁定、可摘要、可引用、可删除。
- LLM compression prompt 只负责在给定 fragment 集合和保留要求下生成摘要。
- LLM 不得自行决定删除 `immutable` / `critical` 内容。
- 所有摘要结果必须标记来源 fragment id，便于审计和回溯。

critical 内容必须保护：
- 当前用户请求、当前任务指令、系统/权限/格式约束。
- 用户明确要求、否定指令、手动编辑或确认过的内容。
- 关键事实：数字、价格、时间、姓名、名单、合同条款、API 参数。
- 最近一次直接产出或已确认决策。

critical 内容不得直接 drop；如必须压缩，只能进入带来源引用的保护性摘要。

## 压缩触发与失败兜底
压缩触发需同时支持：
- 同步触发：LLM 调用前 `estimated_tokens > safe_input_budget` 时立即压缩。
- 异步触发：一次 LLM 调用完成后，如果 run/session/thread 累积上下文超过 soft threshold，则后台生成或更新 summary。
- 建议阈值：`soft_threshold = safe_input_budget * 70%`，`hard_threshold = safe_input_budget * 85%`，最终值 S1/S2 确认。
- 异步压缩失败不得阻断主流程，但必须记录日志并在下次同步触顶时兜底处理。

失败兜底：
- 压缩后仍超限：继续降级低价值 fragment，直到只剩 `immutable` + `critical` + minimal `recent`。
- 当前请求自身超限：不得截断，返回用户可理解错误。
- token profile 缺失：使用 provider/model family 默认 profile + 高 safety multiplier。
- usage 缺失或 provider 返回异常：Reserve 使用估算值，Reconcile 按现有容错策略处理，并记录 calibration skipped。

## 可观测性
每次预算/压缩需记录：
- `model` / `provider`
- `context_window`
- `max_output_tokens`
- `reserved_output_tokens`
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

## 用户体验边界
用户端普通文本输入显示 `x / 40000` 字符；这是 UX 输入限制，不等同于 token budget。附件、知识库、网页、数据库结果不得简单并入该字符上限，应进入后端 fragment budget 和压缩机制。

## 优先级
高

## Triage
- 推荐轨道：Standard
- 分类理由：
  1. 数据库 schema 变更：可能是。预计需要新增或扩展模型 token profile、上下文压缩记录、摘要缓存或 token stats 配置。
  2. 新增 API 端点：可能是。admin web 可能需要新增或扩展 token profile / capability 配置接口。
  3. 新外部服务集成：否。可优先不引入新的第三方服务；官方 tokenizer 如使用也应作为可选 profile 能力。
  4. 影响文件数：>3。预计涉及 `numind-server`、`numind-admin-web`，并可能涉及 `numind-web-v3` 输入计数体验。
  5. 高风险业务逻辑（支付/权限）：是。该需求影响 credits 预扣费、LLM 调用上下文组装和模型能力配置。
- 人类决定：确认进入 Standard Track（2026-04-25）

## 备注
- 该功能的核心抽象应是 `Context Budget Manager`，输入为通用 context fragments，输出为预算内 prompt package。
- 通用 fragment role 建议包括：`immutable`、`recent`、`durable`、`evidence`、`working`、`discardable`。
- “能裁什么/不能裁什么”应由程序规则和 fragment metadata 决定；摘要内容如何保留关键信息可由 compression prompt 实现。
- `max_output_tokens` 应表示模型能力上限，不等同于每次调用实际预留输出预算；后端需区分 `model.max_output_tokens` 和 `reserved_output_tokens`。
- 初步策略：`safe_input_budget = (context_window - reserved_output_tokens) * 85%`。
- 初步默认 `reserved_output_tokens` 可评估 `16384`，用于覆盖约 95% 普通 SOP/chat 长输出场景；最终值需在 S1/S2 中结合现有数据和价格风险确认。
