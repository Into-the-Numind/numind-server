# AiHubMix 协议权威参考

> **来源**：2026-04-20 协议审计，针对 `aihubmix-protocol-audit` feature。
> **测试方法**：真实 curl 调用 `https://aihubmix.com/v1/chat/completions`，4 个 provider_model × 3 场景 + `-think` 变体 + 边界探针。
> **原始响应**：`/tmp/aihubmix-audit/*.json` 和 `/tmp/aihubmix-audit/*.out`（审计机本地，commit 前已拷入本文档 §7）。
> **读者**：写下一版 `internal/pkg/aiservice/adapter/dmxapi.go` 的 AI/人。所有声明必须能用 curl 重放验证。
> **生命周期**：AiHubMix 协议会演进。本文是 2026-04-20 快照，若行为出现偏差，以实测为准并回灌。

---

## 1. 通用约定

- **Endpoint**：`POST https://aihubmix.com/v1/chat/completions`
- **Auth header**：`Authorization: Bearer sk-...`（OpenAI 兼容）
- **Content-Type**：`application/json`
- **协议形态**：OpenAI Chat Completions 兼容层 —— 上游是 AiHubMix 对 Anthropic / Google / DeepSeek / OpenAI 做的多 provider 反向代理。不同底层 provider 在 reasoning_content 的字段命名、`usage.*_tokens` 的层级、以及 `reasoning_effort` 的语义上存在**不可忽略的差异**。不能假设"都是 OpenAI 格式就通用"。
- **错误体**：
  - OpenAI 风格（来自 GPT 路径）：`{"error":{"message":..., "type":"invalid_request_error", "param":..., "code":"unsupported_parameter"}}`
  - AiHubMix 自有风格（模型不存在或无权限）：`{"error":{"message":"Incorrect model ID. Please request... (tid: ...)","type":"Aihubmix_api_error"}}`
  - Gemini thinking level 拒绝：`{"error":{"message":"Thinking level MINIMAL is not supported for this model...","type":"","param":"","code":400}}`
- **tid**：AiHubMix 的 trace id（错误消息尾部），排查时可提供给 AiHubMix 客服定位。
- **响应模型名回显**：有时与请求一致（`claude-sonnet-4-6`, `deepseek-v3.2`），有时被上游展开（GPT 返回 `gpt-5.4-2026-03-05`，Gemini 流式返回 `gemini`，DeepSeek thinking 回显 `DeepSeek-V3.2`）—— **不要用响应里的 model 字段做计费 key，以请求的 provider_model_id 为准**。

---

## 2. 各模型调用矩阵

标号约定：
- **S1** = baseline（只有 messages + max_tokens/max_completion_tokens）
- **S2** = S1 + `reasoning_effort: "medium"`
- **S3** = S2 + `stream: true` + `stream_options.include_usage: true`

测试 prompt 固定为 `"What is 17 × 23? Show your reasoning step by step."`（中文字符已从此版本去除，完全 ASCII，避免 tokenizer 偏差）。

| # | Model | Scenario | HTTP | 请求 token 字段 | `message.reasoning_content` | `message.reasoning_details` | `usage.*reasoning_tokens` 路径 | 备注 |
|---|-------|----------|------|------|----|----|----|------|
| 1 | `claude-sonnet-4-6` | S1 | 200 | `max_tokens` | 无 | 无 | **不存在** | 纯 content 输出。latency ~3.4s。 |
| 2 | `claude-sonnet-4-6` | S2 | 200 | `max_tokens` | **有**（明文短摘要） | **有**（`{type:thinking, thinking:..., signature:base64}`） | **不存在** | reasoning 计入 `completion_tokens`（156→210）。`usage` 只有 `claude_cache_tokens_details`，无 reasoning_tokens 字段。latency ~4.0s。 |
| 3 | `claude-sonnet-4-6` | S3 | 200 | `max_tokens` | **delta.reasoning_content** 有 | **delta.reasoning_details** 有 | final chunk usage 中**不存在** | 流式 chunk 独立携带 `reasoning_details`（含 thinking 明文和 signature）。14 chunks。latency ~4.6s。 |
| 4 | `gemini-3.1-pro-preview` | S1 | 200 | `max_tokens` | **有**（多段 markdown thinking） | **有**（`{thoughtSignature, type:thinking}`） | `usage.completion_tokens_details.reasoning_tokens` = 479 | **即使不传 reasoning_effort，模型固定思考**。常撞 max_tokens=500 被截断（finish_reason=length），必须把 max_tokens 给到 1500+ 才能完成一次含完整答案的响应。latency ~9.1s。 |
| 5 | `gemini-3.1-pro-preview` | S2 | 200 | `max_tokens` | **有** | **有** | `usage.completion_tokens_details.reasoning_tokens` = 476 | 与 S1 行为几乎一致（thinking 是 intrinsic 的），传 reasoning_effort=medium 不改变本质，只是 AiHubMix 转写成 `thinkingConfig.thinkingLevel=MEDIUM`（猜测）。latency ~10.0s。 |
| 6 | `gemini-3.1-pro-preview` | S3 | 200 | `max_tokens` | **delta.reasoning_content** 有 | **delta.reasoning_details** 有 | final chunk `usage.completion_tokens_details.reasoning_tokens` = 476 | 仅 4 chunks（模型批量吐出大段 reasoning_content，然后一次性吐 content）—— **不是逐 token 流式**，更像是 batching。每个中间 chunk 的 `usage` 都是 0；最终 chunk `choices=[]` 带 usage。latency ~5.6s。 |
| 7 | `deepseek-v3.2` | S1 | 200 | `max_tokens` | 无 | 无 | `usage.prompt_tokens_details.cached_tokens` 有，但 `completion_tokens_details` **不存在** | 无思考；纯 content。latency ~6.3s。 |
| 8 | `deepseek-v3.2` | S2 | 200 | `max_tokens` | **有**（长段明文 CoT） | 无 | `usage.completion_tokens_details.reasoning_tokens` = 190 | **`reasoning_effort` 开关是真实生效的**：S1→无 reasoning_content，S2→有 reasoning_content + reasoning_tokens。响应 model 字段回显为 `DeepSeek-V3.2`（大小写变化，别用来做 key）。latency ~11.3s。 |
| 9 | `deepseek-v3.2` | S3 | 200 | `max_tokens` | **delta.reasoning_content** 逐 token 流式 | 无 | final chunk `usage.completion_tokens_details.reasoning_tokens` = 179 | **77 chunks，真逐 token 流式**（先全部 reasoning_content chunks，然后 content chunks，然后 finish+usage）。latency ~10.5s。 |
| 10 | `gpt-5.4` | S1 | 200 | **`max_completion_tokens`**（`max_tokens` 会 400） | 无 | 无 | `usage.completion_tokens_details.reasoning_tokens` = **0** | 默认不思考。latency ~2.4s。 |
| 11 | `gpt-5.4` | S2 | 200 | `max_completion_tokens` | **无**（reasoning_content 字段**根本不返回**） | 无 | `usage.completion_tokens_details.reasoning_tokens` = 66 | **坑**：GPT 5.4 思考链被 OpenAI 上游完全封装，AiHubMix 也拿不到明文，**只能看到 reasoning_tokens 计数**。无法把 CoT 展示给用户。latency ~3.5s。 |
| 12 | `gpt-5.4` | S3 | 200 | `max_completion_tokens` | **无 reasoning_content chunk** | 无 | final chunk `usage.completion_tokens_details.reasoning_tokens` = 42 | 流式 77 chunks，只有 `delta.content` 和 `delta.role`。**没有任何 reasoning 相关字段**。latency ~2.8s。 |

