# AI Service Manager — 迁移前后 Billing 对账文档

> **Task 9 产出物**。记录 SOP + ChatBot 从 LLMRouter（旧路径）迁移到 AI Gateway 后，`usage_record` 表字段的变化对照，供 S5 阶段对账与验证使用。

---

## 背景

Task 9 将以下两条调用路径从 `llmrouter.StreamChat` 迁移到 `aiservice.ChatStream`：

| 调用点 | 文件 | Task Profile |
|--------|------|-------------|
| SOP 节点执行（modelKey 路径） | `internal/numind/biz/sop/executor.go` `executeViaGateway` | `sop.text` / `sop.vision`（按消息是否含图片动态选择） |
| ChatBot 流式对话 | `internal/numind/biz/chatbot/stream.go` `ChatStream` | `chatbot.stream` |

防双记账措施：调用 `aiservice.ChatStream` 前注入 `ctx = aiservice.WithSkipLegacyBilling(ctx)`，LLMRouter 旧路径的 `billing.RecordLLM` 不再触发。

---

## UsageRecord 字段对照表

| 字段 | 迁移前（LLMRouter 旧路径） | 迁移后（AI Gateway 新路径） | 变化说明 |
|------|--------------------------|---------------------------|---------|
| `user_id` | ✅ 从 `billing.BillingContext` 取 | ✅ 从 `ctx.Value(ctxKeyUserID{})` 取 | 来源相同，无变化 |
| `service_type` | `llm_chat`（billing.RecordLLM 写死） | `llm`（从 Registry `service_type` 字段取） | 值从 `llm_chat` 变为 `llm`（规范化） |
| `provider` | 实际调用的 DMXAPI Provider 名（如 `dmxapi`） | Registry 中配置的 Provider 名（如 `volc`, `ali`） | 更精确，反映真实 Provider |
| `model` | `ProviderModelID`（如 `deepseek-v3-250324`） | `ServiceKey`（来自 registry.ResolvedRoute） | 语义相同，来源切换到 Registry |
| `operation` | `chatbot_chat` / `sop_node_execute`（调用方硬编码） | `task_id`（如 `chatbot.stream`, `sop.text`） | 从 `Operation` 字段变为 `TaskID` 字段（见下） |
| `prompt_tokens` | 从 `billing.TokenUsage.PromptTokens` 取 | 从 `ChatChunk.Usage.PromptTokens` 取（IsFinal chunk） | 来源切换，数值相同 |
| `completion_tokens` | 从 `billing.TokenUsage.CompletionTokens` 取 | 从 `ChatChunk.Usage.CompletionTokens` 取 | 同上 |
| `total_tokens` | 从 `billing.TokenUsage.TotalTokens` 取 | 从 `ChatChunk.Usage.TotalTokens` 取 | 同上 |
| `reasoning_tokens` | 从 `billing.TokenUsage.ReasoningTokens` 取 | 从 `ChatChunk.Usage.ReasoningTokens` 取 | 同上 |
| `is_estimated` | 不记录（旧路径无此概念） | Gateway Billing 中间件在流式中断时自动估算并标记 | **新增语义** |
| `is_fallback` | 不记录 | Gateway Fallback 中间件自动标记 | **新增语义** |
| `task_id`（新扩展字段，nullable） | `NULL`（旧路径不填） | `sop.text` / `sop.vision` / `chatbot.stream` | **新增字段**，历史记录保持 NULL |
| `unit`（新扩展字段，nullable） | `NULL` | `per_1m_tokens` | **新增字段** |
| `pricing_input_snapshot` | `NULL` | 调用时 Registry 定价快照（元/百万 tokens） | **新增字段**，便于成本审计 |
| `pricing_output_snapshot` | `NULL` | 同上 | **新增字段** |
| `cost_cents` | 由 `billing.RecordLLM` 根据 PricingRule 查库计算 | 由 Gateway Billing 中间件使用 Snapshot 计算 | 计算来源切换到 Snapshot（更快、无额外 DB 查询） |

---

## BLOCKER：S5 阶段 Baseline 对账待办

> **以下对账工作无法在 Task 9 中执行，原因：dev 环境未 push（per 任务要求），Gateway Registry 中 `sop.text`/`sop.vision`/`chatbot.stream` task profile 需要数据库 seed 数据正确配置后才能产生真实 UsageRecord。**

### S5 验证时必须补做的对账步骤

1. **前提**：Task 8 的 `seed.go` + migration SQL 已在 dev 环境执行，确认 `task_profile` 表中存在以下行：
   - `task_id = 'sop.text'`，`capability = 'llm'`，绑定了有效 Service
   - `task_id = 'sop.vision'`，`capability = 'llm,vision'`，绑定了有效 Service
   - `task_id = 'chatbot.stream'`，`capability = 'llm,streaming'`，绑定了有效 Service

2. **SOP 执行对账**：选取同一 SOP 节点，迁移前后各执行一次，对比 `usage_record` 行：
   - `task_id` 是否从 `NULL` 变为 `sop.text`
   - `pricing_input_snapshot` 是否非 NULL
   - `is_fallback` 是否正确标记（首选 Provider 不可用时）

3. **ChatBot 对账**：同一用户发一条消息，对比 `usage_record` 行：
   - `task_id` 是否从 `NULL` 变为 `chatbot.stream`
   - `service_type` 是否从 `llm_chat` 变为 `llm`
   - Token 计数是否合理（与旧路径同量级）

4. **双记账检验**：搜索同一 trace_id 对应的 `usage_record` 行，确认每次 LLM 调用只有**一条**记录（而非两条）。

### 当前已知 Blocker

| Blocker | 影响 | 负责阶段 |
|---------|------|---------|
| dev 环境未 push（Task 9 per requirement） | 无法运行真实流量验证 | S5 |
| Registry seed data 需人工确认（sop.text/vision/chatbot.stream task profiles） | Gateway 调用会因 `ResolveTask` 失败而报错 | S5 before |
| `modelKey` 透传到 Gateway 的 `ModelOverride` 尚未实现 | 用户选择特定模型的场景由 profile 默认模型处理，可能偏离用户意图 | Task 9 tech debt，建议 S5 前补充 |

---

*文档创建时间：2026-04-15*
*对应 commit：Task 9 `feat(aiservice): migrate SOP + ChatBot to Gateway`*
