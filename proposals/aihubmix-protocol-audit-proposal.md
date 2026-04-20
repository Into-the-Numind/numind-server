# AiHubMix 协议审计 + 思考管道真实打通 — 提案

> **修订历史**：
> - v1 2026-04-20 (初稿)
> - v2 2026-04-20 (按独立 Opus reviewer 反馈修订 P0×3 / P1×5 + 用户 Q4/Q5/Q6 决策 + dev DB 实测结果)

## §1 方案概述 [客户可见]

当前"深度思考"按钮藏起来让用户以为默认打开，但后端两处（SOP 执行 `executor.go:109` 和 Chatbot 流式 `stream.go:177`）把 `thinking` 参数直接扔进地沟 (`_ = thinking`)，实际调用 AiHubMix 完全没有开启思考模式。用户付钱买了"思考算力"但拿到的是普通响应——这就是假思考。

本期做三件事：

1. **协议审计（T2）**：给 AiHubMix 4 个模型逐一调研"正确怎么调"，产出 `docs/aihubmix-protocol-reference.md`——一份让未来 AI 或新人看完就会正确使用的权威手册。包括：每个模型走哪个 URL、请求用 `max_tokens` 还是 `max_completion_tokens`、思考怎么激活、响应怎么解析、踩过的坑。附带**计费对账 spike**（发 100 个 reasoning token，看 AiHubMix 账单是否独立计价）。

2. **代码修正（T1）**：把 thinking flag 从 SOP/Chatbot 入口真的传到 AiHubMix 请求里。按 T2 调研的 per-model 规则构造请求（Claude/Gemini/DeepSeek 加 `reasoning_effort=medium`，GPT 5.4 强制 `max_completion_tokens`），同时补响应端 wire 解码把 `reasoning_tokens` 透传出去。

3. **DB 标志校正**：T2 审计过程发现现有 `ai_service.supports_thinking`/`thinking_only` 存在错误值（Gemini/DeepSeek/GPT 5.4 base 被标成 thinking_only=1，所有 thinking 变体被标 supports_thinking=0）。在本期一并用小 migration 修正，避免给未来 OpenRouter 扩展时留坑。

**交付后效果**：
- Claude/Gemini/DeepSeek：前端能看到真实 chain-of-thought（前端已准备好渲染 `thinking` event）
- GPT 5.4：受限于 OpenAI 加密推理策略看不到推理内容，但 Langfuse trace 能记录 `reasoning_tokens` 证明思考真的发生了，且成本计到位
- Langfuse：每次思考调用都有完整 generation 记录（reasoning_content + reasoning_tokens）
- credits 计费：reasoning_tokens 正确记账到 UsageRecord，不再吃暗账

## §2 报价与周期 [客户可见]

- **预估工作量**：6 天（v1 的 5 天 → v2 加 wire 修正 + ResolvedRoute 扩展 + 计费 spike）
  - S1 proposal 0.5d（当前）
  - S2 spec（含 T2 协议调研 + 计费对账 spike + curl 实测 4 模型）2d
  - S3 plan 0.3d
  - S4 编码（T1 代码 + wire 修正 + ResolvedRoute 扩展 + thinking 标志 migration）2d
  - S5 Playwright E2E（4 条关键路径）0.5d
  - S6 merge 0.2d
  - S7 dev 验证 0.5d
- **交付时间线**：2026-04-27 可进 S6 merge

## §3 技术可行性 [AI 内部]

### 现有功能复用与真相澄清

- **Response 端**：`ChatResponse.ReasoningContent` + `ChatChunk.ReasoningDelta` 已在 `types.go:148/164`，SSE 解析已覆盖 `delta.reasoning_content` 字段。**BUT（v2 修订）**：`adapter.go:213-217` 的 `oaiUsage` 结构**没有** `reasoning_tokens` 字段，`TokenUsage.ReasoningTokens` 始终为 0，billing + Langfuse 对思考 token 都在吃暗账。本期必须加。
- **Dev DB 实测（2026-04-20 SSH 验证）**：AiHubMix 在 `ai_service_route` 有 8 条路由（4 base + 4 thinking 变体），priority=100。`ai_service` 的 thinking 标志存在错误值（见 §3.3）。
- **前端无改动**：`thinking` event 和渲染逻辑在 hotfix-default-thinking-mode 期间已就位。
- **Chatbot 已消费 `ReasoningDelta`**：`stream.go:191-195` 转发到前端，只等 adapter 产出内容。

