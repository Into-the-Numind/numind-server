# AiHubMix Provider 接入 — 技术设计

> 本 spec 继承自：
> - Requirement: `numind-server/requirements/aihubmix-provider.md`
> - Proposal + PRD: `numind-server/proposals/aihubmix-provider-proposal.md`

---

## §1 Context

项目当前通过 `biz/llmrouter/Router` 调度 LLM 调用，provider 动态化存于 `llm_provider` 表。本 spec 增加一家新 provider `aihubmix`，复用现有路由机制，为 4 个核心模型提供第二路由（主）+ 保留 DMXAPI（备）。

### 现状速查（develop 分支）

| 事实 | 证据 |
|------|------|
| 4 个 logical model_key 已存在：`claude-sonnet-4-6` / `gemini-3.1-pro-preview` / `deepseek-v3.2` / `gpt-5.4` | `migrations/20260410_000001_add_llm_routing_tables.sql:68-72` |
| 对应 thinking 变体已存在（`-thinking` 后缀，注意是 `-thinking` 不是 `-think`） | 同上 :75-79 |
| `DMXAPIClient.StreamChatCompletion` 已读取 SSE delta 中的 `reasoning_content` | `internal/pkg/llm/dmxapi_client.go:362` |
| Router 已支持多路由 failover（按 priority 排序后遍历） | `biz/llmrouter/router.go:119-172` |
| Claude thinking 目前通过 DMXAPI 的 `-thinking` 后缀激活 + temperature=1 | `dmxapi_client.go:268-270` |

### 关键术语约定

| 术语 | 含义 |
|------|------|
| **model_key** | 我们系统内部的逻辑模型名（DB 表 `llm_model.model_key`），例如 `claude-sonnet-4-6-thinking`（我方命名，-thinking） |
| **provider_model_id** | 发给 provider API 的模型字符串（DB 表 `llm_model_provider.provider_model_id`），例如 AiHubMix 的 `claude-sonnet-4-6-think`（人家命名，-think） |

---

## §2 范围

**In scope**：
- 在 `llm_provider` 表 INSERT 一行 `aihubmix`
- 在 `llm_model_provider` 表 INSERT 8 行（4 base + 4 thinking 变体）
- 在 `pricing_rule` + `pricing_rule_tier` 表 INSERT 价格规则
- 扩展 `DMXAPIClient.StreamChatCompletion` 支持 `reasoning_effort` 参数注入
- 扩展 `llmrouter.inferThinkingFormat` 按 provider_name 维度分派 ThinkingFormat
- config_*.yaml 四个环境文件新增 `aihubmix.*` 配置（api_key 字面值直写，本次豁免 CLAUDE.md 规则）

**Out of scope**：
- 新 provider 的管理端 CRUD（ai-service-manager 功能会统一接管）
- C 端用户感知的变化（ModelSelector UI 零改动）
- 数据库 schema 变更（仅 INSERT）
- 替换 DMXAPI（DMXAPI 保留作 failover）

---

## §3 架构决策

### D1：ThinkingFormat 分派维度从"模型名"改为"provider + 模型名"

**问题**：当前 `inferThinkingFormat(providerModelID string)` 按模型名前缀推断（如含 "gemini" → 走原生 /v1beta）。但 AiHubMix 的 gemini 走 OpenAI 兼容路径 + reasoning_effort，与 DMXAPI 的 gemini 走原生路径冲突。

**决策**：签名扩展为 `inferThinkingFormat(providerName, providerModelID string)`。
- `providerName == "aihubmix"` 优先分支：
  - 若 providerModelID 以 `-think` 结尾（Claude 专属后缀变体）→ 返回 `ThinkingNone`（thinking 已内置于模型，不需额外参数）
  - 否则 → 返回新常量 `ThinkingReasoningEffort`
- 其他 providerName → 沿用现有按 providerModelID 推断的逻辑（保持 DMXAPI 行为不变）

**替代方案未采纳**：
- 在 `llm_model_provider` 加 `thinking_format` 列：需 schema 变更，ai-service-manager 正在重构该表，避免冲突
- 用一张"provider → thinking_strategy"映射表：同上，schema 风险

### D2：thinking 模式激活协议