### 2.1 `-think` suffix 变体测试

| Model | 请求 | HTTP | 结果 |
|-------|------|------|------|
| `claude-sonnet-4-6-think` | 无 reasoning_effort | 200 | 响应包含 `reasoning_content` + `reasoning_details`（thinking 块）。`temperature` 传入不报错（AiHubMix 侧可能已忽略/固定 1，详见 §6）。相当于 claude base + 永久开启 thinking。latency ~4.0s。 |
| `gemini-3.1-pro-preview-think` | 无 reasoning_effort | **400** | `"Incorrect model ID. Please request to view the model page or you do not have permission to use this model gemini-3.1-pro-preview-think"` —— **此变体在 AiHubMix 不存在**。Gemini 不需要 -think，因为 base 本身就 intrinsic thinking。 |
| `deepseek-v3.2-think` | 无 reasoning_effort | 200 | 响应包含 `reasoning_content`，`reasoning_tokens=201`。相当于 deepseek base + 永久开启 thinking。latency ~10.9s。 |
| `gpt-5.4-think` | 无 reasoning_effort | **400** | `"Incorrect model ID. ... gpt-5.4-think"` —— **此变体在 AiHubMix 不存在**。GPT 5.4 只能靠 base model + `reasoning_effort` 开关调节 reasoning 深度，**没有开/关维度**。 |

**结论：AiHubMix 侧存在的 -think 变体仅为 `claude-sonnet-4-6-think` 和 `deepseek-v3.2-think`。Gemini 和 GPT 的 -think 变体不存在（400 Incorrect model ID）。**

---

## 3. 请求字段分派规则

### 3.1 Max tokens 字段

| Model | 正确字段 | 错误字段行为 |
|-------|---------|-------------|
| `claude-sonnet-4-6` (base & -think) | `max_tokens` | — |
| `gemini-3.1-pro-preview` | `max_tokens` | — |
| `deepseek-v3.2` (base & -think) | `max_tokens` | — |
| `gpt-5.4` | **`max_completion_tokens`** | 传 `max_tokens` 返回 **400 `invalid_request_error`**：<br>`"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."`（`param: "max_tokens"`, `code: "unsupported_parameter"`）<br>说明 AiHubMix 把 GPT 5.4 请求完全透传给 OpenAI；OpenAI 的 reasoning model family（o-series + GPT 5.x）要求 `max_completion_tokens`。 |

**实现建议**：按 provider_model_id 前缀（或数据库 `ai_model.token_param_field`）分派。Adapter 层必须能感知"该走 max_tokens 还是 max_completion_tokens"。

### 3.2 `reasoning_effort` 的语义

| Model | 接受的 effort 值 | effort=none/minimal 是否可用 | 说明 |
|-------|-----------------|----------------------------|------|
| `claude-sonnet-4-6` (base) | `none`, `low`, `medium`, `high` | **是**，`none` 完全关闭 thinking（无 reasoning_content，不计 reasoning token） | 上游 Anthropic extended thinking 开关；`none` 等价于不开启 thinking。 |
| `claude-sonnet-4-6-think` | — | — | 实测：即使不传 reasoning_effort 也输出 reasoning_content。**该变体天生 on，不测试能否关闭**。 |
| `gemini-3.1-pro-preview` | `low`, `medium`, `high` | **否**，`none`/`minimal` 会返 **400** `"Thinking level MINIMAL is not supported for this model"` | Gemini 3.1 Pro Preview **无法关闭 thinking**。即使传 effort=low，reasoning_tokens 仍大几十~一百（实测 120）。传 `medium` 也依然常吃满 476 tokens。如果不想付 reasoning 费，请换 base 非 preview 模型。 |
| `deepseek-v3.2` (base) | `none`, `low`, `medium`, `high` | **是**，`none` 关闭 thinking（无 reasoning_content，无 reasoning_tokens） | 真正的 open/close 开关。base 模型 默认也不开（S1 无 reasoning_tokens）。 |
| `deepseek-v3.2-think` | — | — | 实测：不传 reasoning_effort 也有 reasoning_content + reasoning_tokens=201。**该变体天生 on**。 |
| `gpt-5.4` | `minimal`, `low`, `medium`, `high` | **`minimal` 可用**（实测 reasoning_tokens=0） | GPT 5.4 base 默认等价 `minimal`（不思考）。传 `medium`/`high` 开启内部思考；但**CoT 明文永远不回传**，只能看 reasoning_tokens 数。 |

