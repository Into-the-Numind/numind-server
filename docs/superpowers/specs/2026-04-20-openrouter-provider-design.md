# OpenRouter Provider 接入 + 思考管道 + 管理矩阵视图 — S2 Design Spec

> 工件类型: Spec (S2)
> 引用: requirements/openrouter-provider.md · proposals/openrouter-provider-proposal.md
> 日期: 2026-04-20
> 版本: 1.0

## §0 Spec 命名与范围扩展说明

S1 proposal 的标题是"OpenRouter Provider 接入"。经过 S2 brainstorming，范围已扩展为三件事：

1. **接入 OpenRouter** 作为新的主路由
2. **打通 `reasoning_effort` 思考管道**（含顺带修好 AiHubMix 存量的"思考不生效"问题）
3. **管理后台矩阵视图重构**（消灭 model_key 双分身 + 可视化 3 级关系）

之所以一次性做三件事：S1 proposal 技术调查时发现"接入 OpenRouter 而不打通思考 = 面子工程"，用户进一步指出"管理后台不直观"，三个问题互相锁死，单独做任何一个都不能交付真实价值。

**不变的北极星**（用户 must-have）：
- Claude 4.6 / GPT 5.4 / Gemini 3.1 Pro / DeepSeek V3.2 —— 通过聚合商访问
- 默认深度思考、可调强度
- 前端能看到思维链
- Token / 成本 / 耗时 全部记录可查

---

## §1 架构实情校正（与 S1 proposal 不一致之处）

> S1 proposal 是基于"`llm_model` / `llm_model_provider` / `pricing_rule` 老架构"写的。S2 探索发现运行时早已迁移到新架构。此节纠偏，确保 S3 plan 落到活表。

### 1.1 权威数据模型（真实运行时）

```
┌─────────────────────────────────────────────────┐
│ 模型 ai_service                                  │
│ ├─ id, model_key, display_name                   │
│ ├─ service_type ('llm' | 'ocr' | 'asr')         │
│ ├─ is_thinking / supports_thinking               │
│ │   / thinking_only  ← 思考能力的 3 个开关       │
│ ├─ base_model_id (FK self — thinking 变体指向基础) │
│ ├─ deprecated_at (NULL = 活跃)                   │
│ └─ capability_json / tags / latency/quality tier │
└─────────────────────────────────────────────────┘
             │ 1:N
             ▼
┌─────────────────────────────────────────────────┐
│ 路由 ai_service_route                            │
│ ├─ id, model_id (FK ai_service)                  │
│ ├─ provider_id (FK llm_provider)                 │
│ ├─ provider_model_id                             │
│ ├─ priority (INT, 越大优先级越高)                │
│ ├─ is_active                                     │
│ └─ 〔新增〕reasoning_effort VARCHAR(10)          │
└─────────────────────────────────────────────────┘
             │ N:1
             ▼
┌─────────────────────────────────────────────────┐
│ 服务商 llm_provider                              │
│ ├─ id, name, display_name, base_url, api_key     │
│ └─ is_active                                     │
└─────────────────────────────────────────────────┘

独立计费：
┌─────────────────────────────────────────────────┐
│ pricing_rule (service_type + provider + model)   │
│ ├─ billing_mode ('flat' | 'tiered_token')        │
│ └─ (flat 用直接字段; tiered 走 pricing_rule_tier) │
└─────────────────────────────────────────────────┘
```

### 1.2 关键事实清单（S3 plan 必须遵守）

| 事实 | 出处 | 含义 |
|------|------|------|
| 运行时查询 **只读** `ai_service` + `ai_service_route` | `internal/pkg/aiservice/registry/store.go:305-331` | Migration 必须写入新表名 |
| `ai_service_route` 2026-04-18 已删除定价列 | `migrations/20260418_180000_drop_route_pricing_columns.sql` | 定价**只能**在 `pricing_rule` |
| `priority` 字段规则：**越大越优先**（DESC 排序） | `store.go:324` `ORDER BY r.priority DESC` | 新主路由数值要大于 100（aihubmix 当前值） |
| 调用入口：`aiservice.ChatStream`，走 dmxapi adapter | `biz/sop/executor.go:468` · `biz/chatbot/stream.go:179` | SSE 解析在 `aiservice/adapter/adapter.go`，非 `llm/dmxapi_client.go` |
| `llm/dmxapi_client.go` 在 `internal/` 非测试代码下零引用（仅自己的 `_test.go` 引用） | grep 验证 | S1 proposal 建议改它是误导，**忽略**；测试一并 defer 为未来 tech debt |
| `oaiChatRequest` 当前无 `reasoning_effort` 字段 | `adapter/adapter.go:137-150` | 本次必须新增 |
| `oaiStreamChunk.Delta` 只解析 `reasoning_content` | `adapter/adapter.go:198-210` | 本次新增 `reasoning` 字段合并 |
| OpenRouter 流式用 `delta.reasoning` 字段 | S1 proposal §1.3 实测 | 必须加字段才能接收思维链 |
| `ResolvedRoute` 结构无 `ReasoningEffort` | `aiservice/registry/registry.go:51-63` | 本次必须扩展 |
| Admin 路由 CRUD 已存在 | `admin_router.go:181-217` | 扩展现有 PUT 接口，不创建新接口 |
| aihubmix 8 条路由已在 `ai_service_route`（通过 VIEW passthrough 自动迁移） | `20260416` RENAME + VIEW | 本次不需要数据迁移，只需要扩展字段 |

---

## §2 需求↔实现映射（必须全覆盖）

| 用户 must-have | 落地方式 | 验证点 |
|--------------|---------|-------|
| Claude / GPT / Gemini / DeepSeek 最新版 | OpenRouter seed 4 条 `ai_service_route`，`provider_model_id` 分别为 `anthropic/claude-sonnet-4.6` / `openai/gpt-5.4` / `google/gemini-3.1-pro-preview` / `deepseek/deepseek-v3.2` | S5 真实调用各模型 |
| 通过聚合商调用 | OpenRouter (priority=1000) + AiHubMix (100) + DMXAPI (10) 三家并行 | S5 failover 演练 |
| 思考模式真实生效 | `ai_service_route.reasoning_effort` + dmxapi adapter 注入 + SSE `reasoning` 字段合并 | S5 调一次 Claude，前端能看到思维链 + Langfuse trace `reasoning_tokens > 0` |
| 管理员方便管理 | 后台新增"AI 矩阵视图"页面，单元格点击即编辑路由（含思考强度） | S5 浏览器 QA 走一遍"改 Claude 的 OpenRouter 路由思考强度" |
| 思维链前端展示 | adapter 将 `reasoning` 合并进 `ReasoningContent` → `ChatChunk.ReasoningDelta` 流式透传 → 前端已有渲染逻辑。**GPT 5.4 例外**：OpenAI 加密推理不返回 reasoning_content，前端对空内容走 fallback（见 §14.2） | S5 Playwright 验证 Claude/Gemini/DeepSeek 思维链块可见；GPT 5.4 验证 fallback 显示 |
| Token 消耗量 | 复用现有 `oaiUsage`（`PromptTokens/CompletionTokens/TotalTokens/ReasoningTokens`），已被 billing middleware 记录 | S5 查 `usage_record` 表，OpenRouter 调用有新记录 |
| 成本统计 | `pricing_rule` seed OpenRouter 4 条父规则（Claude flat / DeepSeek flat / Gemini tiered / GPT tiered）+ `pricing_rule_tier` 8 条分档子规则；billing middleware 按规则计算 | S5 查 `billing_record`，单价 × token 量 = 合理金额 |
| 耗时统计 | Langfuse generation 的 `startTime` + `endTime` 差值；已有基础设施 | S5 打开 Langfuse UI，generation 有 duration_ms |