**AiHubMix 上 Claude 的 thinking** 通过 `-think` 模型后缀（provider_model_id 为 `claude-sonnet-4-6-think`）
→ 请求体**不传** reasoning_effort，也不传 enable_thinking
→ Claude thinking 要求 temperature=1：**扩展** `StreamChatCompletion:268` 的后缀检查，从 `-thinking` 扩展为同时匹配 `-think` 和 `-thinking`

**AiHubMix 上 Gemini/GPT/DeepSeek 的 thinking** 通过统一的 `reasoning_effort` 参数
→ provider_model_id 为基础模型名（无后缀，与非 thinking 变体相同）
→ 请求体注入 `reasoning_effort: "high"`
→ temperature 无特殊要求

### D3：priority 策略（主备通道）

AiHubMix 各路由 `priority = 10`，DMXAPI 各路由保持现有值（预计 20 或更高）。Router 按 priority 升序遍历，小值优先，因此 AiHubMix 为主、DMXAPI 为备。

**验证**：S3 plan 的第一个 task 必须先 SELECT 现有 DMXAPI 路由的 priority 值，若已为 10 或更低，需手动调整 AiHubMix 的 priority 使其更小（如改为 5）。

### D4：响应解析无需修改

`dmxapi_client.go:362` 的 SSE chunk 结构已读 `delta.reasoning_content` 字段 → 触发 `onEvent("thinking", ...)`。AiHubMix 的 `reasoning_content` 字段与现有路径一致，零代码改动。

`reasoning_details`（结构化元数据）不消费，接受 Phase 1 限制。

### D5：Pricing 分档实现

**Gemini / GPT**：`billing_mode = 'tiered_token'`，`pricing_rule.input_price_per_m_tok` 和 `output_price_per_m_tok` 填 0，实际价格走子表 `pricing_rule_tier`。

**Claude / DeepSeek**：`billing_mode = 'flat'`，价格直接填在 pricing_rule 行上。

### D6：API Key 存储

本次例外：`aihubmix.api_key` 字面值直写 4 个 config_*.yaml 文件（含 prod）。用户已明示豁免 CLAUDE.md 规则。启动时 `store.LLMProvider().SyncFromConfig()` 或等价逻辑将 config 中的 api_key 同步到 `llm_provider.api_key` 列（参考 DMXAPI 现有同步方式；若不存在同步机制，在 seed SQL 中直写 api_key 字符串）。

**TODO for S3 plan**：确定 config → DB 的 api_key 同步路径。若当前无此机制，seed SQL 直接 INSERT 字面值是可接受的权宜方案。

---

## §4 数据变更

### §4.1 `llm_provider` 新增一行

```sql
INSERT IGNORE INTO llm_provider (name, display_name, base_url, api_key, is_active)
VALUES
  ('aihubmix', 'AiHubMix', 'https://aihubmix.com/v1',
   'sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68', 1);
```

### §4.2 `llm_model_provider` 新增 8 行

| model_key (DB 内部) | provider_model_id (发给 AiHubMix) | priority |
|---|---|---|
| `claude-sonnet-4-6` | `claude-sonnet-4-6` | 10 |
| `claude-sonnet-4-6-thinking` | `claude-sonnet-4-6-think` | 10 |
| `gemini-3.1-pro-preview` | `gemini-3.1-pro-preview` | 10 |
| `gemini-3.1-pro-preview-thinking` | `gemini-3.1-pro-preview` | 10 |
| `deepseek-v3.2` | `deepseek-v3.2` | 10 |
| `deepseek-v3.2-thinking` | `deepseek-v3.2` | 10 |
| `gpt-5.4` | `gpt-5.4` | 10 |
| `gpt-5.4-thinking` | `gpt-5.4` | 10 |

**注意**：gemini/deepseek/gpt 的 thinking 与 base 在 AiHubMix 侧是同一个 provider_model_id；区分由 ThinkingFormat 在请求层通过 `reasoning_effort` 参数实现。

### §4.3 `pricing_rule` + `pricing_rule_tier` 新增

**flat 模式（Claude、DeepSeek）**

| service_type | provider | model | input ¥/M | output ¥/M |
|---|---|---|---|---|
| llm_chat | aihubmix | `claude-sonnet-4-6` | 21.60 | 108.00 |
| llm_chat | aihubmix | `claude-sonnet-4-6-think` | 21.60 | 108.00 |
| llm_chat | aihubmix | `deepseek-v3.2` | 2.16 | 3.24 |