### 3.3 `temperature` 行为

所有被测模型在审计时均**接受 `temperature` 参数不报错**，但不代表底层真的会生效：

| Model | temperature=0.0/0.3 | temperature=2.0 | 备注 |
|-------|-------|-----|-----|
| `claude-sonnet-4-6` | 200 OK | 未测 | 应该按传入值生效。 |
| `claude-sonnet-4-6-think` | 200 OK | 未测 | AiHubMix 官网文档历史上声明 thinking 模型会强制 temperature=1。**本次未看到 400**，但不代表 AiHubMix 不在内部强制固定。保险做法：调用方主动把 temperature 固定为 1，不依赖上游静默处理。 |
| `gemini-3.1-pro-preview` | 200 OK | 未测 | thinkingConfig 模型 Google 侧一般固定 temperature=1；AiHubMix 可能静默吞掉。 |
| `deepseek-v3.2` | 200 OK（未单独探针，S2 未传 temp 也 OK） | 未测 | 可传，应生效。 |
| `gpt-5.4` | **temperature=0.3 200 OK，temperature=2.0 200 OK** | 200 OK | **惊喜**：与之前 o-series 模型不同，GPT 5.4 **接受任意 temperature 不报 400**。但"接受"≠"生效"——OpenAI 新一代 reasoning model 可能内部也忽略。 |

**实现建议**：对 **thinking 模型（`*-think` 变体、Gemini preview、GPT 5.4 开 reasoning_effort 的情况）保持 temperature=1**（或不传），避免偶发行为变化；对 base 非 thinking 模式按调用方传的 temperature 转发即可。

### 3.4 必要的 stream_options

- 流式调用必须显式传 `"stream_options": {"include_usage": true}`，否则 **final chunk 不会带 `usage`**，billing 无法拿到 token 数。
- 实测四个模型都接受此字段；未传时的行为未专门测（但 OpenAI 兼容协议是默认不返回 usage）。

---

## 4. SSE 流式协议

### 4.1 chunk 总体结构

```
data: {"id":..., "object":"chat.completion.chunk", "created":..., "model":..., "choices":[{"index":0, "delta":{...}, "finish_reason":null|"stop"|"length"}]}
...
data: {"id":..., "object":"", "created":..., "model":..., "choices":[], "usage":{...}}
data: [DONE]
```

其中：
- 中间 chunks：`choices` 非空、`delta` 非空、`usage` 缺失（或全 0）
- 最终 chunk：`choices=[]`、带 `usage`、**注意 GPT 的最终 chunk `object` 仍是 `chat.completion.chunk`，但 Claude 的最终 chunk `object` 是空字符串 `""`（见 §7 chunks 样本）**
- `data: [DONE]` 是终止哨兵

### 4.2 delta 字段地图（这张表是解析器的根本）

| Model | `delta.role` | `delta.content` | `delta.reasoning_content` | `delta.reasoning_details` | 流式粒度 |
|-------|--------------|-----------------|--------------------------|--------------------------|---------|
| `claude-sonnet-4-6` (S3) | 首 chunk 有 | 逐 token 拼接 | **有**（单次 or 多次，都是整段 thinking） | **有**（含 `type:thinking`, `thinking:...`, `signature:...`） | 14 chunks，thinking 部分往往整段一次性到达，content 逐 token |
| `gemini-3.1-pro-preview` (S3) | — | 有，整段一次性到达 | **有**（分 2-3 段整段的 reasoning_content，每次几百 token） | **有**（含 `thoughtSignature`, `type:thinking`） | **4 chunks total**（几乎是"伪流式"，批量吐出，不适合实时 CoT 渲染） |
| `deepseek-v3.2` (S3) | 首 chunk 有 | 逐 token | **有**（逐 token） | 无 | 77 chunks，**真逐 token 流式**；先全部 reasoning_content chunks，再 content chunks |
| `gpt-5.4` (S3) | 首 chunk 有 | 逐 token | **完全没有** | 无 | 77 chunks，只有 content，**reasoning 完全不可见，仅终 chunk 的 usage.reasoning_tokens** |

### 4.3 关于 `delta.reasoning` vs `delta.reasoning_content`

**实测四个模型都使用 `reasoning_content`，没有任何一个返回 `reasoning` 字段**。旧版 OpenAI o1 的 SDK 文档曾用过 `reasoning`，当前 AiHubMix 统一到 `reasoning_content`（对齐 DeepSeek 命名）。下一版解析器只需认 `reasoning_content` 这一个字段。

### 4.4 最终 chunk `usage` 差异

```jsonc
// Claude
{"usage": {
  "prompt_tokens": 23, "completion_tokens": 264, "total_tokens": 287,
  "claude_cache_tokens_details": {...}
  // 注意：没有 reasoning_tokens，没有 completion_tokens_details
}}

// Gemini
{"usage": {
  "prompt_tokens": 17, "completion_tokens": 496, "total_tokens": 513,
  "completion_tokens_details": {"reasoning_tokens": 476}
  // 注意：非流式时还有 prompt_tokens_details，流式时被简化掉
}}

// DeepSeek
{"usage": {
  "prompt_tokens": 19, "completion_tokens": 350, "total_tokens": 369,
  "prompt_tokens_details": {"cached_tokens": 0},
  "completion_tokens_details": {"reasoning_tokens": 179}
}}

// GPT 5.4
{"usage": {
  "prompt_tokens": 21, "completion_tokens": 126, "total_tokens": 147,
  "prompt_tokens_details": {"audio_tokens": 0, "cached_tokens": 0},
  "completion_tokens_details": {
    "accepted_prediction_tokens": 0, "audio_tokens": 0,
    "reasoning_tokens": 42, "rejected_prediction_tokens": 0
  }
}}
```