### 技术路径

1. **aiservice 层新增字段**（Q1=A 决策）：`ChatRequest.Thinking bool`（简单开关）
2. **调用点修正**：
   - `executor.go:108` 函数签名已有 `thinking bool`，:109 的 `_ = thinking` 删除，:463 的 `ChatRequest{}` 加 `Thinking: thinking`
   - `chatbot/stream.go:177` 同上，:172 的 `gatewayReq` 加 `Thinking: thinking`
3. **Registry 扩展**（Q5=a 决策）：`ResolvedRoute` 加 `SupportsThinking bool` + `ThinkingOnly bool`，`registry/store.go` 的 JOIN SQL 补 `s.supports_thinking, s.thinking_only` 列，`resolvedRouteRow` 结构对齐，`registry/registry.go` 的 `ResolvedRoute` struct 新增两字段
4. **Adapter 请求构造**（`internal/pkg/aiservice/adapter/dmxapi.go`）：
   - **Adapter gating**：`req.Thinking == true && route.SupportsThinking == true` → 注入 `reasoning_effort="medium"`（ThinkingOnly=true 的模型已"原生思考"，也接受 reasoning_effort 作为强度调节，不区分）
   - **Per-model family 分派**（独立于 thinking gating）：
     - GPT family（prefix `gpt-5` / `gpt-5-` / `o1` / `o3` / `o4`）→ **总是** 用 `max_completion_tokens`，而非 `max_tokens`（P1-1 修订，无论 thinking 开关都生效，否则 thinking=false 也会 400）
     - Claude base（`claude-sonnet-4-6` 且非 `-think` 后缀）+ `req.Thinking == true` → **强制 `temperature=1`**（Q4=A 决策，对齐 `-think` 变体行为，避免两个入口产出差异）
     - 其他模型 → 保持 caller temperature，按标准 OpenAI 格式
   - helper：`inferModelFamily(providerModelID string) ModelFamily` 用前缀匹配分派，有单测
5. **oaiChatRequest struct 扩展**（`adapter.go:137-150`）：增加
   - `ReasoningEffort string `json:"reasoning_effort,omitempty"``
   - `MaxCompletionTokens int `json:"max_completion_tokens,omitempty"``
   （`MaxTokens` 保留，adapter 层按 model family 选择填充哪个）
6. **oaiUsage wire 修正**（P0-1 修订）：`adapter.go:213-217` 增加
   - `ReasoningTokens int `json:"reasoning_tokens,omitempty"``
   - **⚠️ T2 须实测 AiHubMix 响应字段路径**：OpenAI 原生是 `usage.completion_tokens_details.reasoning_tokens`（嵌套），但 DeepSeek/Qwen 派系是 `usage.reasoning_tokens`（平级）。Gemini 和 Claude via AiHubMix 的字段形态未知——T2 必须 curl 取 raw JSON 验证。若形态不一，要按 provider 分派解码
   - 透传到 `aiservice.TokenUsage.ReasoningTokens`（`dmxapi.Chat` + `runOAIStream` 都要改）
7. **DB 标志校正 migration**（T2 审计结果驱动）：
   - `migrations/20260421_XXXXXX_fix_ai_service_thinking_flags.sql`
   - 根据 T2 实测结果（base 模型接受 reasoning_effort 且可切换）修正：
     - Claude base（id=1）：保持 (1, 0) optional ✅
     - Claude thinking 变体（id=5）：(0, 0) → **(1, 1)** intrinsic
     - Gemini base（id=12）：(1, 1) → **(1, 0)** optional（T2 验证后如果 Gemini 不加 reasoning_effort 时也思考，就不改）
     - DeepSeek base（id=13）：(1, 1) → **(1, 0)** optional（同上待 T2 验证）
     - GPT 5.4 base（id=14）：(1, 1) → **(1, 0)** optional（GPT 明确支持 reasoning_effort 切换）
     - Gemini/DeepSeek/GPT thinking 变体（id=15/16/17）：(0, 0) → **(1, 1)** intrinsic
   - **注意**：最终修正值依赖 T2 per-model 实测产出的 `is reasoning_effort necessary to thinking？` 矩阵，S2 在 spec 锁定 migration 内容
8. **Langfuse tracing**（P1-4 修订，方案 b）：扩展 `middleware/tracing.go`——generation.input 除 `ChatRequest` 原字段外，额外记录 adapter 层解析出的 `resolved_reasoning_effort`（空或 medium）和 `resolved_model_family`，方便 trace 侧分析