**tiered_token 模式（Gemini、GPT）— pricing_rule 头表**：

| service_type | provider | model | billing_mode |
|---|---|---|---|
| llm_chat | aihubmix | `gemini-3.1-pro-preview` | tiered_token |
| llm_chat | aihubmix | `gpt-5.4` | tiered_token |

**pricing_rule_tier 分档明细**（rule_id 由上表 INSERT 后取得）：

| rule | token_type | min_tokens | max_tokens | cost_per_mtok |
|---|---|---|---|---|
| gemini-3.1-pro-preview | input | 0 | 200000 | 14.40 |
| gemini-3.1-pro-preview | input | 200001 | NULL | 28.80 |
| gemini-3.1-pro-preview | output | 0 | 200000 | 86.40 |
| gemini-3.1-pro-preview | output | 200001 | NULL | 129.60 |
| gpt-5.4 | input | 0 | 272000 | 18.00 |
| gpt-5.4 | input | 272001 | NULL | 36.00 |
| gpt-5.4 | output | 0 | 272000 | 108.00 |
| gpt-5.4 | output | 272001 | NULL | 162.00 |

**分档依据**：AiHubMix 模型页截图（2026-04-16）。汇率 × 7.2（美元 → 人民币）。

**注**：`token_type` 区间以 **input tokens 长度**为索引；output 价格也按 input size 所属档位取值（而非 output size 自身长度）。S3 plan 需验证现有 billing recorder 的档位查找逻辑符合此语义。

---

## §5 代码变更

### §5.1 `internal/pkg/llm/dmxapi_client.go`

**改动 1**（line ~268）：后缀匹配从 `-thinking` 扩展为同时匹配 `-think`

```go
// 旧
if strings.HasSuffix(strings.ToLower(model), "-thinking") {
    bodyMap["temperature"] = 1
}

// 新
lowerModel := strings.ToLower(model)
if strings.HasSuffix(lowerModel, "-thinking") || strings.HasSuffix(lowerModel, "-think") {
    bodyMap["temperature"] = 1
}
```

**改动 2**（line ~272 switch thinkingFormat）：新增 `case "reasoning_effort"`

```go
case "reasoning_effort":
    bodyMap["reasoning_effort"] = "high"
```

**400 重试安全网**（line ~309 已存在）：body 包含 `"reasoning_effort"` 也触发去 thinking 重试。扩展字符串匹配条件。

### §5.2 `internal/numind/biz/llmrouter/types.go`

新增常量：

```go
// ThinkingReasoningEffort AiHubMix 统一推理协议：通过 reasoning_effort 参数激活
ThinkingReasoningEffort = "reasoning_effort"
```

### §5.3 `internal/numind/biz/llmrouter/router.go`

**改动 1**：`inferThinkingFormat` 签名加一个参数。

```go
func inferThinkingFormat(providerName, providerModelID string) string {
    id := strings.ToLower(providerModelID)

    if providerName == "aihubmix" {
        if strings.HasSuffix(id, "-think") {
            // Claude thinking 变体：thinking 内置，不传 reasoning_effort
            return ThinkingNone
        }
        return ThinkingReasoningEffort
    }

    // 其他 provider：沿用原有逻辑
    if strings.Contains(id, "claude") {
        return ThinkingNone
    }
    // ... 其余保持不变
}
```

**改动 2**：调用点（line ~79）更新为传入 provider_name：

```go
tf = inferThinkingFormat(mp.Provider.Name, mp.ProviderModelID)
```

### §5.4 `config_*.yaml`（4 个环境）

新增配置节：

```yaml
aihubmix:
  base_url: "https://aihubmix.com/v1"
  api_key: "sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68"
```

**local/dev/qa/prod 四个环境用同一个 api_key**（用户已确认）。

---

## §6 Failover 语义（无代码变更，仅确认现有行为）

1. 用户请求 model_key = `claude-sonnet-4-6-thinking`
2. `Router.Resolve` 返回按 priority 升序的路由列表：
   - [0] aihubmix::claude-sonnet-4-6-think (priority=10, ThinkingFormat=None)
   - [1] dmxapi::claude-sonnet-4-6-thinking (priority=20, ThinkingFormat=None, 已有)