**零遗漏**：上述 8 条覆盖用户 must-have 全部。

---

## §3 数据模型变更

### 3.1 `ai_service_route` 新增字段

```sql
ALTER TABLE ai_service_route
  ADD COLUMN reasoning_effort VARCHAR(10) DEFAULT NULL
  COMMENT '思考强度：NULL=不启用 | low | medium | high | minimal；值原样注入 OpenAI 兼容请求的 reasoning_effort 字段';
```

**语义**：
- `NULL` → adapter 不发 `reasoning_effort` → 提供商走默认（普通模式）
- `low | medium | high | minimal` → adapter 注入 `reasoning_effort: "<值>"` → 提供商进入思考模式

**值约束**：应用层校验（见 §7.2），DB 不加 CHECK 约束（避免未来新值要改 schema）。

### 3.2 `ai_service` 的思考能力表达（复用现有字段，不加列）

经过探索验证，`ai_service` 已有 3 个布尔字段足以表达 3 种模型形态，**不需要新增 `thinking_capability` 列**：

| 用户语义（spec §2 的 3 种形态） | `supports_thinking` | `thinking_only` | 业务含义 |
|-----------------------------|-------------------|----------------|--------|
| 只支持基础（none） | false | false | 模型不具备思考能力 |
| 只支持思考（intrinsic） | true | true | 天生思考，无法关闭（o1/o3、R1） |
| 可调节（optional） | true | false | 可通过 `reasoning_effort` 切换 |

后端/前端统一通过这个组合判断，见 §7.3 约束规则。

### 3.3 `ai_service` 的 `-thinking` 变体收敛（软删除）

**动机**：`claude-sonnet-4-6` 和 `claude-sonnet-4-6-thinking` 分别是两条记录，用户 B 痛点（"长得像两个东西"）。

**操作**：对 4 对 `-thinking` 变体执行软删除（`deprecated_at=NOW()`），保留基础 `model_key`，把"支持思考"标记到基础记录：

```sql
-- 4 条 base：打开 supports_thinking
UPDATE ai_service SET supports_thinking = 1
WHERE model_key IN (
  'claude-sonnet-4-6',
  'gpt-5.4',
  'gemini-3.1-pro-preview',
  'deepseek-v3.2'
);

-- 4 条 -thinking：软删
UPDATE ai_service SET deprecated_at = NOW(3)
WHERE model_key IN (
  'claude-sonnet-4-6-thinking',
  'gpt-5.4-thinking',
  'gemini-3.1-pro-preview-thinking',
  'deepseek-v3.2-thinking'
);
```

**为什么软删不硬删**：
- 零风险回滚（`SET deprecated_at = NULL`）
- 历史 Langfuse trace 里可能还引用过 `model_key='claude-sonnet-4-6-thinking'`，硬删丢失上下文
- `ai_service_route` 里引用这些 id 的行也会被保留；我们会把它们一并 deactivate（is_active=0），不删除

**对应 `ai_service_route`**：软禁用引用了被 deprecated ai_service 的路由（以及清理其 pricing_rule，见 §3.6）：

```sql
UPDATE ai_service_route r
  JOIN ai_service s ON s.id = r.model_id
  SET r.is_active = 0
WHERE s.deprecated_at IS NOT NULL;
```

### 3.4 `user_model_preference` 保险迁移

今早 hotfix 已经把大多数用户的偏好置为基础 model_key。本次补一条保险 UPDATE，防止残留：

**正确 schema 确认**：表 `user_model_preference` 的列是 `model_key VARCHAR(100)` + `thinking TINYINT(1)`（见 `migrations/20260410_000001_add_llm_routing_tables.sql:56-65` 和 `internal/pkg/model/llm.go:55-64`）。

```sql
-- 把残留的 -thinking 后缀 model_key 切换到 base 同时强制 thinking=1
-- （本 feature 把"思考开关"上移到 route 层，user 偏好里的 thinking 字段保留语义=
-- 用户选择的 base 模型 + 系统默认进入思考；若 user 主动改为 false，见 §3.4.1 合成规则）
UPDATE user_model_preference
SET model_key = REPLACE(model_key, '-thinking', ''),
    thinking  = 1
WHERE model_key LIKE '%-thinking';
```

**影响面检查（S3 plan 先跑）**：
```sql
SELECT COUNT(*) FROM user_model_preference WHERE model_key LIKE '%-thinking';
```
预期今日 < 5 行（hotfix 后几乎为 0）。

### 3.4.1 Thinking 合成规则（决策已敲定）

**规则**：adapter 只看 `route.ReasoningEffort`，`user_model_preference.thinking` 字段被彻底忽略。

**落地**：
- `biz/sop/executor.go:109` 的 `_ = thinking` 保持不变（flag 继续丢弃，意图符合）
- `aiservice.ChatRequest` 不新增 Thinking 字段
- `user_model_preference.thinking` 列事实废弃，列入未来独立 tech debt
- 矩阵视图的"思考强度"下拉是唯一控制入口

**行为矩阵**：

| route.reasoning_effort | 实际注入 | 行为 |
|----------------------|--------|------|
| `''` 或 NULL | 不发 reasoning_effort 字段 | 普通模式 |
| `'low'/'medium'/'high'/'minimal'` | `reasoning_effort: "<值>"` | 思考模式 |

（客户 2026-04-20 S2 review 时确认选方案 A，详见 §14 附录。）

### 3.5 新增 OpenRouter provider

```sql
-- llm_provider 新增一行
INSERT INTO llm_provider (name, display_name, base_url, api_key, is_active)
VALUES (
  'openrouter',
  'OpenRouter',
  'https://openrouter.ai/api/v1',
  '',  -- 空串，启动时由 SyncProviderCredentials 从 config 填充
  1
);
```

**API key 管理**：
- `config_dev.yaml` 和 `config_prod.yaml` 新增 `ai_providers.openrouter.api_key` + `base_url`
- `internal/pkg/aiservice/seed.go` 的 `providerSeedEntries` 追加 openrouter 条目
- 启动时 `SyncProviderCredentials` UPSERT 到 `llm_provider.api_key`
- **不走豁免**（与 aihubmix 字面值写 SQL 的做法相反）

**命名一致性硬约束**（S3 task 4 验收条件）：
- `llm_provider.name` 的值（migration SQL 里）**必须**等于 `providerSeedEntries[].name`（seed.go 里），严格字符串匹配 `openrouter`
- 任一处拼写偏差（如 `open-router` / `openRouter`）会导致 `SyncProviderCredentials` UPSERT 找不到行 → api_key 永远为空 → 每次调用 401
- S3 task 4 验收：启动一次 server，观察 `[aiservice] SyncProviderCredentials: completed synced=N` 的 N 相比前一次是否 +1（成功同步 openrouter）

### 3.6 新增 OpenRouter 路由（4 条 `ai_service_route`）

```sql
INSERT INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, reasoning_effort, is_active)
VALUES
  -- Claude Sonnet 4.6 via OpenRouter → 思考=medium
  ((SELECT id FROM ai_service WHERE model_key='claude-sonnet-4-6'),
   (SELECT id FROM llm_provider WHERE name='openrouter'),
   'anthropic/claude-sonnet-4.6', 1000, 'medium', 1),
  -- GPT 5.4 via OpenRouter → 思考=medium
  ((SELECT id FROM ai_service WHERE model_key='gpt-5.4'),
   (SELECT id FROM llm_provider WHERE name='openrouter'),
   'openai/gpt-5.4', 1000, 'medium', 1),
  -- Gemini 3.1 Pro Preview via OpenRouter → 思考=medium
  ((SELECT id FROM ai_service WHERE model_key='gemini-3.1-pro-preview'),
   (SELECT id FROM llm_provider WHERE name='openrouter'),
   'google/gemini-3.1-pro-preview', 1000, 'medium', 1),
  -- DeepSeek V3.2 via OpenRouter → 思考=medium
  ((SELECT id FROM ai_service WHERE model_key='deepseek-v3.2'),
   (SELECT id FROM llm_provider WHERE name='openrouter'),
   'deepseek/deepseek-v3.2', 1000, 'medium', 1);
```