---

## 5. `reasoning_tokens` wire 路径总表

| Model | 路径 | 是否永远可拿到 |
|-------|------|--------------|
| `claude-sonnet-4-6` (base & -think) | **无此字段**（reasoning 已被合并进 `completion_tokens`） | **否**。要单独计算 reasoning 成本，需要 `len(reasoning_content) / avg_chars_per_token` 做估算，或承认"Claude reasoning = completion 同价"。 |
| `gemini-3.1-pro-preview` | `usage.completion_tokens_details.reasoning_tokens` | 是（含流式终 chunk） |
| `deepseek-v3.2` (base & -think) | `usage.completion_tokens_details.reasoning_tokens` | 开启 reasoning 时是；S1 baseline（未开 reasoning_effort）字段整体缺失 |
| `gpt-5.4` | `usage.completion_tokens_details.reasoning_tokens` | 是（开/关都会回，关闭时=0） |

**结论：Billing 层写 token 读取逻辑时，路径统一为 `usage.completion_tokens_details.reasoning_tokens`（nested OpenAI 风格），Claude 单独特判返回 0（或用字符数估算）。不要尝试读 `usage.reasoning_tokens`（flat）—— 无模型走这条路径。**

---

## 6. 已知坑

### 6.1 400 错误清单（已复现）

| 触发条件 | 错误消息 | 规避 |
|---------|---------|------|
| `gpt-5.4` 传 `max_tokens` | `Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.` (`invalid_request_error`/`unsupported_parameter`) | adapter 按模型分派 token 字段 |
| `gemini-3.1-pro-preview` 传 `reasoning_effort: "minimal"` 或 `"none"` | `Thinking level MINIMAL is not supported for this model. Please retry with other thinking level.` | 对 gemini preview 模型：要么不传 reasoning_effort（等价 medium），要么传 low/medium/high，**绝不传 none/minimal** |
| 请求 `gemini-3.1-pro-preview-think` | `Incorrect model ID. Please request to view the model page or you do not have permission...` (`Aihubmix_api_error`) | 此变体不存在，不要在 DB 中添加 |
| 请求 `gpt-5.4-think` | `Incorrect model ID. ...` | 此变体不存在 |

### 6.2 非 400 的隐性陷阱

1. **Gemini preview 常撞 max_tokens 被 truncated**：`finish_reason="length"`，`completion_tokens=max_tokens-ish`，其中绝大部分是 reasoning。调用方必须给 gemini 足够 headroom（建议 ≥1500 tokens），否则 content 字段可能只有"The answer is 391"一句、reasoning_content 大几百字。
2. **Claude 的 reasoning_tokens 没有入口**：billing 如果想按 reasoning_tokens 单独计价（与 completion_tokens 不同价），Claude 这条路不通。当前 pricing_rules 对 Claude 的 `reasoning_rate` 无效。建议用 "Claude reasoning == completion 同价" 或在 pricing_rules 里显式把 Claude 的 reasoning_rate = completion_rate。
3. **Gemini 流式是伪流式**：4 个 chunks 里有 3 个是大段 reasoning_content 整段到达。前端如果想实现"边思考边打字"的逐字渲染，Gemini preview 做不到（需 fallback 到 non-stream + 客户端模拟）。
4. **DeepSeek 响应 model 字段大小写抖动**：非流式 reasoning 场景返回 `"model": "DeepSeek-V3.2"`（capitalized），其他场景 `"model": "deepseek-v3.2"`。不要用响应里的 model 做计费 key —— 用请求里的 provider_model_id。
5. **GPT 5.4 CoT 对外完全不可见**：想给用户展示 CoT 的功能，GPT 5.4 无法实现（不是 dmxapi.go 的 bug，是 OpenAI 的策略）。产品文档里不要承诺"GPT 5.4 展示思考过程"。
6. **Claude `reasoning_details.signature`**：流式情况下 reasoning_details 可能分多个 chunk 出现，第一个 chunk 只有 `{type:thinking}`，后续 chunk 才携带 `thinking` 明文和 base64 `signature`。解析器要按 chunk 累积而非覆盖。
7. **GPT 5.4 temperature 行为变化**：与 o-series 不同，GPT 5.4 **不再 400 reject** temperature=0.3。但"不报错"不等于"按 0.3 生效"。如无强需求，thinking 模型一律 temperature=1 最稳。
8. **AiHubMix 错误响应无统一 schema**：OpenAI 风格（GPT）、Google 风格（Gemini thinking）、AiHubMix 自有风格（模型不存在）三种并存。`err.Error.Type` 字段可能是 `invalid_request_error` / `Aihubmix_api_error` / `""`。错误归一化时不能假设固定字段。

### 6.3 API key

审计用的 API key `sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68` 当前硬编码于 `migrations/20260416_100000_seed_aihubmix_provider.sql`（项目豁免，per migration 说明）。**不要在新代码中二次硬编码**，一律从 DB `ai_service.api_key` 读取（通过 `aiservice` 入口的 provider registry）。

---

## 7. 原始 raw JSON 样本

> 以下 12 个样本为 `/tmp/aihubmix-audit/` 目录的非流式响应及流式 chunks 汇总的精简版（长字符串截断标注 `…[truncated]` / `…`，用于阅读；完整原件在本地审计目录）。grep 友好，建议按 `--- <filename> ---` 标记定位。

