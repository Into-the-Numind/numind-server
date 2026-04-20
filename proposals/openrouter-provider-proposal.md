# OpenRouter Provider 接入 — S1 Proposal

> 工件类型: Proposal (S1)
> 引用: requirements/openrouter-provider.md
> 日期: 2026-04-20

## 1. 技术调查结论（真实请求验证）

### 1.1 API key / 模型 slug 全部有效

```bash
curl -sS GET https://openrouter.ai/api/v1/models -H "Authorization: Bearer sk-or-v1-1a7c..."
```

| 目标 slug | 存在 |
|-----------|-----|
| `anthropic/claude-sonnet-4.6` | ✅ |
| `google/gemini-3.1-pro-preview` | ✅ |
| `openai/gpt-5.4` | ✅ |
| `deepseek/deepseek-v3.2` | ✅ |

### 1.2 协议兼容性（OpenAI 兼容 `/chat/completions`）

**请求结构**：`model`/`messages`/`max_tokens`/`temperature`/`stream` — 完全同 OpenAI 规范。

**Thinking 激活**：两种形式均接受
- 扁平：`reasoning_effort: "low"|"medium"|"high"`（OpenAI 兼容）
- 对象：`reasoning: {effort: "low"|"medium"|"high"}`（OpenRouter 推荐）

本设计采用**扁平 `reasoning_effort`**，与 AiHubMix 保持一致（dmxapi_client.go case "reasoning_effort" 已有分支）。

### 1.3 ⚠️ 关键发现：流式 reasoning 字段名差异

OpenRouter 流式 SSE chunk 的结构：

```jsonc
{
  "choices": [{
    "delta": {
      "content": "",
      "role": "assistant",
      "reasoning": "56",                     // ← OpenRouter 用这个字段
      "reasoning_details": [{...}]           // 结构化元数据（含 signature）
    }
  }]
}
```

而现有 `dmxapi_client.go:352` 解析的是 `delta.reasoning_content`（AiHubMix 约定）：

```go
Thinking string `json:"reasoning_content"` // 有些厂商可能使用 reasoning_content
```

**结论**：无最小改动即可复用 dmxapi adapter，**但必须在 delta struct 新增 `Reasoning` 字段并 OR 到 Thinking**。这是 S4 实施的**必做改动**。

### 1.4 非流式响应结构

`message.reasoning`（字符串）+ `message.reasoning_details`（结构化）并存。现有 biz 层只消费流式，非流式 reasoning 字段不影响。

### 1.5 Temperature 兼容性

Claude on OpenRouter **接受** temperature=0.7 + reasoning 组合（不强制温度=1）。现有 dmxapi_client.go 的"Claude -thinking 后缀强制 temperature=1"分支对 OpenRouter provider_model_id（`anthropic/claude-sonnet-4.6`，无 -thinking/-think 后缀）**不会触发**，符合预期。

### 1.6 成本实测

| 模型 | 调用 | prompt | completion | $ 成本 |
|------|-----|--------|------------|-------|
| deepseek/deepseek-v3.2 | "say hi in 3 words" | 10 tok | 5 tok | $0.00000469 |
| anthropic/claude-sonnet-4.6 + reasoning:low | "hi" | 8 tok | 50 tok | $0.000774 |

与 AiHubMix 同型号价格基本持平（aihubmix claude-sonnet-4-6 $3/$15 per M token → 50 tok out = $0.00075，吻合）。

---

## 2. 架构决策

### 2.1 复用 DMXAPI adapter via alias

OpenRouter 走 OpenAI 兼容 `/chat/completions`，与 DMXAPI/AiHubMix 协议同源。采用相同策略：

```go
gateway.RegisterProviderAlias("openrouter", "dmxapi")
```

**唯一代码改动**在 `dmxapi_client.go` 的 SSE delta 解析 struct：加 `Reasoning string \`json:"reasoning"\`` 并与 `Thinking`（`reasoning_content`）合并。双字段保留 — 向后兼容 AiHubMix/DMXAPI。

### 2.2 凭据管理：走 SyncProviderCredentials（不硬编码）

相较 aihubmix-provider 的"api_key 字面值直写 migration SQL"豁免方案，本次采用**标准清洁路径**：

1. `config_dev.yaml` + `config_prod.yaml` 新增 `ai_providers.openrouter.api_key` 和 `base_url`
2. `internal/pkg/aiservice/seed.go` 的 `providerSeedEntries` 追加 openrouter 条目
3. migration SQL 插入 llm_provider 行时 `api_key=''`（空串），启动时 SyncProviderCredentials 从 config UPSERT 真实 key 到 DB
4. 遵守 CLAUDE.md §3 "禁止硬编码 API 密钥" 规则

**效益**：key 轮换只需改 config 重启，无需改代码或 SQL。

### 2.3 优先级策略

Router 按 `priority` **升序遍历**，最小值最先选中：

| Provider | priority | 角色 |
|----------|---------|-----|
| **openrouter** | **1** | **新主路由（本次新增）** |
| aihubmix | 5 | 次路由（现状） |
| dmxapi | 10 | failover（现状） |

所有 8 条 llm_model_provider 行（4 base + 4 thinking）priority=1。

### 2.4 Pricing 策略：与 AiHubMix 同价（用户确认 C 方案）

复制 aihubmix pricing_rule 结构（5 条 pricing_rule + 8 条 pricing_rule_tier）：