### 3.7 AiHubMix 存量路由补配思考强度

今早 hotfix 原意是"默认打开深度思考"，但后端没接线。本次补好：

```sql
UPDATE ai_service_route r
  JOIN ai_service s ON s.id = r.model_id
  JOIN llm_provider p ON p.id = r.provider_id
  SET r.reasoning_effort = 'medium'
WHERE p.name = 'aihubmix'
  AND s.model_key IN (
    'claude-sonnet-4-6','gpt-5.4','gemini-3.1-pro-preview','deepseek-v3.2'
  )
  AND r.is_active = 1;
```

DMXAPI 路由不动（`reasoning_effort` 保持 NULL，failover 角色保持最小行为）。

### 3.8 OpenRouter `pricing_rule` 4 条父规则 + `pricing_rule_tier` 8 条分档子规则

**结构完全对标 AiHubMix**（同价策略，用户 S1 确认）：

```sql
-- pricing_rule 4 条（2 flat + 2 tiered_token）
INSERT INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
   is_active, created_at, updated_at)
VALUES
  ('llm_chat', 'openrouter', 'claude-sonnet-4-6', 'flat', 'call',
   21.60, 108.00, 0, 0, 21.60, 108.00, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'openrouter', 'deepseek-v3.2', 'flat', 'call',
   2.16, 3.24, 0, 0, 2.16, 3.24, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'openrouter', 'gemini-3.1-pro-preview', 'tiered_token', 'call',
   0, 0, 0, 0, 0, 0, 0, 0, 1, NOW(3), NOW(3)),
  ('llm_chat', 'openrouter', 'gpt-5.4', 'tiered_token', 'call',
   0, 0, 0, 0, 0, 0, 0, 0, 1, NOW(3), NOW(3));

-- pricing_rule_tier 8 条（Gemini × 4 + GPT × 4）
-- 见 20260420_200000_seed_aihubmix_base_pricing.sql 镜像结构
```

**完整 tier INSERT 在 migration 文件中**（结构同 aihubmix，只换 provider 名）。

### 3.9 DMXAPI 已有的无关模型不受影响

DMXAPI 的 `qwen-turbo` / `qwen3-vl-flash` / `qwen3-rerank` / `text-embedding-v4` 等路由（2026-04-19 seed 进来的）不属于本次范围，保持现状。

---

## §4 后端代码改动

### 4.1 `ResolvedRoute` 扩展 `ReasoningEffort` 字段

**文件**：`internal/pkg/aiservice/registry/registry.go:51-63`

```go
type ResolvedRoute struct {
    TaskID          string
    ServiceID       uint64
    ServiceKey      string
    DisplayName     string
    ServiceType     string
    LatencyTier     string
    QualityTier     string
    Provider        ProviderInfo
    ProviderModelID string
    // 新增：路由级思考强度。空字符串 = 不发 reasoning_effort 字段。
    ReasoningEffort string
    Capability      profile.ServiceCapability
    Pricing         PricingInfo
}
```

### 4.2 Registry SQL 查询读取 `reasoning_effort`

**文件**：`internal/pkg/aiservice/registry/store.go:305-331`（主查询）+ `store.go:411` 附近（fallback 查询）

在 SELECT 列表中追加：
```sql
r.reasoning_effort AS route_reasoning_effort
```

对应的 Go struct scan 里把 `route_reasoning_effort` → `ResolvedRoute.ReasoningEffort`。

**验证点**：registry cache 也要透传此字段（已按值拷贝，无需改动）。

### 4.3 `oaiChatRequest` 加 `ReasoningEffort` + `MaxCompletionTokens` 字段

**文件**：`internal/pkg/aiservice/adapter/adapter.go:138-150`

```go
type oaiChatRequest struct {
    Model string `json:"model"`
    Messages []oaiMessage `json:"messages"`
    // ───── 既有字段 ─────
    MaxTokens int `json:"max_tokens,omitempty"`
    Temperature float64 `json:"temperature,omitempty"`
    Stream bool `json:"stream"`
    StreamOptions *oaiStreamOptions `json:"stream_options,omitempty"`
    ResponseFormat *oaiResponseFormat `json:"response_format,omitempty"`
    // ───── 新增字段 ─────
    // 思考强度：当 route.ReasoningEffort 非空时设置。omitempty 使空串不出现在 body
    ReasoningEffort string `json:"reasoning_effort,omitempty"`
    // OpenAI 新一代推理模型专用（gpt-5.*、o1/o3/o4 系列），与 MaxTokens 互斥
    // 详见 §14.1 对 GPT 5.4 实测发现的补丁说明
    MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
}
```

**字段选择逻辑**（见 §14.1 实测证据）：

```go
// adapter/dmxapi.go Chat / ChatStream 里构造请求时
req := oaiChatRequest{Model: route.ProviderModelID, ...}
if needsMaxCompletionTokens(route.ProviderModelID) {
    req.MaxCompletionTokens = userMaxTokens  // gpt-5/o1/o3 系列
} else {
    req.MaxTokens = userMaxTokens            // 其他模型
}

func needsMaxCompletionTokens(modelID string) bool {
    return strings.Contains(modelID, "gpt-5") ||
           strings.Contains(modelID, "/o1") || strings.Contains(modelID, "/o3") ||
           strings.Contains(modelID, "/o4")
}
```

### 4.4 `oaiChatResponse` + `oaiStreamChunk` 加 `Reasoning` 字段（合并语义）

**文件**：`internal/pkg/aiservice/adapter/adapter.go:183-210`

```go
type oaiChatResponse struct {
    ID      string `json:"id"`
    Model   string `json:"model"`
    Choices []struct {
        Message struct {
            Content          string `json:"content"`
            ReasoningContent string `json:"reasoning_content"` // AiHubMix/DeepSeek/Qwen
            // 新增：OpenRouter 用 `reasoning` 字段。两字段优先级 ReasoningContent，若空则用 Reasoning。
            Reasoning string `json:"reasoning"`
        } `json:"message"`
        FinishReason string `json:"finish_reason"`
    } `json:"choices"`
    Usage *oaiUsage `json:"usage,omitempty"`
    Error *oaiError `json:"error,omitempty"`
}

type oaiStreamChunk struct {
    ID      string `json:"id"`
    Model   string `json:"model"`
    Choices []struct {
        Delta struct {
            Content          string `json:"content"`
            ReasoningContent string `json:"reasoning_content"` // AiHubMix/DeepSeek/Qwen
            // 新增：OpenRouter 流式用 `reasoning`
            Reasoning string `json:"reasoning"`
        } `json:"delta"`
        FinishReason string `json:"finish_reason"`
    } `json:"choices"`
    Usage *oaiUsage `json:"usage,omitempty"`
}
```

**合并规则**（在 adapter/stream.go 消费时应用）：

```go
// adapter/stream.go:107-122 附近
delta := choice.Delta.Content
// 合并：优先 ReasoningContent，空则 Reasoning
reasoningDelta := choice.Delta.ReasoningContent
if reasoningDelta == "" {
    reasoningDelta = choice.Delta.Reasoning
}
```

**为什么合并而不是新增 stream field**：上游 `aiservice.ChatChunk.ReasoningDelta` 已存在且前端已渲染。保持 adapter 层内部合并，不扩展对外契约，改动最小。

### 4.5 dmxapi adapter 注入 `reasoning_effort`