### 技术风险

- **GPT 5.4 加密推理**：已知且可接受。产品层面接受 "GPT 5.4 无 chain-of-thought 可视"，用 `reasoning_tokens` 证明思考发生
- **Claude 两入口温度一致性**（Q4=A 决策）：Claude base + Thinking=true 强制 temp=1，与 `-think` 变体对齐。若用户期望低温创意性（temp=0.3）+ thinking，会被静默覆盖——T2 协议 doc 必须明文说明
- **oaiUsage reasoning_tokens 字段路径不确定**（v2 新增）：T2 必 curl 取 raw response，万一 4 个 provider 字段形态不一，adapter 解码要按 provider 分派（风险：增加 T2 时间，但不阻塞架构）
- **计费对账风险**（v2 新增 spike）：如果 AiHubMix 账单把 reasoning_tokens 独立计价（而非并入 completion），current pricing_rule 结构可能低估成本。T2 spike 的产出会决定是否 S2 加 pricing_rule migration
- **reasoning_effort 值硬编码 medium**：本期固定，暴露给用户/admin 调节留到未来 openrouter-provider 重启。Tech debt 登记，不解决
- **DB 标志修正风险**（v2 新增）：修正 `thinking_only` 可能影响管理端 UI 的 "3 种形态筛选"展示（若有）。S2 必 grep admin-web 和 admin API 对这两字段的读取点

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3（零改动）
- [ ] numind-admin-web（零改动；但 S2 要 grep admin 侧对 supports_thinking/thinking_only 的展示点，确认标志修正不破坏 UI）

### AI 可观测性

- [x] 涉及 LLM 调用：是
- **Trace 起点**：
  - SOP：`biz/sop/executor.go:executeViaGateway`（已有 trace，复用）
  - Chatbot：`biz/chatbot/stream.go:ChatStream`（已有 trace，复用）
- **Generation 点**：`aiservice.ChatStream` 内的 adapter 调用（由 middleware 层自动记录），每次生成包含：
  - `model`（provider_model_id 实际送给 AiHubMix 的 slug）
  - `input.thinking`（本次是否启用思考，来自 ChatRequest.Thinking）
  - `input.resolved_reasoning_effort`（adapter 最终注入值，空或 "medium"；P1-4 方案 b 加在 tracing middleware）
  - `input.resolved_model_family`（adapter.inferModelFamily 推断结果）
  - `output.reasoning_content`（思维链文本，Claude/Gemini/DeepSeek 有，GPT 5.4 空）
  - `usage.promptTokens` / `completionTokens` / `reasoningTokens`（v2 新增 wire 解码后可信）
- **关键元数据**：`user_id`（已有）+ `sop_id` / `chatbot_session_id`（已有）。本期不新增 trace tag

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为**付费用户**，我使用 SOP 或 Chatbot 时，深度思考默认打开，我**真的能看到 AI 在思考的过程**（Claude/Gemini/DeepSeek 4 个选项里 3 个展示 chain-of-thought，GPT 5.4 因厂商限制不显示思考内容但消耗的算力体现在计费上）
- 作为**管理员**，我在 Langfuse 面板上能按 trace 过滤到"是否启用思考 + 哪个 model family + 用了哪档 reasoning_effort"，对比思考 vs 普通模式的成本质量差异
- 作为**财务**，月末 credits 消耗报告能准确反映 reasoning_tokens 的成本，不再把思考算力作为隐藏成本

### 验收标准（v2 修订）

- [ ] `go test ./internal/pkg/aiservice/adapter/... -v` 通过（新增 ≥ 8 个单测：4 model family × 2 thinking 开关，覆盖 P1-1 边界）
- [ ] `go test ./internal/pkg/aiservice/registry/... -v` 通过（ResolvedRoute.SupportsThinking/ThinkingOnly 从 DB 正确回读）
- [ ] Adapter 单测用 fake HTTP server 验证 wire-level 行为（P1-2 修订）：
  - Thinking=true + Claude base → 出站 body 含 `"reasoning_effort":"medium"` 且 `"temperature":1`
  - Thinking=true + GPT 5.4 → 出站 body 含 `"reasoning_effort":"medium"` 且 `"max_completion_tokens":N`，不含 `max_tokens`
  - Thinking=false + GPT 5.4 → 出站 body 不含 `reasoning_effort`，但**仍**含 `max_completion_tokens`（P1-1 回归保护）
  - Thinking=true + 某不支持 thinking 的模型（如 qwen-turbo via dmxapi 路由）→ 出站 body 不含 `reasoning_effort`