### 7.1 非流式样本（S1 + S2，4 模型 × 2 场景）

```jsonc
--- claude-s1-baseline.json ---
{
  "id": "chatcmpl-msg_019n2U1hbs58kHj1pX1VC2Tw",
  "model": "claude-sonnet-4-6",
  "object": "chat.completion",
  "created": 1776683780,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "# Calculating 17 × 23\n\n## Method: Breaking Apart (Distributive Property)\n\nI'll split 23 into **20 + 3** to make the math easier:\n\n$$17 \\times 23 = 17 \\times (20 + 3)$$\n\n**Step 1:** Calculate 17 × 20\n$$17 \\times 20 = 340$$\n\n**Step 2:** Calculate 17 × 3\n$$17 \\times 3 = 51$$\n\n**Step 3:** Add the results\n$$340 + 51 = 391$$\n\n## Answer: **17 × 23 = 391**"
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 23, "completion_tokens": 156, "total_tokens": 179,
    "claude_cache_tokens_details": {
      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
      "cache_write_5_minutes_input_tokens": 0, "cache_write_1_hour_input_tokens": 0
    }
  }
}
```

```jsonc
--- claude-s2-medium.json ---
{
  "id": "chatcmpl-msg_01KCZCKAvMbEocAkwzfJAzMx",
  "model": "claude-sonnet-4-6",
  "object": "chat.completion",
  "created": 1776683808,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "## Calculating 17 × 23\n\n**Break apart one of the numbers (distributive property):**\n\n17 × 23 = 17 × (20 + 3)\n\n**Multiply each part:**\n\n- 17 × 20 = **340**\n- 17 × 3 = **51**\n\n**Add the results:**\n\n340 + 51 = **391**\n\n---\n\n**Verification using the standard algorithm:**\n```\n  17\n× 23\n----\n  51   ← (17 × 3)\n 340   ← (17 × 20)\n----\n 391\n```\n\n**17 × 23 = 391**",
      "reasoning_content": "17 × 23\n\n= 17 × 20 + 17 × 3\n= 340 + 51\n= 391",
      "reasoning_details": {
        "type": "thinking",
        "thinking": "17 × 23\n\n= 17 × 20 + 17 × 3\n= 340 + 51\n= 391",
        "signature": "EuwBClsIDBgCKkAXbWtiumMwXetQPjc1yfQ9hyr7ArD0Jy8Hr4HJpRaLo53JRFW4WzAI+Pc6U22zWEzZ…"
      }
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 23, "completion_tokens": 210, "total_tokens": 233,
    "claude_cache_tokens_details": {
      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
      "cache_write_5_minutes_input_tokens": 0, "cache_write_1_hour_input_tokens": 0
    }
  }
}
```

```jsonc
--- gemini-s1-baseline.json ---
{
  "id": "chatcmpl-d5b2e9aee2ef44c5b76f7d84f776c8f5",
  "model": "gemini-3.1-pro-preview",
  "object": "chat.completion",
  "created": 1776683789,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "Here is the step-by-step reasoning to solve 17 × 2",
      "reasoning_content": "**Analyzing Multiplication Strategies**\n\nI am currently exploring efficient methods to calculate 17 × 23. I've identified the difference of squares as a particularly elegant approach, leveraging the fact that both numbers are equidistant from 20. I'm also considering the distributive property as a standard alternative.",
      "reasoning_details": {
        "thoughtSignature": "CsQLAY89a1+qDwSYMO/B5M4RKYaE9pCPZ0o11S5huokhSw2OhgLNYXDmkw0QUjs5cEwMdqm6E0vUhZ5y…",
        "type": "thinking"
      }
    },
    "finish_reason": "length"
  }],
  "usage": {
    "prompt_tokens": 17, "completion_tokens": 496, "total_tokens": 513,
    "prompt_tokens_details": {"image_tokens": 0, "cached_tokens": 0},
    "completion_tokens_details": {"image_tokens": 0, "reasoning_tokens": 479}
  }
}
```

```jsonc
--- gemini-s2-medium.json ---
{
  "id": "chatcmpl-4e7aa2693fdb48979139fb5ea92b7633",
  "model": "gemini-3.1-pro-preview",
  "object": "chat.completion",
  "created": 1776683818,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "The answer is **391**. \n\nHere are two easy ways to break down the math",
      "reasoning_content": "**Exploring Multiplication Strategies**\n\nI've been considering different pathways to articulate the calculation of 17 multiplied by 23. My current focus is on demonstrating this using the elegant difference of squares property, which simplifies the mental arithmetic considerably by leveraging the proximity to 20.",
      "reasoning_details": {
        "thoughtSignature": "CsUKAY89a1+xhKDTNaK/SBffsIEuX8Tsc89ViX0PJPSmLrg84wsDlC/FW4dyfGc8gJqJrCw+stL53See…",
        "type": "thinking"
      }
    },
    "finish_reason": "length"
  }],
  "usage": {
    "prompt_tokens": 17, "completion_tokens": 496, "total_tokens": 513,
    "prompt_tokens_details": {"image_tokens": 0, "cached_tokens": 0},
    "completion_tokens_details": {"image_tokens": 0, "reasoning_tokens": 476}
  }
}
```