**文件**：`internal/pkg/aiservice/adapter/dmxapi.go:70-90` 和 `:120-143`

`Chat` 和 `ChatStream` 两个方法都在 `json.Marshal(oaiChatRequest{...})` 时增加：

```go
body, err := json.Marshal(oaiChatRequest{
    Model:           route.ProviderModelID,
    Messages:        buildOAIMessages(req.Messages),
    MaxTokens:       req.MaxTokens,
    Temperature:     req.Temperature,
    Stream:          false,  // or true
    ResponseFormat:  translateResponseFormat(req.ResponseFormat),
    ReasoningEffort: route.ReasoningEffort,  // 新增
})
```

`ReasoningEffort` 为空串时走 `omitempty`，不出现在线上 body 中，对不支持该字段的 provider 无害。

### 4.6 400 兜底保留（防御 provider 拒绝）

**文件**：`internal/pkg/aiservice/adapter/dmxapi.go` 的 `doPost` 附近

如果 provider 返回 400 且 body 含 `reasoning_effort` 或 `unknown_parameter` 关键词 → 自动去掉 `ReasoningEffort` 重试一次。与 `llm/dmxapi_client.go:297-306` 的历史实现对齐（该逻辑新 adapter 路径当前**不存在**，本次必须补齐）。

**trigger 条件必须两关键词 OR**（不同 provider 错误文案不一致）：

```go
// 简化伪代码
if status == 400 && req.ReasoningEffort != "" &&
   (strings.Contains(body, "reasoning_effort") ||
    strings.Contains(body, "unknown_parameter")) {
    log.Warnw("provider rejected reasoning_effort, retrying without",
        "route", route.Provider.Name, "model", route.ProviderModelID,
        "status", status, "body_preview", body[:min(200, len(body))])
    // 重试：ReasoningEffort 置空
    return d.doPostWithoutReasoning(ctx, route, path, originalBody)
}
```

S3 单测 `Test_400Fallback_On_ReasoningEffort_Rejected` 必须覆盖两种 body：
- `{"error":"reasoning_effort: invalid_request_error"}`（OpenRouter 风格）
- `{"error":"unknown_parameter: reasoning_effort"}`（AiHubMix proxy 风格）

### 4.7 OpenRouter alias 注册

**文件**：`internal/numind/numind.go:148-154`

```go
gateway.RegisterProviderAlias("dmxapi-ssvip", "dmxapi")
gateway.RegisterProviderAlias("aihubmix",     "dmxapi")
gateway.RegisterProviderAlias("openrouter",   "dmxapi")  // 新增
```

### 4.8 `seed.go` 追加 openrouter 凭据同步条目

**文件**：`internal/pkg/aiservice/seed.go:39-73`

```go
var providerSeedEntries = []providerSeedEntry{
    // ... 现有条目 ...
    {
        name:          "openrouter",
        cfgKeyAPIKey:  "ai_providers.openrouter.api_key",
        cfgKeyBaseURL: "ai_providers.openrouter.base_url",
    },
}
```

### 4.9 配置文件

**`config_dev.yaml`**（§128 附近 `ai_providers:` 下）：

```yaml
ai_providers:
  # ... 现有条目 ...
  openrouter:
    api_key: "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"
    base_url: "https://openrouter.ai/api/v1"
```

**`config_prod.yaml`**：同上（用户 S0 已授权明文配置）。

**豁免说明**：`CLAUDE.md §3` 禁止硬编码 API 密钥。用户已在 S0 授权 dev+prod 明文。`api_key` 在 `config_*.yaml` 明文，不进 DB migration SQL，不进代码。

### 4.10 `llm/dmxapi_client.go` 不动

遗留客户端在 `internal/` 下无任何引用（grep 已验证）。不改。

---

## §5 管理后台"矩阵视图"（前端 + admin API 扩展）

### 5.1 心智目标

消灭用户 B+F 痛点：

- 模型表只剩 4 行（base），不再看到 `-thinking` 幽灵
- 路由、定价、思考强度三件事在**一个单元格**里编辑
- 一张表说清 "模型 × 提供商 × 路由" 的三维关系

### 5.2 矩阵视图布局（Vue 3 新页面）

**路径**：`numind-admin-web/src/views/AIService/MatrixView.vue`

**数据源**：GET `/v1/admin/ai/matrix`（新增端点，见 §5.5）

**视觉框架**：

```
            ┌ OpenRouter ┬ AiHubMix ┬  DMXAPI  ┐
            │ (主 1000)  │ (备 100) │ (failover│
            │            │          │    10)   │
┌───────────┼────────────┼──────────┼──────────┤
│ Claude    │ ✅ medium  │ ✅ medium│ ❌ 未启用│
│ 4.6       │ ¥21.6/108  │ ¥21.6/108│ ¥?/?     │
│ Sonnet    │            │          │          │
├───────────┼────────────┼──────────┼──────────┤
│ GPT 5.4   │ ✅ medium  │ ✅ medium│ ❌ 未启用│
│           │ ¥18→36/..  │ ¥18→36/..│          │
├───────────┼────────────┼──────────┼──────────┤
│ Gemini    │ ✅ medium  │ ✅ medium│ ❌       │
│ 3.1 Pro   │ ¥14.4→..   │ ¥14.4→.. │          │
├───────────┼────────────┼──────────┼──────────┤
│ DeepSeek  │ ✅ medium  │ ✅ medium│ ✅ 未思考│
│ V3.2      │ ¥2.16/3.24 │ ¥2.16/... │ ¥?/?     │
└───────────┴────────────┴──────────┴──────────┘
```

**单元格信息**（3 行）：
- Row 1：状态图标（✅/❌）+ 思考强度（英文值或"未思考"）
- Row 2：价格简摘（`输入单价/输出单价` for flat；`起始档价` for tiered）
- Row 3：`点击编辑` 提示

**点击单元格** → 右侧滑出抽屉（Drawer）含完整编辑表单：

```
┌─ 编辑路由：Claude 4.6 Sonnet × OpenRouter ─┐
│                                              │
│  启用状态           [○ 启用 ● 停用]          │
│  供应商模型 ID      [anthropic/claude-...]   │
│  优先级             [1000]                   │
│                                              │
│  ─── 思考配置 ────────────────────────       │
│  模型支持思考       ✓（来自 ai_service）    │
│  思考强度           [下拉 ▼ medium]          │
│    └─ 选项：关 / low / medium / high / min   │
│    └─ 模型能力为 none 时此项锁死"关"        │
│                                              │
│  ─── 定价配置（来自 pricing_rule）───        │
│  计费模式           flat                     │
│  输入单价           21.60 元/M tok            │
│  输出单价           108.00 元/M tok           │
│  [编辑定价 →]（跳转 PricingRulesView）        │
│                                              │
│  [取消]                       [保存]         │
└──────────────────────────────────────────────┘
```

**可编辑字段**（MVP 范围）：
- `is_active` 开关
- `provider_model_id` 文本
- `priority` 数字
- `reasoning_effort` 下拉（与模型能力联动，见 §7.3）

**不编辑的字段**（跳转现有 PricingRulesView 处理）：
- `pricing_rule.*` — 价格编辑保留在现有独立页面，矩阵视图只展示不编辑

### 5.3 "新增路由"操作

矩阵空白单元格（即 "❌ 未启用"）也可点击：
- 打开同款抽屉，但表单为"新增路由"模式
- 复用现有 POST `/v1/admin/ai/services/:id/routes` 端点

### 5.4 辅助视图（保留 + 瘦身）

原 `ServicesList.vue` / `ProvidersList.vue` 保留为"侧边导航"使用：