- [ ] curl 实测 4 个 AiHubMix 模型 base slug + Thinking=true（文档在 T2），观察到：
  - Claude：`message.reasoning_content` 非空
  - Gemini：`message.reasoning_content` 非空 + `usage.reasoning_tokens > 0`
  - DeepSeek：同上
  - GPT 5.4：`message.reasoning_content` 可空 + `usage.reasoning_tokens > 0`
- [ ] `executor.go` 和 `chatbot/stream.go` **出站 HTTP body** 在 Thinking=true 时含 `reasoning_effort`（不是 grep `_ = thinking`，是行为断言，P1-2 修订）
- [ ] `docs/aihubmix-protocol-reference.md` 存在且覆盖 4 个模型 × 6 个维度（endpoint / request / response / SSE / reasoning_tokens wire path / 坑）
- [ ] `docs/aihubmix-billing-reconciliation-spike.md` 存在（Q6=A 决策，记录 reasoning token 计价实测）
- [ ] Playwright E2E 4 条关键路径（见 §5）
- [ ] Langfuse trace 可见：`generation.input.thinking=true` + `generation.input.resolved_reasoning_effort=medium` + `generation.usage.reasoning_tokens` 字段记录正确（至少 1 个 model family 验证）
- [ ] DB 标志修正 migration 产出 `ai_service` 正确状态（2 rollback 文件齐全），且 `task lint` 对 store/admin 相关测试通过

### 边界情况

- **用户偏好 `thinking=false`**：SOP/Chatbot 走普通路径，adapter 不注入 `reasoning_effort`。但 GPT 5.4 **仍**用 `max_completion_tokens`（P1-1 修订）
- **路由 `supports_thinking=false` 的模型** + 用户 pref thinking=true：adapter 忽略 pref，走普通路径（避免向不支持 thinking 的模型发 reasoning_effort 触发 400）。典型场景：qwen-turbo via dmxapi
- **路由 `thinking_only=true` 的模型**（Claude `-think` 变体修正后）：adapter **仍**可注入 reasoning_effort 作为强度调节（AiHubMix 接受），也可不注入——本期选择**不注入**（slug 本身已激活 thinking，避免双重指令）
- **GPT 5.4 用 `max_tokens`**：AiHubMix 会返回 400；本期 adapter 对 GPT family **总是**强制 `max_completion_tokens`（P1-1 修订）
- **Claude base + Thinking=true + caller temp≠1**：adapter **静默覆盖为 temp=1**（Q4=A 决策），T2 doc 会明文说明这个行为
- **并发安全**：adapter 无共享可变状态，per-request 构造，并发无风险

### 权限规则

- 与现有 SOP/Chatbot 权限完全一致，本 feature 不改权限模型
- credits 制用户：思考模式消耗 `reasoning_tokens`，`computeTokens = prompt + completion + reasoning` 累计。本期确保 wire 解码正确（P0-1 修订），但**不**改 `pricing_rule`（Q6=A spike 产出后若有成本偏差，记 tech debt 或 follow-up hotfix）

### UI 行为规格

- **前端零改动**：hotfix-default-thinking-mode 已把默认 thinking=ON 铺到前端 store，ModelSelector 下拉已过滤 -thinking 变体，本 feature 不触碰任何 .vue
- **用户可见差异**：Claude/Gemini/DeepSeek 思考展示从"空白"变为"有实质思考文本"；GPT 5.4 展示不变（仍空白，tooltip 可后续 feature 加说明）

## §5 S5 验证策略（NDF §10 要求）

**选择**：**Playwright E2E**（Q3=A 决策）

**关键用户路径（v2 修订为 4 条）**：
1. 登录 → 打开 Chatbot → 选 Claude 4.6 → 发消息"用三步推导斐波那契第 10 项" → 收到至少 1 个 `thinking` 事件 + content 非空
2. 登录 → 打开 SOP 运行某模板（内置思考节点）→ 执行 → 收到 thinking SSE 事件
3. 登录 → 切换到 GPT 5.4 → 发消息 → 确认 content 非空 + 前端不报错（reasoning_content 空是预期）
4. **（P1-3 新增）**登录 → 切换到非 thinking 模型（如 qwen-turbo via ali-dashscope 主路由）→ 发消息 → 确认**不**收到任何 thinking 事件 + 请求 200 成功（证明 adapter 正确 skip reasoning_effort 注入）