```jsonc
--- deepseek-s1-baseline.json ---
{
  "id": "chatcmpl-5cf75589-172d-9864-a61c-34815a0fffe7",
  "model": "deepseek-v3.2",
  "object": "chat.completion",
  "created": 1776683796,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "Alright, let's compute \\( 17 \\times 23 \\) step-by-step.\n\n---\n\n**Step 1: Break down \\( 23 \\)**\n\\( 23 = 20 + 3 \\)\n\nSo:\n\\[\n17 \\times 23 = 17 \\times (20 + 3)\n\\]\n\n---\n\n**Step 2: Use distributive property**\n\\[\n17 \\times 20 = 340\n\\]\n\\[\n17 \\times 3 = 51\n\\]\n\n---\n\n**Step 3: Add**\n\\[\n340 + 51 = 391\n\\]\n\n…[truncated]"
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 19, "completion_tokens": 191, "total_tokens": 210,
    "prompt_tokens_details": {"cached_tokens": 0}
    // NOTE: no completion_tokens_details (no reasoning_tokens) — S1 baseline without reasoning_effort
  }
}
```

```jsonc
--- deepseek-s2-medium.json ---
{
  "id": "chatcmpl-e57e139a-49e4-993d-8395-8bb8ebec882e",
  "model": "DeepSeek-V3.2",                             // NOTE case-shift when reasoning is on
  "object": "chat.completion",
  "created": 1776683830,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "To compute \\( 17 \\times 23 \\), we can use the distributive property by breaking 23 into \\( 20 + 3 \\). …",
      "reasoning_content": "We are asked: \"What is 17 × 23? Show your reasoning step by step.\" So we need to multiply 17 by 23 and show step-by-step reasoning.\n\nWe can compute 17 × 23 directly. Maybe break it down using distributive property or multiplication algorithm.\n\n17 × 23 = 17 × (20 + 3) = 17×20 + 17×3 = 340 + 51 = 391. …[truncated]"
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 19, "completion_tokens": 337, "total_tokens": 356,
    "prompt_tokens_details": {"cached_tokens": 0},
    "completion_tokens_details": {"reasoning_tokens": 190}
  }
}
```

```jsonc
--- gpt-s1-baseline.json ---
{
  "id": "chatcmpl-DWguC1MDNAun54t6blsXiJ71DRDH4",
  "model": "gpt-5.4-2026-03-05",                       // NOTE model version expanded by OpenAI
  "object": "chat.completion",
  "created": 1776683796,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "17 × 23 = 391\n\nStep by step:\n1. Break 23 into 20 + 3\n2. Compute 17 × 20 = 340\n3. Compute 17 × 3 = 51\n4. Add them: 340 + 51 = 391\n\nSo, the answer is 391."
    },
    "finish_reason": "stop"
  }],
  "service_tier": "default",
  "usage": {
    "prompt_tokens": 21, "completion_tokens": 74, "total_tokens": 95,
    "prompt_tokens_details": {"audio_tokens": 0, "cached_tokens": 0},
    "completion_tokens_details": {
      "accepted_prediction_tokens": 0, "audio_tokens": 0,
      "reasoning_tokens": 0,                           // NOTE present-but-zero, not missing
      "rejected_prediction_tokens": 0
    }
  }
}
```

```jsonc
--- gpt-s2-medium.json ---
{
  "id": "chatcmpl-DWguk7a8qipz2htrHm5mKeLqxEzuv",
  "model": "gpt-5.4-2026-03-05",
  "object": "chat.completion",
  "created": 1776683830,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "17 × 23 = 391\n\nStep by step:\n- 23 × 10 = 230\n- 23 × 7 = 161\n- 230 + 161 = 391\n\nSo, the answer is 391."
      // NOTE: NO reasoning_content — GPT 5.4 hides CoT even when reasoning_effort=medium
    },
    "finish_reason": "stop"
  }],
  "service_tier": "default",
  "usage": {
    "prompt_tokens": 21, "completion_tokens": 126, "total_tokens": 147,
    "prompt_tokens_details": {"audio_tokens": 0, "cached_tokens": 0},
    "completion_tokens_details": {
      "accepted_prediction_tokens": 0, "audio_tokens": 0,
      "reasoning_tokens": 66,                          // only signal that thinking happened
      "rejected_prediction_tokens": 0
    }
  }
}
```

### 7.2 流式样本（S3，4 模型各选代表性 chunks）

```jsonc
--- claude-s3-stream.json (14 chunks total, sample) ---
// chunk 0: role + empty reasoning_details placeholder
{"id":"chatcmpl-msg_013RzQLKt63Mt7Yv5RLH2N9R","object":"chat.completion.chunk","created":1776683859,"model":"claude-sonnet-4-6","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_details":{"type":"thinking"}}}],"system_fingerprint":"fp-msg_013RzQLKt63Mt7Yv5RLH2N9R"}
// chunk 1: first thinking content + signature begins (reasoning_content is the exposed plaintext)
{"id":"chatcmpl-msg_013RzQLKt63Mt7Yv5RLH2N9R","object":"chat.completion.chunk","created":1776683859,"model":"claude-sonnet-4-6","choices":[{"index":0,"delta":{"content":"","reasoning_content":"17 × 23\n\n= 17 × 20 + 17 × 3\n= 340 + 51\n= 391","reasoning_details":{"type":"thinking","thinking":"17 × 23\n\n= 17 × 20 + 17 × 3\n= 340 + 51\n= 391"}}}],"system_fingerprint":"fp-msg_013RzQLKt63Mt7Yv5RLH2N9R"}
// chunk 12 (last with delta): finish_reason stop
{"id":"chatcmpl-msg_013RzQLKt63Mt7Yv5RLH2N9R","object":"chat.completion.chunk","created":1776683859,"model":"claude-sonnet-4-6","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"system_fingerprint":"fp-msg_013RzQLKt63Mt7Yv5RLH2N9R"}
// chunk 13 (FINAL usage chunk): note object=""  ← Claude-specific quirk
{"id":"chatcmpl-msg_013RzQLKt63Mt7Yv5RLH2N9R","object":"","created":1776683859,"model":"claude-sonnet-4-6","choices":[],"usage":{"prompt_tokens":23,"completion_tokens":264,"total_tokens":287,"claude_cache_tokens_details":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_write_5_minutes_input_tokens":0,"cache_write_1_hour_input_tokens":0}}}
```