- **模型库**列表：展示 4 行 base 模型 + 可编辑模型自身属性（display_name / icon / sort_order / 思考能力布尔组合）
- **服务商**列表：展示 5 家（含 openrouter）+ base_url / api_key / is_active 总开关
- 矩阵视图作为"AI 服务"菜单的默认落地页

**模型能力表达 UI**：单独的"思考能力"下拉（3 选 1）写入 `supports_thinking` + `thinking_only` 布尔组合（§3.2 映射表）。

### 5.5 后端 admin API 扩展

**现有端点复用** + **新增 1 个聚合端点**：

| 方法 | 端点 | 状态 | 说明 |
|------|------|------|------|
| **GET** | **`/v1/admin/ai/matrix`** | **新增** | 返回矩阵数据：`{ models: [...], providers: [...], routes: [[...]] }` |
| PUT | `/v1/admin/ai/routes/:route_id` | 扩展 | 请求体新增 `reasoning_effort` 字段支持 |
| PUT | `/v1/admin/ai/services/:id` | 扩展 | 请求体新增 `supports_thinking` + `thinking_only` 支持 |
| POST | `/v1/admin/ai/services/:id/routes` | 扩展 | 新增路由时可带 `reasoning_effort` |
| 其余 | 保持现状 | — | — |

### 5.6 矩阵 API 契约

```json
// GET /v1/admin/ai/matrix?service_type=llm
// 响应：
{
  "code": 0,
  "data": {
    "models": [
      {
        "id": 1, "model_key": "claude-sonnet-4-6",
        "display_name": "Claude 4.6 Sonnet",
        "supports_thinking": true, "thinking_only": false,
        "capability_label": "optional"
      }
    ],
    "providers": [
      { "id": 5, "name": "openrouter", "display_name": "OpenRouter", "is_active": true }
    ],
    "routes": {
      "1_5": {  // model_id "_" provider_id
        "route_id": 42,
        "provider_model_id": "anthropic/claude-sonnet-4.6",
        "priority": 1000,
        "reasoning_effort": "medium",
        "is_active": true,
        "pricing_summary": {
          "billing_mode": "flat",
          "input_display": "¥21.6/M",
          "output_display": "¥108/M"
        }
      }
    }
  }
}
```

---

## §6 Migration 清单

按执行顺序（**可幂等重跑**）：

| # | 文件名 | 内容 | 幂等性 |
|---|-------|------|--------|
| 1 | `20260420_210000_add_reasoning_effort_to_route.sql` | ALTER ai_service_route ADD COLUMN reasoning_effort | `ADD COLUMN IF NOT EXISTS` 通过 `INFORMATION_SCHEMA` check |
| 2 | `20260420_210000_seed_openrouter_provider.sql` | llm_provider + 4 ai_service_route + 5 pricing_rule + 8 pricing_rule_tier | INSERT IGNORE / NOT EXISTS 子查询 |
| 3 | `20260420_210000_collapse_thinking_variants.sql` | UPDATE base set supports_thinking=1; UPDATE -thinking set deprecated_at=NOW(); deactivate routes referencing deprecated rows | UPDATE 幂等 |
| 4 | `20260420_210000_seed_aihubmix_reasoning_effort.sql` | UPDATE aihubmix 4 base routes set reasoning_effort='medium' | UPDATE 幂等 |
| 5 | `20260420_210000_cleanup_user_model_preference.sql` | UPDATE user_model_preference REPLACE `-thinking` | 幂等（LIKE 过滤） |

**每个 migration 都配一个 `_rollback.sql`** 兄弟文件。

**执行顺序约束**：
- Migration 1 必须最先（字段要先存在才能在 2 中被 seed 赋值）
- Migration 2 在 3 之前（seed OpenRouter 先于 collapse thinking 变体）
- Migration 3 在 4 之后（先让 aihubmix 的 base 路由配置 reasoning_effort='medium'，再软禁用 -thinking 变体路由，避免"aihubmix 思考真空窗口"）
- **推荐执行顺序：1 → 2 → 4 → 3 → 5**
- **所有 5 个 migration 必须在同一次部署中原子性跑完**。禁止分批（如 CI/CD 只跑 1+2 后上线再跑 3+4+5），否则 OpenRouter 成主路由但 AiHubMix 未配 reasoning_effort，failover 场景会降级到"不思考"，用户体验打折

---

## §7 联动约束与前端校验

### 7.1 矩阵编辑保存时的后端校验

PUT `/v1/admin/ai/routes/:route_id` 接收 `reasoning_effort` 字段时：

```go
// 伪码
var allowedValues = map[string]bool{
    "":         true,  // 空串 = 关闭思考
    "low":      true,
    "medium":   true,
    "high":     true,
    "minimal":  true,
}

if _, ok := allowedValues[req.ReasoningEffort]; !ok {
    return errno.ErrBind.SetMessage("reasoning_effort 取值必须是 '' | low | medium | high | minimal")
}

// 模型能力约束
service := s.GetService(ctx, route.ModelID)
if !service.SupportsThinking && req.ReasoningEffort != "" {
    return errno.ErrBind.SetMessage(
        fmt.Sprintf("模型 %s 不支持思考能力（supports_thinking=false），不能设置 reasoning_effort", service.ModelKey))
}
if service.ThinkingOnly && req.ReasoningEffort == "" {
    return errno.ErrBind.SetMessage(
        fmt.Sprintf("模型 %s 为仅思考（thinking_only=true），reasoning_effort 不能为空", service.ModelKey))
}
```

### 7.2 前端下拉联动逻辑

Matrix 编辑抽屉中，`reasoning_effort` 下拉的可选项根据 `model.supports_thinking` + `model.thinking_only` 计算：

```typescript
function allowedEffortOptions(model: Model): string[] {
  if (!model.supports_thinking && !model.thinking_only) {
    return [''];  // 只允许"关"
  }
  if (model.thinking_only) {
    return ['low', 'medium', 'high', 'minimal'];  // 不允许"关"
  }
  // optional (supports_thinking=true, thinking_only=false)
  return ['', 'low', 'medium', 'high', 'minimal'];
}
```

### 7.3 思考能力下拉映射

模型自身属性编辑页（非矩阵）的"思考能力"下拉 3 选 1：

| 用户可见选项 | 写入 DB |
|-----------|---------|
| 不支持思考 | `supports_thinking=false, thinking_only=false` |
| 仅支持思考模式 | `supports_thinking=true, thinking_only=true` |
| 可调节（默认） | `supports_thinking=true, thinking_only=false` |

---

## §8 Langfuse Trace Topology

### 8.1 现有基础设施（不变）

按 `.claude/rules/ai-service.md` §1-3 约定：
- Trace 在 SOP/Chatbot 调用入口创建（executor.go / chatbot/stream.go 已有）
- Generation 在 `aiservice.Chat` / `ChatStream` 内部记录（已有）
- Span 用于非 LLM 子操作（prompt 构建、向量检索）

### 8.2 本次需要记录到 Generation 的字段

要求 `aiservice.Chat` / `ChatStream` 调用路径（或其 middleware）把以下字段写入 generation：

| 字段 | 值 | 用途 | 注入位置 |
|------|-----|------|---------|
| `provider_name` | `openrouter` / `aihubmix` / ... | 按 provider 过滤 | `WithGenInput` 的 input 对象子字段 |
| `provider_model_id` | `anthropic/claude-sonnet-4.6` | 区分不同 slug | 同上 |
| `reasoning_effort` | `medium` / `""` | 验证思考强度是否被注入 | 同上 |
| `reasoning_content` | 流式收集的思维链完整文本 | 审计思维链内容 | `WithGenOutput` 对象子字段 |
| `model` | 模型 key（如 `claude-sonnet-4-6`） | 已有，沿用 | `WithGenModel` |
| `prompt_tokens` / `completion_tokens` | | 已有，沿用 | `WithGenUsage(p, c)` |
| `reasoning_tokens` | 从 provider `usage.completion_tokens_details.reasoning_tokens` 提取 | **S3 task 验证现有 `langfuse.WithGenUsage` 是否支持第 3 参数** | `WithGenUsage` 或新增 helper |