3. `Router.StreamChat` 遍历：先打 aihubmix，成功 → 返回；失败 → log warn + 打 dmxapi
4. 全部失败 → 返回 `all routes failed for model %q` 错误

无需新增失败分类、重试次数、降级指标等 — 全部沿用。

---

## §7 可观测性（trace topology）

**结论**：零新增。全部复用 `biz/llmrouter/router.go:StreamChat` 的现有 `llm-chat` generation 记录：

- `model` 字段 = `route.ProviderModelID`（例如 `claude-sonnet-4-6-think` 或 `gemini-3.1-pro-preview`）
- `usage.promptTokens` / `completionTokens` 正常记录
- billing 侧通过 `route.ProviderName`（`"aihubmix"`）区分

**事后分析（不在本功能范围，记录在此）**：上线后可在 Langfuse 按 `metadata.provider` 聚合主备通道的 p95 latency / error rate，决定是否调整 priority。

---

## §8 PRD 验收标准映射

| PRD §4 验收条目 | 本 spec 覆盖点 |
|---|---|
| 4 模型各在 chatbot 测 thinking | §5 代码 + §4 数据 |
| trace.provider_name = aihubmix | §7（Langfuse `metadata.provider` 来自 route.ProviderName）|
| 人为故障触发 failover 到 DMXAPI | §6 failover 语义 + §4.2 priority=10 vs 20 |
| usage_record.cost_cents > 0 且符合 AiHubMix 单价 | §4.3 pricing_rule / pricing_rule_tier |
| `task lint` + `go test ./...` 通过 | §5 代码改动 + 单元测试（S3 plan 会排 task） |
| `gstack /qa` 覆盖 4 模型 | S5 验收策略（S3 plan 会单排 task） |

---

## §9 Out-of-spec 边界情况（按 PRD §4 边界情况节）

| 情境 | 本 spec 处理 |
|---|---|
| AiHubMix 返回非标准 reasoning_details 结构 | 不消费 reasoning_details，只取 delta.reasoning_content |
| `reasoning_effort` 被某模型拒绝（400） | 复用 `dmxapi_client.go:309` 现有 fallback，去 thinking 重试并保留 warn 日志 |
| AiHubMix 账户余额不足 | 返回 401/402，Router failover 到 DMXAPI；S5 验收前必须充值账户 |
| failover 级联失败（两家都挂） | 沿用现有 `all routes failed` 错误，上层已处理 |
| config api_key 为空字符串 | 启动时同步到 DB，调用时 401 → failover |

---

## §10 预估任务数（给 S3 plan 参考）

5-6 个原子 task：

1. Seed migration（llm_provider + llm_model_provider + pricing_rule + pricing_rule_tier）
2. `dmxapi_client.go` 扩展（后缀匹配 + reasoning_effort 分支 + 400 fallback 扩展）+ 单元测试
3. `llmrouter` 扩展（新常量 + inferThinkingFormat 签名 + 调用点）+ 单元测试
4. `config_*.yaml` × 4 新增 aihubmix 配置 + Viper 读取路径（若需）+ DB 同步逻辑（若 DMXAPI 有现成模式则 copy）
5. S5 验证策略 task（必填，按 NDF 规则 10）：
   - 验证方式：`gstack /qa`（4 模型各一次 chat，观察 thinking 正常渲染 + Langfuse trace 出现 + DB usage_record 扣费）+ 手动故障注入测试 failover
   - 关键用户路径：访问 `http://localhost:5173/chatbot` → 登录 → 新建会话 → ModelSelector 选模型 → 发送"用一段话介绍你自己" → 确认 ThinkingBlock 渲染 thinking 内容 + 正文流式返回

（S3 的独立 Sonnet reviewer 审 plan 原子性 + 验证策略合理性。）

---

## §11 开放问题（S3 之前需确认）

- **P0**: DMXAPI 当前 llm_model_provider.priority 具体值是多少？若 ≤ 10，需反向调整 AiHubMix 到更小值。S3 plan 第一个 task 就是 SELECT 验证。
- **P0**: config → llm_provider.api_key 的同步机制是否存在？若无，seed SQL 直接 INSERT 字面值 api_key 是唯一可行路径。
- **P1**: S5 验收前 AiHubMix 账户是否已充值？未充值则所有 AiHubMix 调用 402/401 直接 failover 到 DMXAPI，等于没测到主路径。