```jsonc
--- gemini-s3-stream.json (4 chunks total) ---
// chunk 0: bulk reasoning_content, usage all-zero
{"id":"chatcmpl-520bda16035d4247bd80cf6eac41b622","object":"chat.completion.chunk","created":1776683865,"model":"gemini","choices":[{"index":0,"delta":{"content":"","reasoning_content":"**Calculating Multiplication Steps**\n\nI'm currently exploring different methods to multiply 17 by 23, with a focus on the standard algorithm. My aim i…"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}
// chunk 1: more reasoning_content
{"id":"chatcmpl-56e4b7f6af70412d9aec17442bc7265a","object":"chat.completion.chunk","created":1776683868,"model":"gemini","choices":[{"index":0,"delta":{"content":"","reasoning_content":"**Developing Multiplication Strategies**\n\nI'm currently refining the explanation of how to multiply 17 by 23. …"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}
// chunk 2: bulk content
{"id":"chatcmpl-b5cfaee4e7a6417fa2fdaecf0b0f0f10","object":"chat.completion.chunk","created":1776683868,"model":"gemini","choices":[{"index":0,"delta":{"content":"The answer is **391**."}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}
// chunk 3: final usage (omitted here for brevity; see non-stream for structure — completion_tokens_details.reasoning_tokens=476)
```

```jsonc
--- deepseek-s3-stream.json (77 chunks total, sample) ---
// chunk 0: role init
{"id":"chatcmpl-a67f46c8-938e-9ee3-a171-45256e797b83","object":"chat.completion.chunk","created":1776683870,"model":"deepseek-v3.2","choices":[{"index":0,"delta":{"role":"assistant"}}]}
// chunk 1: first reasoning_content token
{"id":"chatcmpl-a67f46c8-938e-9ee3-a171-45256e797b83","object":"chat.completion.chunk","created":1776683870,"model":"deepseek-v3.2","choices":[{"index":0,"delta":{"reasoning_content":"We"}}]}
// chunk 2: next reasoning_content token
{"id":"chatcmpl-a67f46c8-938e-9ee3-a171-45256e797b83","object":"chat.completion.chunk","created":1776683870,"model":"deepseek-v3.2","choices":[{"index":0,"delta":{"reasoning_content":" are"}}]}
// ... many more reasoning_content tokens, then content tokens ...
// chunk 75 (last with delta): finish stop
{"id":"chatcmpl-a67f46c8-938e-9ee3-a171-45256e797b83","object":"chat.completion.chunk","created":1776683870,"model":"deepseek-v3.2","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}
// chunk 76: final usage
{"id":"chatcmpl-a67f46c8-938e-9ee3-a171-45256e797b83","object":"chat.completion.chunk","created":1776683870,"model":"deepseek-v3.2","choices":[],"usage":{"prompt_tokens":19,"completion_tokens":350,"total_tokens":369,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":179}}}
```

```jsonc
--- gpt-s3-stream.json (77 chunks total, sample) ---
// chunk 0: role init
{"id":"chatcmpl-DWgvXdNyJJznEs6dujU5P2ysvWtwH","object":"chat.completion.chunk","created":1776683879,"model":"gpt-5.4-2026-03-05","choices":[{"index":0,"delta":{"role":"assistant","content":""}}],"service_tier":"default"}
// chunk 1,2,3: content tokens — NOTHING about reasoning_content
{"id":"chatcmpl-DWgvXdNyJJznEs6dujU5P2ysvWtwH","object":"chat.completion.chunk","created":1776683879,"model":"gpt-5.4-2026-03-05","choices":[{"index":0,"delta":{"content":"17"}}],"service_tier":"default"}
{"id":"chatcmpl-DWgvXdNyJJznEs6dujU5P2ysvWtwH","object":"chat.completion.chunk","created":1776683879,"model":"gpt-5.4-2026-03-05","choices":[{"index":0,"delta":{"content":" ×"}}],"service_tier":"default"}
// ... many more content tokens ...
// final usage chunk (note: service_tier preserved even in final)
{"id":"chatcmpl-DWgvXdNyJJznEs6dujU5P2ysvWtwH","object":"chat.completion.chunk","created":1776683879,"model":"gpt-5.4-2026-03-05","choices":[],"service_tier":"default","usage":{"prompt_tokens":21,"completion_tokens":126,"total_tokens":147,"prompt_tokens_details":{"audio_tokens":0,"cached_tokens":0},"completion_tokens_details":{"accepted_prediction_tokens":0,"audio_tokens":0,"reasoning_tokens":42,"rejected_prediction_tokens":0}}}
```

---

## 8. 调用建议（总结表）

**核心决策：如果你想让用户开 thinking，应该怎么传？**