**S3 Task 约定**：如果现有 `internal/pkg/langfuse` 包的 `WithGenUsage` 只有 `(promptTokens, completionTokens)` 两参数，Task 2 或 Task 3 需在该包**新增**一个 `WithGenReasoningTokens(n int)` 或扩展 `WithGenUsage(p, c, r)`，具体 API 形式留给 implementer。spec 仅约束 **"reasoning_tokens 必须能被读到"**，不约束 Go 调用签名。

**S5 验证**：打开 Langfuse UI → 搜索最新的 generation → 检查 `input.reasoning_effort='medium'` + `output.reasoning_content` 非空 + `usage.reasoning_tokens > 0`（若 provider 返回）+ `duration_ms > 0`。

### 8.3 失败分支的 trace

若调用 OpenRouter 失败 → failover 到 AiHubMix：
- 产生 2 条 generation（一条失败 error、一条成功）
- 第 2 条的 metadata.provider 必须清晰记录为 aihubmix（证明 failover 生效）

---

## §9 数据迁移 / 风险与回滚

### 9.1 数据迁移安全措施

| 迁移 | 最坏情况影响 | 回滚 |
|------|-------------|------|
| ALTER ai_service_route ADD reasoning_effort | 无（新字段 NULLABLE） | `ALTER TABLE ai_service_route DROP COLUMN reasoning_effort` |
| Seed OpenRouter provider | 新 provider + 4 routes；旧流量不改变 | 删除新插入的行 |
| Collapse thinking 变体（软删） | 用户可能失去对 `-thinking` 模型的引用 | `UPDATE ai_service SET deprecated_at=NULL WHERE model_key LIKE '%-thinking'` |
| AiHubMix 加 reasoning_effort | AiHubMix Claude 开始真实思考 → tokens 消耗↑ 30-50% | `UPDATE ... SET reasoning_effort=NULL WHERE provider='aihubmix'` |
| user_model_preference cleanup | 今日 hotfix 后预期为 0 行 | 无需回滚（LIKE 过滤保证只改残留数据） |

### 9.2 运行时风险

| 风险 | 缓解 |
|------|------|
| R1：dmxapi adapter 响应 struct 新增字段破坏现有 AiHubMix 流式解析 | `reasoning_content` 保留不动，`reasoning` 独立字段；合并规则优先前者，未命中才用后者 |
| R2：priority=1000 使 OpenRouter 成主路由；若 OpenRouter 挂掉影响全线用户 | Registry fallback 机制已有（按 priority DESC 遍历失败降级）；S5 故障注入验证 failover |
| R3：`reasoning_effort='medium'` 全铺开后成本暴增 | Claude thinking 成本约为 base 的 2-3×；若发现超预期，admin 矩阵视图一键改为 'low' 或 '' |
| R4：某些 provider_model_id 不支持 `reasoning_effort` 返回 400 | 400 兜底自动 retry without reasoning_effort（§4.6） |
| R5：OpenRouter 流式 `reasoning_details` 未处理（含 signature） | 本期不处理结构化 metadata，只解析 `reasoning` 字符串字段；signature 记入未来功能 tech debt |

### 9.3 Prod 部署前置（S6 前）

在 dev 验证通过后，S6 merge develop 之前：
- 确认 `config_prod.yaml` 的 `ai_providers.openrouter.api_key` 已就位
- 确认 prod 数据库 migration 执行顺序与 dev 一致
- **确认 prod 数据库中 `llm_compat` VIEW 已被 drop 的状态**（2026-04-17 migration 是否已到 prod）——**S3 第一个 task 开始前必做**，SSH prod 跑：
  ```sql
  SHOW FULL TABLES WHERE Table_type='VIEW' AND Tables_in_<db> LIKE 'llm_%';
  ```
  若 VIEW 仍存在，在本 feature 的 migration 之前先在 prod 补跑 `20260417_180000_drop_llm_compat_views.sql`；否则 Migration 3（软删 `-thinking` 变体）可能因 VIEW 依赖失败。

---

## §10 S5 自动验收策略

### 10.1 验收方式：**Playwright E2E + gstack /qa 混合**

**理由**：
- 后端 adapter 字段改动 → Playwright 可测 API 行为回归
- 前端矩阵视图 → gstack /qa 浏览器截图 QA
- LLM 调用端到端 → Playwright 可构造真实调用，断言思维链流式 chunk 到达

**"默认深度思考"是高风险业务逻辑**（一改值用户所有调用的行为和成本都变）→ 必须有自动回归保护 → 此项走 Playwright（`.claude/rules/ndf-enforcement.md` 规则 10）。

### 10.2 Playwright 场景（新增 `e2e/openrouter-thinking.spec.ts`）

| 场景 | 操作 | 断言 |
|------|------|------|
| OR-1 默认思考生效 | 登录 → 打开 Chatbot → 发一句话 | 收到 `reasoning_delta` 流式事件（SSE chunk）；完整 reasoning 内容非空 |
| OR-2 SOP 执行带思考 | 登录 → 执行一个 SOP 节点 | Langfuse 检查：generation.metadata.reasoning_effort=='medium'；有 reasoning_tokens |
| OR-3 API 返回 thinking 内容 | POST `/v1/chatbot/chat` | 响应 body 的 reasoning_content 字段非空 |
| OR-4 Failover 验证 | 临时将 OpenRouter api_key 置为错误值 → 发起调用 | 调用成功（降级到 AiHubMix）；Langfuse 里能看到 2 条 generation |

### 10.3 gstack /qa 场景（矩阵视图）

| 场景 | 操作 | 截图验证 |
|------|------|----------|
| QA-1 矩阵视图布局 | 登录 admin → 进入"AI 服务"菜单 | 4 行 × 3 列表格可见；无 `-thinking` 变体；列头显示优先级数字 |
| QA-2 单元格点击编辑 | 点击 Claude × OpenRouter 单元格 | 右侧抽屉出现，`reasoning_effort` 下拉可选 5 档；其他字段正确 |
| QA-3 思考能力联动 | 在"模型库"创建一个 `supports_thinking=false` 的模型 → 回矩阵编辑它的路由 | `reasoning_effort` 下拉锁死为"关" |
| QA-4 修改强度保存后生效 | 把 Claude×OpenRouter 从 `medium` 改为 `high` → 发起一次 Chatbot 调用 | Langfuse generation metadata.reasoning_effort == 'high' |

### 10.4 后端单元测试（Go）

| 测试 | 文件 | 覆盖 |
|------|------|------|
| `Test_DMXAPIAdapter_InjectsReasoningEffort` | `aiservice/adapter/dmxapi_test.go` | route.ReasoningEffort 非空 → request body 包含 `reasoning_effort` |
| `Test_DMXAPIAdapter_Omits_When_Empty` | 同上 | route.ReasoningEffort='' → request body 不含该字段（omitempty 生效） |
| `Test_StreamChunk_Merges_Reasoning_And_ReasoningContent` | `aiservice/adapter/stream_test.go` | `reasoning_content` 优先；空则 fallback `reasoning` |
| `Test_400Fallback_On_ReasoningEffort_Rejected` | `aiservice/adapter/dmxapi_test.go` | 400 + "reasoning_effort" in body → 自动 retry without reasoning_effort |
| `Test_AdminRoutePut_Rejects_Invalid_Effort` | `admin/route_test.go` | reasoning_effort="xxx" → 400 ErrBind |
| `Test_AdminRoutePut_Rejects_Effort_For_None_Model` | 同上 | supports_thinking=false 模型 → reasoning_effort 必须空 |
| `Test_RegistryStore_Reads_ReasoningEffort` | `aiservice/registry/store_test.go` | SELECT 返回的 ResolvedRoute.ReasoningEffort 正确 |