**后端单测覆盖**：
- `oaiChatRequest` 构造函数 4 个 model family × 2 thinking 开关 = 8 单测（P1-1 回归保护）
- `inferModelFamily()` helper 对 `gpt-5`、`gpt-5.4`、`gpt-5-preview`、`o1-preview`、`o3-mini`、`o4-turbo` 全部识别为 openai-encrypted-reasoning
- `max_completion_tokens` vs `max_tokens` 分派（基于 family，不基于 thinking）
- `supports_thinking=false` 时 adapter 忽略 Thinking flag
- `registry.ResolvedRoute.SupportsThinking/ThinkingOnly` 从 DB 正确回读

## §6 代码改动清单（v2 修订）

**新增文件（3）**：
- `docs/aihubmix-protocol-reference.md` — T2 协议权威手册
- `docs/aihubmix-billing-reconciliation-spike.md` — T2 计费对账 spike（Q6=A）
- `migrations/20260421_XXXXXX_fix_ai_service_thinking_flags.sql` + rollback — 标志校正

**修改文件（~9）**：
- `internal/pkg/aiservice/types.go` — `ChatRequest` 加 `Thinking bool`
- `internal/pkg/aiservice/adapter/adapter.go`
  - `oaiChatRequest` 加 `ReasoningEffort` / `MaxCompletionTokens` 字段
  - `oaiUsage` 加 `ReasoningTokens` 字段（P0-1 修订）
- `internal/pkg/aiservice/adapter/dmxapi.go` — Chat/ChatStream 按 per-model 规则构造 + 注入 reasoning_effort + 温度覆盖 + `inferModelFamily` helper
- `internal/pkg/aiservice/adapter/stream.go` — `runOAIStream` 透传 `reasoning_tokens` 到 `aiservice.TokenUsage.ReasoningTokens`（P0-1 修订）
- `internal/pkg/aiservice/registry/registry.go` — `ResolvedRoute` 加 `SupportsThinking` / `ThinkingOnly`（Q5=a）
- `internal/pkg/aiservice/registry/store.go` — SQL JOIN 多选 `s.supports_thinking, s.thinking_only`；`resolvedRouteRow` 对齐
- `internal/pkg/aiservice/middleware/tracing.go` — generation input 追加 `resolved_reasoning_effort` + `resolved_model_family`（P1-4 方案 b）
- `internal/numind/biz/sop/executor.go:108-113, 463-467` — 删 `_ = thinking`，`ChatRequest` 加 `Thinking: thinking`
- `internal/numind/biz/chatbot/stream.go:172-177` — 同上

**新增测试**：约 12 个单测（adapter 8 + registry 2 + stream 2）+ 1 条新 Playwright 路径（P1-3）。

## §7 遗留 S2 细化点（v2 修订）

1. **`oaiUsage.ReasoningTokens` wire 字段路径分派**：T2 curl 实测 4 个 provider 的 `usage` JSON 形态，如 OpenAI 嵌套 `usage.completion_tokens_details.reasoning_tokens` vs DeepSeek/Qwen 派系平级 `usage.reasoning_tokens`。S2 在 spec 锁定解码策略（全局分派 vs per-provider 分派）
2. **计费对账 spike 的具体方法学**：发 100 个 reasoning token，如何测量 AiHubMix 账单扣费？调用 `/v1/dashboard/billing/usage` 端点？还是人工查账户面板？S2 锁定步骤
3. **DB 标志修正的最终值**：§3.3 给出初步 mapping，但 Gemini/DeepSeek base 是否**必须**加 reasoning_effort 才思考、还是**可选**，需 T2 curl 分别测 `带 vs 不带 reasoning_effort`，才能敲定最终 migration 内容
4. **AiHubMix 对 reasoning_effort 的兜底容忍**：如果某天 AiHubMix 废弃 `reasoning_effort` 参数返回 400，adapter 是否自动剥掉重试？本期倾向**不加**（NDF §3 "不要添加错误处理，当场景不存在"），让错误暴露。S2 Opus reviewer 确认
5. **未来 openrouter-provider 的迁移路径**：如果将来恢复 OpenRouter feature，`SupportsThinking`/`ThinkingOnly` 基础设施本期已铺好，`oaiChatRequest.ReasoningEffort` 也已存在——只需补 OpenRouter 的 `delta.reasoning` SSE 字段解析（和 `delta.reasoning_content` 并列）。记入 §8 tech debt