| provider_model_id | 想开 thinking，怎么做？ | 想关 thinking，怎么做？ | 能拿到 CoT 明文？ | 能拿到 reasoning_tokens 计数？ | 有 -think 变体？ |
|-------------------|--------------------------|-------------------------|-----------------|----------------------------|------------------|
| `claude-sonnet-4-6` | 传 `reasoning_effort: "medium"`（或 `low`/`high`） | 不传，或传 `reasoning_effort: "none"` | **是**（`message.reasoning_content` + `reasoning_details.thinking`） | **否**（Claude 不回此字段，reasoning 已计入 completion_tokens） | **是**：`claude-sonnet-4-6-think`（天生 on，可选温度=1） |
| `gemini-3.1-pro-preview` | 默认已 on；可传 `reasoning_effort: "low"`/`"medium"`/`"high"` 调节强度 | **不能关**（传 `none`/`minimal` 直接 400）。想省钱请换 base 非 preview 的 Gemini | **是** | **是**（`usage.completion_tokens_details.reasoning_tokens`） | **否**：`gemini-3.1-pro-preview-think` 404 |
| `deepseek-v3.2` | 传 `reasoning_effort: "medium"`（或 `low`/`high`） | 不传，或传 `reasoning_effort: "none"` | **是**（`message.reasoning_content`） | **是**（`usage.completion_tokens_details.reasoning_tokens`） | **是**：`deepseek-v3.2-think`（天生 on） |
| `gpt-5.4` | 传 `reasoning_effort: "medium"`（或 `low`/`high`）。token 参数用 **`max_completion_tokens`** | 传 `reasoning_effort: "minimal"`，或不传（default=minimal） | **否**（OpenAI 封装，AiHubMix 也看不到） | **是**（`usage.completion_tokens_details.reasoning_tokens`；关闭时=0） | **否**：`gpt-5.4-think` 404 |

### 8.1 一句话行动项（给 dmxapi.go 下一版作者）

1. **不要"原样透传"**：AiHubMix 虽是 OpenAI 兼容壳，但按 provider_model_id 分派 **是必须的**（token 字段名 + reasoning_effort 有效值 + CoT 字段是否存在 三处均分叉）。
2. **`thinking` flag 映射**：
   - `thinking=true` + Claude base → 加 `"reasoning_effort":"medium"`
   - `thinking=true` + Gemini preview → 加 `"reasoning_effort":"medium"`（但注意默认也思考，此 flag 仅影响思考深度）
   - `thinking=true` + DeepSeek base → 加 `"reasoning_effort":"medium"`
   - `thinking=true` + GPT 5.4 → 加 `"reasoning_effort":"medium"` + 确保 token 参数是 `max_completion_tokens`
   - `thinking=false` + Claude base → 不传 reasoning_effort（或传 `"none"`）
   - `thinking=false` + Gemini preview → **产品层面必须告知：该模型无法关思考**；代码层不传 reasoning_effort 或抛 `ErrThinkingDisableNotSupported`
   - `thinking=false` + DeepSeek base → 不传 reasoning_effort（或传 `"none"`）
   - `thinking=false` + GPT 5.4 → 传 `"reasoning_effort":"minimal"`（等价于关）
3. **-think variant 路由**：保留 `claude-sonnet-4-6-think` 和 `deepseek-v3.2-think`；**删除 `gemini-3.1-pro-preview-think` 和 `gpt-5.4-think`**（如果 ai_service_route 表里有，这是死路由会 400）。
4. **Billing 字段读取**（aiservice/adapter 层）：
   ```go
   // pseudo-code
   reasoningTokens := 0
   if usage.CompletionTokensDetails != nil {
       reasoningTokens = usage.CompletionTokensDetails.ReasoningTokens // Gemini/DeepSeek/GPT
   }
   // Claude doesn't expose this field at all -> reasoningTokens stays 0
   // If we want per-model reasoning rate for Claude: estimate via len(reasoning_content) / 4 chars
   ```
5. **解析器只识别 `delta.reasoning_content`**，不要再兼容 `delta.reasoning`（AiHubMix 已统一）。
6. **Gemini preview max_tokens headroom**：biz 层默认 max_tokens ≥ 1500，防止思考吃完被截断。
7. **GPT 5.4 product note**：不要在产品里承诺 "GPT 5.4 展示思考过程"。

### 8.2 五个 thinking flag 迁移决策的置信度（针对 S1 Q2=A 决策）

| 模型 | 决策选项 | 本次审计后的置信度 |
|------|---------|------------------|
| Claude `claude-sonnet-4-6` base thinking 是 optional | **高** | 实测 effort=none 关、effort=medium 开，`reasoning_content` 跟随出现/消失 |
| Gemini `gemini-3.1-pro-preview` base thinking 是 optional（可传 effort 调节） | **中低** —— 应改为 **intrinsic + depth-tunable**（无法关） | `effort=none/minimal` 直接 400；`effort=low` 实测仍产生 ~120 reasoning tokens。不能称为 optional。 |
| DeepSeek `deepseek-v3.2` base thinking 是 optional | **高** | 实测 effort=none 关、effort=medium 开；reasoning_tokens 字段在关闭时完全消失而非=0 |
| GPT `gpt-5.4` base thinking 是 optional | **高** —— 但有一个重要子特性：CoT 明文不可见 | effort=minimal（或不传）→ reasoning_tokens=0；effort=medium → reasoning_tokens>0。开关可控，但 reasoning_content 永远拿不到。 |

**产出建议**：S1 Q2 决策表里的"Gemini 3.1 Pro Preview base thinking=optional/intrinsic"应当明确改为 **intrinsic（无法关闭），reasoning_effort 只控制深度（low/medium/high，禁止 none/minimal）**。其余三个 base 模型"optional"的判断成立。

---

## 附录：测试命令模板

```bash
# Baseline
curl -s https://aihubmix.com/v1/chat/completions \
  -H "Authorization: Bearer $AIHUBMIX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<provider_model_id>",
    "messages": [{"role":"user","content":"What is 17 × 23? Show your reasoning step by step."}],
    "max_tokens": 500  # or max_completion_tokens for gpt-5.4
  }'

# With reasoning
curl ... -d '{..., "reasoning_effort": "medium"}'

# Streaming (always include_usage)
curl ... -d '{..., "stream": true, "stream_options": {"include_usage": true}}'
```

---

*Audit conducted 2026-04-20 by AI investigator per `aihubmix-protocol-audit` S1 gate. All `/tmp/aihubmix-audit/*.json` responses backed by real 200/400 HTTP calls. Bypassing this document = flying blind.*