---

## §11 S3 Plan 预告（不是 S3 的产出，仅大纲）

S3 writing-plans 阶段将拆分为以下 task（预估粒度，非最终）：

1. **Task 1 — Migration SQL × 5 文件 + rollback 镜像**（1 天）
2. **Task 2 — ResolvedRoute + registry SQL + adapter 请求注入**（0.8 天）
3. **Task 2.5 — MaxCompletionTokens 分派逻辑（GPT-5/o1/o3 系列）+ 单测**（0.3 天，由 §14.1 实测驱动新增）
4. **Task 3 — adapter 响应结构合并 + 400 兜底（含 max_tokens 关键词扩展）**（0.5 天）
5. **Task 4 — `numind.go` alias + `seed.go` 条目 + config YAML**（0.3 天）
6. **Task 5 — admin API 扩展（路由 PUT 支持 reasoning_effort + matrix 端点）**（1 天）
7. **Task 6 — admin web 矩阵视图 + 抽屉编辑**（1.5 天）
8. **Task 7 — admin web 模型能力编辑（supports_thinking/thinking_only 映射）**（0.5 天）
9. **Task 8a — 后端 Go 单元测试**（0.5 天，从原 Task 8 拆出）
10. **Task 8b — Playwright E2E spec（含 GPT 5.4 fallback 场景）**（0.5 天）
11. **Task 8c — gstack /qa 浏览器场景脚本**（0.3 天）
12. **Task 9 — S5 验证策略文档 + 回归 checklist**（0.2 天）

**合计预估约 9.4 天**（含 §14.1 新增 Task 2.5 和 §14.2 前端 fallback 验证；Task 8 按 Opus review P2-5 建议拆成 8a/8b/8c）。

---

## §12 验收标准（Gate for S3 Entry）

S2 通过 gate 的条件：

- [x] Spec 覆盖 PRD 全部用户故事：§2 映射表 8/8（GPT 5.4 思维链前端 fallback 已在映射表注明）
- [x] 多仓库 API 契约定义：§5.5 + §5.6 的 admin API 合同明确
- [x] AI 功能 trace topology 定义：§8 齐全
- [x] **Opus 4.7 独立 reviewer 审核通过**（2026-04-20 PASS_WITH_CONCERNS，P0/P1/P2 全部已消化或列入 S3 task）
- [x] **客户确认设计方向**（2026-04-20 D1=A、D2=X 实测已完成）← S2 硬门禁 ✅

---

## §13 附录：术语对照

| 术语 | 说明 |
|------|------|
| **路由** | `ai_service_route` 表的一行，表达"某个模型通过某个提供商调用时的配置" |
| **思考强度** | `reasoning_effort` 值，控制模型推理深度 |
| **思考能力** | 模型自身是否支持思考（ai_service.supports_thinking + thinking_only 布尔组合） |
| **主路由** | 同模型下 priority 值最高的 active 路由（OpenRouter=1000） |
| **聚合商** | OpenRouter / AiHubMix / DMXAPI，代理多个模型原厂的服务商 |
| **OpenAI 兼容端点** | 所有聚合商使用的 `/chat/completions` 事实标准协议 |

---

---

## §14 附录：P0 决策已敲定（2026-04-20）

### 决策 D1：Thinking flag × reasoning_effort 合成规则 → **A** ✅（客户 2026-04-20 拍板）

**生效规则**：完全忽略 `user_model_preference.thinking`，adapter 只看 `route.ReasoningEffort`。

**落地改动**：
- `biz/sop/executor.go:109` 的 `_ = thinking` 保持不变（flag 继续丢弃）
- `aiservice.ChatRequest` 不加 Thinking 字段
- `user_model_preference.thinking` 列事实废弃，作为 tech debt 列入未来独立清理 feature
- 矩阵视图是管理员控制思考开关的**唯一入口**

详细方案见下文（撤销原候选 B/C 描述）。

---

### 决策 D2：AiHubMix base slug + reasoning_effort 可行性验证 → **X** ✅（2026-04-20 实测完成）

**实测条件**：
- AiHubMix base URL `https://aihubmix.com/v1/chat/completions`
- API key 来自 `config_dev.yaml`
- Payload: `{"model": "<base_slug>", "messages": [...], "max_tokens": 500, "reasoning_effort": "medium", "stream": false}`

**实测结果**：

| 模型 | base slug | HTTP | `reasoning_content` | `reasoning_tokens` | 结论 |
|------|----------|------|---------------------|-------------------|------|
| Claude 4.6 Sonnet | `claude-sonnet-4-6` | 200 | **40 字符**（非空） | — | ✅ base slug + reasoning_effort 触发思考 |
| **GPT 5.4** | `gpt-5.4` | **400** | — | — | ❌ 报错 `max_tokens not supported, use max_completion_tokens`（见 §14.1） |
| Gemini 3.1 Pro | `gemini-3.1-pro-preview` | 200 | **241 字符**（非空） | 291 | ✅ base slug + reasoning_effort 触发思考 |
| DeepSeek V3.2 | `deepseek-v3.2` | 200 | **359 字符**（非空） | 131 | ✅ base slug + reasoning_effort 触发思考 |

**结论**：
- 3/4 模型（Claude / Gemini / DeepSeek）按 spec §3.7 原方案直接可用
- **GPT 5.4 独立缺陷**：需要新增 §14.1 的补丁
- Spec §3.3（软删 `-thinking` 变体）可以照推进，不会让 AiHubMix Claude 思考能力消失（本次实测证明 base slug 够用）

### §14.1 补丁：GPT 5.4（及所有 `gpt-5.*` 思考模型）的 `max_completion_tokens` 特例

**问题**：GPT 5.x 系列要求用 `max_completion_tokens` 字段传 token 上限，**拒绝** `max_tokens`（400 `unsupported_parameter`）。这是 OpenAI 新一代推理模型（o1/o3/gpt-5）的 API 惯例，与 legacy `llm/dmxapi_client.go:267` 的 `bodyMap["max_completion_tokens"] = 1000` 逻辑对齐——但新 aiservice adapter 路径当前**不做任何 max_completion_tokens 特殊处理**。

**补充实测**（GPT 5.4 改用 `max_completion_tokens`）：
```json
{"model":"gpt-5.4","max_completion_tokens":500,"reasoning_effort":"medium"}
→ HTTP 200, content_len=4, reasoning_tokens=28, reasoning_content_len=0
```

**关键发现**：GPT 5.x **确实执行了思考**（reasoning_tokens=28），但**不返回 `reasoning_content` 字段**（OpenAI "加密推理" 策略：只计费、不暴露）。

**影响**：
- 用户 must-have "前端看到思维链" 对 GPT 5.4 **无法满足**（OpenAI 官方限制，聚合商也透传）
- 用户 must-have "Token 消耗统计" **仍满足**（reasoning_tokens 明确暴露）
- 成本统计**仍准确**（按 completion_tokens 总数计费，含 reasoning_tokens）

**补丁实施**：

1. **`oaiChatRequest` 二选一字段**（`adapter/adapter.go`）：当 `ReasoningEffort != ""` **或** 检测到 provider_model_id 是 `gpt-5.*` / `openai/gpt-5.*` / `openai/o[1-9]` 系列 → 序列化 `MaxTokens` 为 `max_completion_tokens`；否则走原 `max_tokens`