- Claude Sonnet 4.6: flat ¥21.60/M input, ¥108.00/M output
- DeepSeek V3.2: flat ¥2.16/M input, ¥3.24/M output
- Gemini 3.1 Pro Preview: tiered_token（≤200K ¥14.40/¥86.40；>200K ¥28.80/¥129.60）
- GPT 5.4: tiered_token（≤272K ¥18.00/¥108.00；>272K ¥36.00/¥162.00）

**注**：OpenRouter 实际成本与 AiHubMix 基本持平（§1.6 实测），同价策略合理。

### 2.5 Attribution header：不加

OpenRouter 可选 `HTTP-Referer` + `X-Title` header 提升 ranking。为保持 dmxapi_client.go 通用性（adapter 被 DMXAPI + AiHubMix + OpenRouter 三方共用），**不在客户端注入**。日后如需可通过 base_url 路径参数或独立 adapter 注入。

### 2.6 Claude Thinking：复用 base provider_model_id + reasoning_effort

不走 aihubmix 的 `-think` 后缀变体路径（那是 aihubmix 历史包袱）。模型映射表：

| model_key | openrouter provider_model_id | thinking 激活方式 |
|-----------|------------------------------|------------------|
| claude-sonnet-4-6 | anthropic/claude-sonnet-4.6 | （无） |
| claude-sonnet-4-6-thinking | anthropic/claude-sonnet-4.6 | reasoning_effort=medium |
| gemini-3.1-pro-preview | google/gemini-3.1-pro-preview | （无） |
| gemini-3.1-pro-preview-thinking | google/gemini-3.1-pro-preview | reasoning_effort=medium |
| gpt-5.4 | openai/gpt-5.4 | （无） |
| gpt-5.4-thinking | openai/gpt-5.4 | reasoning_effort=medium |
| deepseek-v3.2 | deepseek/deepseek-v3.2 | （无） |
| deepseek-v3.2-thinking | deepseek/deepseek-v3.2 | reasoning_effort=medium |

thinking 参数注入由现有 llm_model.is_thinking → thinkingFormat 选路逻辑决定（OpenRouter 同 AiHubMix，均用 "reasoning_effort" 分支）。

---

## 3. 影响范围 & 工件清单（S3 将拆 task）

### 3.1 预计文件改动（6 个）

1. `migrations/20260420_XXXXXX_seed_openrouter_provider.sql`（**新增**，参考 aihubmix 模板）
2. `migrations/20260420_XXXXXX_seed_openrouter_provider_rollback.sql`（**新增**）
3. `internal/pkg/llm/dmxapi_client.go`（**改动**，delta struct 加 `Reasoning` 字段 + OR 合并）
4. `internal/pkg/aiservice/seed.go`（**改动**，providerSeedEntries 追加 openrouter）
5. `internal/numind/numind.go`（**改动**，RegisterProviderAlias("openrouter", "dmxapi")）
6. `config_dev.yaml` + `config_prod.yaml`（**改动**，ai_providers.openrouter.api_key + base_url）

**不改动**：前端代码、llm_model 表结构、router.go、biz/sop、biz/chatbot。

### 3.2 回归风险点

- **R1**：dmxapi_client.go SSE 解析改动可能影响 AiHubMix/DMXAPI thinking 渲染
  - 缓解：保留 `reasoning_content` 字段解析不删，新增 `reasoning` 字段 OR 合并
  - S5 验证：用 AiHubMix thinking 模型发起流式调用，确认 ThinkingBlock 渲染正常

- **R2**：priority=1 使 OpenRouter 成主路由，真实流量打到 OpenRouter，若出错影响用户
  - 缓解：现有 Router 已有失败降级机制（遍历 priority 列表），出错自动切 AiHubMix(5)/DMXAPI(10)
  - S5 验证：OpenRouter 故障注入（临时改 api_key 为错误值）确认降级生效

- **R3**：config_prod.yaml api_key 明文存储（与 CLAUDE.md §3 相悖）
  - 用户已明示授权 dev + prod 明文配置（manifest decision 记录）
  - 豁免范围：仅限 ai_providers.openrouter.api_key 一项，不扩散至其他 secret

### 3.3 与 hotfix-default-thinking-mode (H1) 的兼容性

hotfix 只改 `biz/llmrouter/preference.go`，本功能不触及该文件。两者独立分支，无冲突。

---

## 4. 进入 S2 的前置条件

- [x] OpenRouter API key 可用
- [x] 4 模型 slug 确认
- [x] 流式协议差异识别（`reasoning` vs `reasoning_content`）
- [x] 凭据管理路径明确（SyncProviderCredentials）
- [x] 优先级策略确认（priority=1 最高）
- [x] Pricing 策略确认（与 aihubmix 同价）
- [x] 与活跃 hotfix 无冲突

**可进入 S2**。

---

## 5. S2 将产出

技术 spec（`docs/superpowers/specs/2026-04-20-openrouter-provider-design.md`），包含：

1. 完整的 migration SQL 草案（8 行 llm_model_provider + 5 pricing_rule + 8 pricing_rule_tier）
2. dmxapi_client.go SSE struct 具体 diff
3. SyncProviderCredentials openrouter entry 的精确代码
4. 配置文件 YAML 片段（dev + prod）
5. S5 验证脚本草案（Langfuse trace 筛选 + 故障注入步骤）