```go
type oaiChatRequest struct {
    Model string `json:"model"`
    Messages []oaiMessage `json:"messages"`
    // 按调用方式动态选择：普通模型用 MaxTokens，新一代推理模型用 MaxCompletionTokens
    MaxTokens           int `json:"max_tokens,omitempty"`
    MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
    Temperature float64 `json:"temperature,omitempty"`
    ReasoningEffort string `json:"reasoning_effort,omitempty"`
    Stream bool `json:"stream"`
    StreamOptions *oaiStreamOptions `json:"stream_options,omitempty"`
    ResponseFormat *oaiResponseFormat `json:"response_format,omitempty"`
}
```

2. **adapter.Chat / ChatStream 构造请求时的分派逻辑**：

```go
req := oaiChatRequest{
    Model: route.ProviderModelID,
    Messages: buildOAIMessages(...),
    Temperature: ...,
    ReasoningEffort: route.ReasoningEffort,
    ...
}
if needsMaxCompletionTokens(route.ProviderModelID, route.ReasoningEffort) {
    req.MaxCompletionTokens = userMaxTokens
} else {
    req.MaxTokens = userMaxTokens
}

// helper
func needsMaxCompletionTokens(modelID, effort string) bool {
    // 新一代 OpenAI 推理模型总是要求 max_completion_tokens
    if strings.Contains(modelID, "gpt-5") || strings.Contains(modelID, "/o1") ||
       strings.Contains(modelID, "/o3") || strings.Contains(modelID, "/o4") {
        return true
    }
    // 其他模型：仅启用 reasoning 时使用（与 legacy 对齐，预留未来 provider 演进）
    return effort != ""
}
```

3. **400 兜底扩展**：除 `reasoning_effort` / `unknown_parameter` 关键词外，新增 `max_tokens` 关键词 → 检测到后切换为 `max_completion_tokens` 重试一次。避免 provider 规则变化导致全线 400。

4. **Spec §11 S3 plan task 拆分调整**：Task 3（adapter 响应结构合并）之前增加 **Task 2.5：MaxCompletionTokens 分派逻辑 + 单测**（约 0.3 天，9 天总估算 → 9.3 天）。

### §14.2 前端思维链展示的 per-model 差异说明

Spec §2 "思维链前端展示" 实际情况：

| 模型 | 前端能看到思维链？ | 依据 |
|------|-----------------|------|
| Claude 4.6 Sonnet | ✅ | 实测 reasoning_content 非空 |
| GPT 5.4 | ❌ **仅显示 token 统计** | OpenAI 加密推理策略，aggregator 透传空字符串 |
| Gemini 3.1 Pro | ✅ | 实测 reasoning_content 非空 |
| DeepSeek V3.2 | ✅ | 实测 reasoning_content 非空 |

**前端处理**：思维链区块在内容为空时自动隐藏（按钮显示 "思考已启用（X tokens）" 作为 fallback，给用户透明度但不虚假显示思考文本）。S5 QA 必须验证这个 fallback 行为。

---

### 原候选 D1（B/C）/ D2（Y/Z）描述已归档到 git history，本次 spec 不再保留

**背景**：
- `user_model_preference.thinking bool`：用户级意图（今早 hotfix 强制置 `true`）
- `ai_service_route.reasoning_effort`（新增）：路由级强度配置
- `biz/sop/executor.go:109` 明确 `_ = thinking`（flag 当前被丢弃）

**问题**：本 feature 把思考的主控权上移到 route 层之后，user pref.thinking 还起什么作用？

**候选方案**：

**A. 路由级为准，完全忽略 user pref.thinking**（推荐）
- Adapter 只看 `route.ReasoningEffort`，user pref.thinking 字段保留但不再被任何路径读取
- admin 矩阵视图的"思考强度"是唯一真相
- Executor 第 109 行的 `_ = thinking` 保持不变（flag 依然丢弃）
- 意义：管理员全权控制；用户不能单独关闭（符合用户 must-have "默认深度思考 ON"）
- 副作用：pref.thinking 字段事实死亡，作为 tech debt 列入后续清理

**B. User pref 可覆写为关闭**
- 若 user pref.thinking=false → 强制 reasoning_effort=''（无论 route 配什么都不思考）
- 若 user pref.thinking=true → 用 route.ReasoningEffort
- Executor 需修 `_ = thinking`，让 thinking flag 流过 adapter
- 意义：保留用户降级权（如用户自己想省 token）
- 副作用：需要改 ChatRequest struct 加 `Thinking` 字段，scope 扩大 ~0.5 天

**C. User pref 可覆写为开启但不能关闭**
- user pref.thinking=true 且 route.reasoning_effort='' → 强制注入 'medium'（用户想思考但 admin 关了）
- user pref.thinking=false → 无效，仍按 route 配置走
- 意义：用户可"升级"但不能"降级"
- 复杂度最高，不推荐

**我的推荐**：**A**。
理由：
1. 用户明确"默认深度思考 ON，用户不能改回普通模式"——B 和 C 都允许用户绕过，与原意相悖
2. A 的实施最简单（不改 ChatRequest，不改 executor）
3. pref.thinking 字段列入 tech debt，留给未来独立清理 feature
4. 矩阵视图的管理员自主权清晰

---

### 决策 D2：AiHubMix base slug + reasoning_effort 可行性验证

**背景**：
- 当前 AiHubMix 用 `-think` 后缀 provider_model_id（如 `claude-sonnet-4-6-think`）激活 Claude thinking，是历史固有方式
- Spec §3.7 假设 AiHubMix base slug（无 `-think`）+ `reasoning_effort='medium'` 也能触发思考
- **此假设未经真实 API 验证**。若不成立，§3.7 的 UPDATE 是空操作，AiHubMix Claude 的思考能力在 `-thinking` 变体软删后反而消失

**候选方案**：

**X. 实测后决定**（推荐）
- S3 task 1 开始前跑一次 curl 到 `https://aihubmix.com/v1/chat/completions`，用 base slug `claude-sonnet-4-6` + `reasoning_effort='medium'`，检查响应含 `reasoning_content` 非空
- 我（主控 AI）可以代跑，需要你授权 api_key 来源（`config_*.yaml` 或直接给）

**Y. 保守路径：AiHubMix 保留 `-think` 后缀混搭**
- AiHubMix × Claude：`provider_model_id='claude-sonnet-4-6-think'` + `reasoning_effort=NULL`（用后缀激活）
- AiHubMix × GPT/Gemini/DeepSeek：base slug + `reasoning_effort='medium'`（根据 aihubmix "统一推理协议"文档，非 Claude 应支持）
- 需要同步调整：
  - Spec §3.3 保留 aihubmix Claude -thinking 路由 active（仅软删 claude-sonnet-4-6-thinking 的 **ai_service** 行，同时把 aihubmix base 路由的 provider_model_id 改为 `claude-sonnet-4-6-think`）
  - Spec §3.7 UPDATE 排除 claude 行，只改 3 条
- 好处：零风险（历史行为保持）
- 坏处：admin 矩阵视图 aihubmix × Claude 单元格"思考强度"字段显示"关"但实际会思考 → 需要 UI tooltip 说明"此路由通过 provider_model_id 后缀固定启用思考"

**Z. 激进路径：信任 AiHubMix 文档**
- 按 spec 现状，所有 aihubmix 4 路由用 base slug + `reasoning_effort='medium'`
- 若实际不生效，靠 400 兜底让 Claude 降级到不思考（差于历史状态）
- 不推荐

**我的推荐**：**X**。
理由：
1. 实测 5 分钟内完成，风险扫除
2. 若实测通过 → spec 不改；实测失败 → 改为 Y 方案
3. 依赖文档不如依赖事实

---

### 请客户回复

**D1 选**：A / B / C
**D2 选**：X / Y / Z（如选 X，确认我用 `config_dev.yaml` 里的 aihubmix api_key 代跑实测）

---

*Last Updated: 2026-04-20 S2 (Opus review 修订)*
