# aihubmix-protocol-audit — S2 Design Spec

> **Feature**: aihubmix-protocol-audit
> **NDF 阶段**: S2 — 详细设计
> **作者**: AI (Claude Opus 4.7)
> **日期**: 2026-04-20
> **S1 输入**: [requirement](../../../requirements/aihubmix-protocol-audit.md) + [proposal v2](../../../proposals/aihubmix-protocol-audit-proposal.md)
> **T2 调研依据**: [AiHubMix 协议权威参考](../../aihubmix-protocol-reference.md)（564 行，含 12 个 raw JSON 样本）

---

## §1 Scope 与目标

### 1.1 要解决的问题

2026-04-20 hotfix 把 SOP/Chatbot "深度思考"按钮藏起来让默认 ON，但后端 `executor.go:109` 和 `chatbot/stream.go:177` 把 `thinking` 参数**直接丢弃** (`_ = thinking`)。用户以为在思考，实际 AiHubMix 调用完全没启用思考模式。

同时运行时发现 `ai_service` 表中 `supports_thinking` / `thinking_only` 7/8 AiHubMix 路由行值错误——导致 `llmrouter/preference.go:246` 对合法 thinking 请求硬拒。这是线上 user-facing bug。

### 1.2 本期交付

1. **T1 代码修正** — thinking flag 从用户 preference 真实传递到出站 HTTP 请求
2. **T2 协议权威化** — `docs/aihubmix-protocol-reference.md` 已完成（S2 研究阶段产出）
3. **DB 标志校正** — migration 修正 7 行错误值
4. **响应端 wire 补全** — `reasoning_tokens` 字段解码，补全 billing + Langfuse 对思考 token 的可见性
5. **Langfuse observability 增强** — generation 记录 adapter 解析的 `resolved_reasoning_effort`
6. **计费对账 spike** — 文档化 reasoning_tokens 计价实测方法

### 1.3 不在本期（deferred）

- OpenRouter provider 接入（deferred feature）
- 管理端矩阵视图（deferred feature）
- `reasoning_effort` 值用户/admin 可调（本期硬编码 `medium`）
- `pricing_rule` 调整（spike 产出后如发现偏差，独立 hotfix）
- 将 AiHubMix 凭据从硬编码 SQL 迁移到 SyncProviderCredentials（独立 tech debt）

---

## §2 基于 T2 实测的架构校正

S1 proposal 的若干假设被 T2 curl 推翻，本 S2 以 T2 实测为准：

### 2.1 Claude 的 reasoning_tokens 不存在

**T2 实测**：Claude via AiHubMix 在 `usage` 顶层无 `reasoning_tokens`，`completion_tokens_details` 对象也不存在。思考 token 静默折叠进 `completion_tokens`。

**影响**：
- `oaiUsage.ReasoningTokens` 对 Claude 恒为 0，但思考内容正常（`message.reasoning_content` 有）
- Langfuse generation 对 Claude 调用记录 `reasoning_tokens=0` 是事实，非 bug
- 计费方面，AiHubMix 已把 reasoning token 并入 completion_tokens，`pricing_rule` 不需调整（可靠验证属 Q6=A spike）

### 2.2 Gemini 3.1 Pro Preview 是 intrinsic-only

**T2 实测**：`reasoning_effort=none` 和 `minimal` 均返回 400：`"Thinking level MINIMAL is not supported for this model"`。`low/medium/high` 均成功，思考强度可调但**无法关闭**。

**影响**：
- S1 proposal 假设的 Gemini base `thinking_only=1 → 改 0` 方向错误。**维持 (1, 1)**
- 用户 pref `thinking=false` + 路由到 Gemini base：adapter 不注入 `reasoning_effort`，但思考仍发生（AiHubMix 侧默认 medium 强度）。是硬性限制，文档说明
- 管理端可考虑把 Gemini thinking 开关 UI 禁用（标记 intrinsic）—— 但本期前端零改动原则，不做

### 2.3 `-think` 后缀变体只对 Claude/DeepSeek 有效

**T2 实测**：
- `claude-sonnet-4-6-think` ✅ 200
- `deepseek-v3.2-think` ✅ 200
- `gemini-3.1-pro-preview-think` ❌ 400（"Incorrect model ID"）
- `gpt-5.4-think` ❌ 400（同上）

**影响**：dev DB 查询结果（id=15/17）使用 base slug 而非 `-think`，实际可用：
- id=15 `gemini-3.1-pro-preview-thinking` 路由 → `provider_model_id='gemini-3.1-pro-preview'` → Gemini 原生思考触发 ✅
- id=17 `gpt-5.4-thinking` 路由 → `provider_model_id='gpt-5.4'` → GPT 默认 reasoning 触发 ✅

所有 8 行路由实际可用，标志校正 migration 不删除任何行。

### 2.4 GPT 5.4 CoT 完全隐藏

**T2 实测**：`reasoning_effort=medium` 时 `usage.completion_tokens_details.reasoning_tokens` 非零，但 `message.reasoning_content` 字段**不存在**、SSE `delta.reasoning_content` 也**永远不发**。OpenAI 加密推理策略。

**影响**：
- 前端对 GPT 5.4 不会收到 `thinking` 事件（已默认 ON 下）
- Langfuse trace 对 GPT 5.4 记录 `reasoning_content=""` + `reasoning_tokens > 0` — 可信号 "思考发生但内容不可见"
- 产品层面接受，可选加 UI tooltip "该模型思考内容不对外展示"（本期不做）

### 2.5 最终 migration 标志映射表

基于 T2 证据，校正 `ai_service` 行最终状态：

| id | model_key | provider_model_id | 当前 (supports, only) | 目标 (supports, only) | 语义 | 依据 |
|----|-----------|-------------------|----------------------|----------------------|------|------|
| 1 | claude-sonnet-4-6 | claude-sonnet-4-6 | (1, 0) | **(1, 0)** ✅ keep | optional | T2 证实 effort=none 可关 |
| 5 | claude-sonnet-4-6-thinking | claude-sonnet-4-6-think | (0, 0) ❌ | **(1, 1)** fix | intrinsic | -think 变体固定思考 |
| 12 | gemini-3.1-pro-preview | gemini-3.1-pro-preview | (1, 1) | **(1, 1)** ✅ keep | intrinsic | T2 证实不可关闭 |
| 13 | deepseek-v3.2 | deepseek-v3.2 | (1, 1) ❌ | **(1, 0)** fix | optional | T2 证实 effort=none 可关（reasoning_tokens 字段消失）|
| 14 | gpt-5.4 | gpt-5.4 | (1, 1) ❌ | **(1, 0)** fix | optional | T2 证实可通过 effort 控制 |
| 15 | gemini-3.1-pro-preview-thinking | gemini-3.1-pro-preview | (0, 0) ❌ | **(1, 1)** fix | intrinsic | 路由 base slug + 原生思考 |
| 16 | deepseek-v3.2-thinking | deepseek-v3.2-think | (0, 0) ❌ | **(1, 1)** fix | intrinsic | -think 变体固定思考 |
| 17 | gpt-5.4-thinking | gpt-5.4 | (0, 0) ❌ | **(1, 1)** fix | intrinsic | 路由同 base，约定固定思考 |

总计 **6 行需修正**（id 5, 13, 14, 15, 16, 17）。

---

## §3 架构设计

### 3.1 端到端数据流

```
User toggles thinking (前端 store; 当前按 hotfix 默认 ON)
  ↓
user_model_preference.thinking  (DB: 已存在)
  ↓
llmrouter/preference.go:GetPreferences  (读 pref)
  ↓
SOP executor.go / chatbot stream.go:  thinking bool 参数
  ↓ (本期修复：删 _ = thinking，塞进 ChatRequest.Thinking)
aiservice.ChatRequest.Thinking  (本期新增字段)
  ↓ (tracing middleware 捕获 req.Thinking)
adapter/dmxapi.go:Chat/ChatStream
  ↓ (按 route.SupportsThinking + model family 决定注入 reasoning_effort)
AiHubMix /v1/chat/completions
  ↓ (按 T2 实测返回 reasoning_content + reasoning_tokens)
adapter parses SSE/JSON → ChatResponse/ChatChunk
  ↓ (本期新增：ResolvedReasoningEffort 字段回传给 tracing middleware)
SOP/Chatbot → 前端 thinking/token events
```

### 3.2 Thinking flag → reasoning_effort 决策表

Adapter 收到 `req.Thinking` + 路由 `SupportsThinking/ThinkingOnly` 后：

| req.Thinking | route.SupportsThinking | route.ThinkingOnly | 注入 reasoning_effort？ | 语义 |
|-------------|----------------------|--------------------|-----------------------|------|
| false | * | * | **不注入** | 用户关思考 → 不发 effort |
| true | false | false | **不注入** | 模型不支持思考 → 忽略 pref |
| true | true | false | **medium** | optional 模型：发 effort 启用思考 |
| true | true | true | **不注入** | intrinsic 模型：思考自动发生，effort 可选（Gemini 对 minimal 400，稳妥不发）|

### 3.3 Model family 分派（独立于 Thinking 决策）

GPT-5 / o1 / o3 / o4 系列**必须**用 `max_completion_tokens`，否则 400。无论 Thinking 开关都生效。

```
inferModelFamily(providerModelID) → ModelFamily
```

| provider_model_id 前缀 | ModelFamily | max_tokens 字段 | 默认温度特殊约束 |
|----------------------|-------------|----------------|----------------|
| `gpt-5` / `gpt-5-` / `gpt-5.` / `o1` / `o1-` / `o3` / `o3-` / `o4` / `o4-` | `openai-reasoning` | `max_completion_tokens` | 无 |
| `claude-sonnet-4-6-think`（完整匹配）| `claude-thinking-suffix` | `max_tokens` | temp 由 AiHubMix 强制 1（caller 值被忽略）|
| `claude-` | `claude` | `max_tokens` | Thinking=true 时 adapter **强制 temp=1**（对齐 -think 变体，Q4=A）|
| `gemini-` | `gemini` | `max_tokens` | 无 |
| `deepseek-` | `deepseek` | `max_tokens` | 无 |
| 其他（qwen-*, 等）| `generic` | `max_tokens` | 无 |

**注意**：匹配顺序是先精确后前缀。`claude-sonnet-4-6-think` 完整匹配在前，避免被更通用的 `claude-` 前缀吞掉。

### 3.4 Langfuse tracing 观测信号传递（P1-4 方案选择）

**S1 P1-4 三个方案重评**（基于 Agent B 的时序分析）：
- (a) **ChatResponse.Metadata**：✅ 选择。adapter 把解析结果写回 response；middleware 拦截 response 时读取
- (b) context value side-channel：次选，额外语义耦合
- (c) downgrade 只记 Thinking bool：丢失 `resolved_reasoning_effort` 信号

**决策**：采用 (a)。给 `ChatResponse` 和 `ChatChunk`（final chunk）加：

```go
// TraceMetadata 是 adapter 在响应中回传的调试元数据，给 tracing middleware 记入 Langfuse
type TraceMetadata struct {
    // ResolvedReasoningEffort 是 adapter 实际注入请求的 reasoning_effort 值（"" 表示未注入）
    ResolvedReasoningEffort string `json:"resolved_reasoning_effort,omitempty"`
    // ResolvedModelFamily 是 adapter.inferModelFamily 的推断结果
    ResolvedModelFamily string `json:"resolved_model_family,omitempty"`
    // TempOverridden 为 true 表示 adapter 静默覆盖了 caller temperature（Claude base + Thinking=true 场景）
    TempOverridden bool `json:"temp_overridden,omitempty"`
}
```

加在 `ChatResponse.TraceMetadata *TraceMetadata` 和 `ChatChunk.TraceMetadata *TraceMetadata`（IsFinal=true 时填充）。

**Tracing middleware 改动**：`middleware/tracing.go` 在响应到达后补 `langfuse.UpdateGeneration` 调用，追加 metadata 字段。这样 input 仍记 `req.Thinking`，output 补 resolved 值。

---

## §4 Code Change Spec（逐文件）

### 4.1 `internal/pkg/aiservice/types.go`

**改动 1：ChatRequest 增加 Thinking 字段**（L122-141）

```go
type ChatRequest struct {
    Messages         []ChatMessage       `json:"messages"`
    MaxTokens        int                 `json:"max_tokens,omitempty"`
    Temperature      float64             `json:"temperature,omitempty"`
    Tools            []Tool              `json:"tools,omitempty"`
    ModelOverride    string              `json:"model_override,omitempty"`
    ResponseFormat   ResponseFormatType  `json:"response_format,omitempty"`
    // Thinking requests the model to enable reasoning/chain-of-thought.
    // Adapter is responsible for translating this to the provider-specific
    // parameter (e.g. AiHubMix uses reasoning_effort="medium").
    // For intrinsic-thinking models (thinking_only=true), this flag is
    // advisory only — thinking always happens.
    Thinking         bool                `json:"thinking,omitempty"`
}
```

**改动 2：ChatResponse 增加 TraceMetadata**

```go
type ChatResponse struct {
    Content          string         `json:"content"`
    ReasoningContent string         `json:"reasoning_content,omitempty"`
    ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
    FinishReason     string         `json:"finish_reason,omitempty"`
    Usage            TokenUsage     `json:"usage"`
    Model            string         `json:"model"`
    Provider         string         `json:"provider"`
    // TraceMetadata carries adapter-resolved values for observability layers.
    // Nil when no resolution occurred (e.g. non-Chat adapters).
    TraceMetadata    *TraceMetadata `json:"trace_metadata,omitempty"`
}
```

**改动 3：ChatChunk 增加 TraceMetadata**（仅 IsFinal=true 时填充）

```go
type ChatChunk struct {
    Delta          string         `json:"delta"`
    ReasoningDelta string         `json:"reasoning_delta,omitempty"`
    Index          int            `json:"index"`
    FinishReason   string         `json:"finish_reason,omitempty"`
    IsFinal        bool           `json:"is_final"`
    Usage          *TokenUsage    `json:"usage,omitempty"`
    Model          string         `json:"model,omitempty"`
    Provider       string         `json:"provider,omitempty"`
    TraceMetadata  *TraceMetadata `json:"trace_metadata,omitempty"`
}
```

**改动 4：新增 TraceMetadata 类型**（放在 types.go 末尾）

```go
type TraceMetadata struct {
    ResolvedReasoningEffort string `json:"resolved_reasoning_effort,omitempty"`
    ResolvedModelFamily     string `json:"resolved_model_family,omitempty"`
    TempOverridden          bool   `json:"temp_overridden,omitempty"`
}
```

### 4.2 `internal/pkg/aiservice/adapter/model_family.go`（新文件）

```go
package adapter

import "strings"

type ModelFamily string

const (
    ModelFamilyOpenAIReasoning    ModelFamily = "openai-reasoning"
    ModelFamilyClaudeThinkingSlug ModelFamily = "claude-thinking-suffix"
    ModelFamilyClaude             ModelFamily = "claude"
    ModelFamilyGemini             ModelFamily = "gemini"
    ModelFamilyDeepSeek           ModelFamily = "deepseek"
    ModelFamilyGeneric            ModelFamily = "generic"
)

// InferModelFamily returns the model family based on provider_model_id.
// Matching order: exact → longest-prefix → generic fallback.
// Exported for unit tests.
func InferModelFamily(providerModelID string) ModelFamily {
    // Exact match: Claude -think suffix variants (highest priority)
    if strings.HasSuffix(providerModelID, "-think") {
        if strings.HasPrefix(providerModelID, "claude-") {
            return ModelFamilyClaudeThinkingSlug
        }
    }
    // Prefix match: OpenAI reasoning series
    for _, prefix := range []string{"gpt-5", "o1", "o3", "o4"} {
        if strings.HasPrefix(providerModelID, prefix) {
            return ModelFamilyOpenAIReasoning
        }
    }
    // Other prefixes
    switch {
    case strings.HasPrefix(providerModelID, "claude-"):
        return ModelFamilyClaude
    case strings.HasPrefix(providerModelID, "gemini-"):
        return ModelFamilyGemini
    case strings.HasPrefix(providerModelID, "deepseek-"):
        return ModelFamilyDeepSeek
    default:
        return ModelFamilyGeneric
    }
}
```

单测 `model_family_test.go`：
- `gpt-5.4`, `gpt-5-preview`, `o1-preview`, `o3-mini`, `o4-turbo` → openai-reasoning
- `claude-sonnet-4-6` → claude
- `claude-sonnet-4-6-think` → claude-thinking-suffix（不被 `claude-` 通用分支吞掉）
- `gemini-3.1-pro-preview`, `gemini-3.1-pro-preview-think` → gemini（think 后缀对 gemini 非精确匹配路径）
- `deepseek-v3.2`, `deepseek-v3.2-think` → deepseek
- `qwen-turbo`, `text-embedding-v4` → generic

### 4.3 `internal/pkg/aiservice/adapter/adapter.go`

**改动 1：oaiChatRequest 扩展字段**（~L138-150）

```go
type oaiChatRequest struct {
    Model              string              `json:"model"`
    Messages           []oaiMessage        `json:"messages"`
    MaxTokens          int                 `json:"max_tokens,omitempty"`
    // MaxCompletionTokens is required for OpenAI reasoning family (gpt-5/o1/o3/o4).
    // When set, MaxTokens should be left 0 (server rejects both being set).
    MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
    Temperature        float64             `json:"temperature,omitempty"`
    Stream             bool                `json:"stream,omitempty"`
    StreamOptions      *oaiStreamOptions   `json:"stream_options,omitempty"`
    ResponseFormat     *oaiResponseFormat  `json:"response_format,omitempty"`
    // ReasoningEffort gates thinking mode for providers that accept this field.
    // Empty = not sent. Values: "low", "medium", "high". Avoid "none"/"minimal"
    // unless provider is known to accept them (Gemini rejects both).
    ReasoningEffort    string              `json:"reasoning_effort,omitempty"`
}
```

**改动 2：oaiUsage 扩展 reasoning_tokens**（~L212-217）

```go
type oaiUsage struct {
    PromptTokens      int                       `json:"prompt_tokens"`
    CompletionTokens  int                       `json:"completion_tokens"`
    TotalTokens       int                       `json:"total_tokens"`
    // CompletionTokensDetails contains nested reasoning_tokens for OpenAI family.
    // DeepSeek/Qwen use flat ReasoningTokens; Claude has neither.
    // See docs/aihubmix-protocol-reference.md §5 for per-provider evidence.
    CompletionTokensDetails *oaiCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
    // ReasoningTokens (flat) for DeepSeek/Qwen. Takes precedence if set.
    ReasoningTokens   int                       `json:"reasoning_tokens,omitempty"`
}

type oaiCompletionTokensDetails struct {
    ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}
```

**改动 3：新增辅助函数 extractReasoningTokens**

```go
// extractReasoningTokens returns the provider's reasoning_tokens count,
// handling the two wire path conventions (OpenAI nested / DeepSeek flat).
// Returns 0 when neither is present (e.g. Claude).
func (u *oaiUsage) extractReasoningTokens() int {
    if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens > 0 {
        return u.CompletionTokensDetails.ReasoningTokens
    }
    return u.ReasoningTokens
}
```

### 4.4 `internal/pkg/aiservice/adapter/dmxapi.go`

**改动：Chat/ChatStream 注入 reasoning_effort + max_completion_tokens 分派 + 温度覆盖 + TraceMetadata 回传**

伪代码（具体行号实现阶段确认）：

```go
func (a *DMXAPIAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
    family := InferModelFamily(route.ProviderModelID)
    oaiReq, traceMeta := a.buildOAIRequest(route, req, family, /*stream=*/false)

    // ... existing HTTP call logic ...

    resp := &aiservice.ChatResponse{ /* ... */ }
    resp.TraceMetadata = traceMeta
    resp.Usage.ReasoningTokens = oaiResp.Usage.extractReasoningTokens()
    return resp, nil
}

func (a *DMXAPIAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
    family := InferModelFamily(route.ProviderModelID)
    oaiReq, traceMeta := a.buildOAIRequest(route, req, family, /*stream=*/true)
    // ... pipe traceMeta to runOAIStream so it can stamp the final chunk ...
}

// buildOAIRequest centralizes per-family dispatch logic.
func (a *DMXAPIAdapter) buildOAIRequest(
    route *registry.ResolvedRoute,
    req aiservice.ChatRequest,
    family ModelFamily,
    stream bool,
) (oaiChatRequest, *aiservice.TraceMetadata) {
    meta := &aiservice.TraceMetadata{
        ResolvedModelFamily: string(family),
    }

    oaiReq := oaiChatRequest{
        Model:       route.ProviderModelID,
        Messages:    convertMessages(req.Messages),
        Temperature: req.Temperature,
        Stream:      stream,
    }

    // Max tokens dispatch (independent of Thinking flag)
    if family == ModelFamilyOpenAIReasoning {
        oaiReq.MaxCompletionTokens = req.MaxTokens // or sensible default
    } else {
        oaiReq.MaxTokens = req.MaxTokens
    }

    // Reasoning effort gating (uses route flags from §3.2 decision table)
    if req.Thinking && route.SupportsThinking && !route.ThinkingOnly {
        oaiReq.ReasoningEffort = "medium"
        meta.ResolvedReasoningEffort = "medium"
    }

    // Claude base + Thinking=true → force temp=1 (Q4=A)
    if family == ModelFamilyClaude && req.Thinking && req.Temperature != 1 {
        oaiReq.Temperature = 1
        meta.TempOverridden = true
    }

    // ResponseFormat passthrough (unchanged)
    if req.ResponseFormat != "" {
        oaiReq.ResponseFormat = &oaiResponseFormat{Type: string(req.ResponseFormat)}
    }

    return oaiReq, meta
}
```

### 4.5 `internal/pkg/aiservice/adapter/stream.go`

**改动：runOAIStream 透传 reasoning_tokens + 接收 traceMeta 填 final chunk**

修改签名：

```go
func runOAIStream(
    r io.ReadCloser,
    ch chan<- aiservice.ChatChunk,
    provider string,
    defaultModel string,
    traceMeta *aiservice.TraceMetadata, // 新增参数
)
```

在现有 L94-98 `aiservice.TokenUsage` 构造处：

```go
usage := &aiservice.TokenUsage{
    PromptTokens:     chunk.Usage.PromptTokens,
    CompletionTokens: chunk.Usage.CompletionTokens,
    TotalTokens:      chunk.Usage.TotalTokens,
    ReasoningTokens:  chunk.Usage.extractReasoningTokens(), // 新增
}
```

在 final chunk 发射处（~L145-152）：

```go
ch <- aiservice.ChatChunk{
    IsFinal:       true,
    FinishReason:  finishReason,
    Usage:         usage,
    Model:         modelName,
    Provider:      provider,
    TraceMetadata: traceMeta, // 新增
}
```

### 4.6 `internal/pkg/aiservice/registry/registry.go`

**改动：ResolvedRoute 扩展**（L51-63）

```go
type ResolvedRoute struct {
    TaskID            string
    ServiceID         uint64
    ServiceKey        string
    DisplayName       string
    ServiceType       string
    LatencyTier       string
    QualityTier       string
    Provider          ProviderInfo
    ProviderModelID   string
    Capability        profile.ServiceCapability
    Pricing           PricingInfo
    // SupportsThinking is true when the model can produce chain-of-thought.
    // Mirrors ai_service.supports_thinking column.
    SupportsThinking  bool
    // ThinkingOnly is true when the model always thinks (intrinsic reasoning
    // not controllable via reasoning_effort=none). Adapter should skip
    // reasoning_effort injection for these models to avoid provider-specific
    // 400 errors (e.g. Gemini rejects "minimal"/"none").
    ThinkingOnly      bool
}
```

### 4.7 `internal/pkg/aiservice/registry/store.go`

**改动 1：两个 SELECT 查询补列**（~L306-332 GetResolvedRoute + ~L395+ GetResolvedRouteByModelKey）

加两列 `s.supports_thinking, s.thinking_only`。

```sql
SELECT
  s.id, s.model_key, s.display_name, s.service_type, s.capability_json,
  s.latency_tier, s.quality_tier, s.deprecated_at, s.is_active,
  s.supports_thinking, s.thinking_only,            -- 新增
  p.id, p.name, p.base_url, p.api_key,
  r.provider_model_id, r.priority, r.is_active
FROM ai_service s
JOIN ai_service_route r ON ...
JOIN llm_provider p ON ...
WHERE ...
ORDER BY r.priority DESC
LIMIT 1
```

**改动 2：resolvedRouteRow 对齐**（L85-102）

```go
type resolvedRouteRow struct {
    ServiceID         uint64
    ModelKey          string
    DisplayName       string
    ServiceType       string
    CapabilityJSON    model.JSONMap
    LatencyTier       string
    QualityTier       string
    DeprecatedAt      *time.Time
    IsActive          bool
    SupportsThinking  bool     // 新增
    ThinkingOnly      bool     // 新增
    ProviderID        uint64
    ProviderName      string
    ProviderBaseURL   string
    ProviderAPIKey    string
    ProviderModelID   string
    RoutePriority     int
    RouteIsActive     bool
}
```

**改动 3：GORM Scan struct 对齐**（L333-350 raw query struct）

同步加两字段。

**改动 4：ResolvedRoute 构造处透传**（ResolveTask + ResolveByModelKey）

```go
route := &ResolvedRoute{
    // ... existing fields ...
    SupportsThinking: row.SupportsThinking,
    ThinkingOnly:     row.ThinkingOnly,
}
```

### 4.8 `internal/pkg/aiservice/middleware/tracing.go`

**改动：响应后补 generation metadata**

现有流程（L60-80 概念）：
1. `safeInput(req)` 记录请求（已捕获 `req.Thinking`）
2. 调用 next handler 拿 response
3. `safeOutput(resp)` 记录响应

新增：在 3 之后，如果 `resp.TraceMetadata != nil`，调用 `langfuse.UpdateGeneration` 追加 metadata：

```go
if cr, ok := resp.(*aiservice.ChatResponse); ok && cr.TraceMetadata != nil {
    langfuse.UpdateGenerationMetadata(genID, map[string]interface{}{
        "resolved_reasoning_effort": cr.TraceMetadata.ResolvedReasoningEffort,
        "resolved_model_family":     cr.TraceMetadata.ResolvedModelFamily,
        "temp_overridden":           cr.TraceMetadata.TempOverridden,
    })
}
```

**流式情况**：ChatStream 返回 channel。tracing middleware 需拦截 channel，在消费完所有 chunk 后（发现 IsFinal=true 的 chunk）捕获其 `TraceMetadata` 写入。具体实现细节放 S4。

### 4.9 `internal/numind/biz/sop/executor.go`

**改动 1：删除 L109 的 _ = thinking**

```go
func (e *SopExecutor) ExecuteNodeStream(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {
    // Gateway 路径：用户选择了特定模型
    if modelKey != "" {
        return e.executeViaGateway(ctx, node, input, history, modelKey, thinking, handler)  // 新增 thinking 参数
    }
    // ...
}
```

**改动 2：executeViaGateway 接收 thinking + L463-467 传入 ChatRequest**

```go
func (e *SopExecutor) executeViaGateway(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {
    // ... existing setup ...
    req := aiservice.ChatRequest{
        Messages:      aiMessages,
        Temperature:   0.7,
        ModelOverride: modelKey,
        Thinking:      thinking, // 新增
    }
    ch, err := aiservice.ChatStream(ctx, taskID, req)
    // ...
}
```

### 4.10 `internal/numind/biz/chatbot/stream.go`

**改动：删 L177 的 _ = thinking + L172-176 塞入 Thinking**

```go
gatewayReq := aiservice.ChatRequest{
    Messages:      aiMessages,
    Temperature:   0.7,
    ModelOverride: modelKey,
    Thinking:      thinking, // 新增
}
ch, llmErr := aiservice.ChatStream(ctx, profile.ChatbotStream, gatewayReq)
```

---

## §5 Migration Spec

### 5.1 `migrations/20260421_000001_fix_ai_service_thinking_flags.sql`

```sql
-- Migration: 20260421_000001_fix_ai_service_thinking_flags.sql
-- Feature:   aihubmix-protocol-audit (S4 Task 7)
-- Date:      2026-04-21
--
-- 背景：2026-04-20 dev DB 查询发现 ai_service 表 8 行 AiHubMix 路由中
-- 6 行的 supports_thinking / thinking_only 值错误。根因：
-- migrations/20260416_100000_seed_aihubmix_provider.sql 写入时未按 T2
-- 协议实测结果赋值，且 supports_thinking/thinking_only 列是 RENAME 后
-- 从老表 llm_model 继承的默认值。
--
-- T2 协议实测依据：docs/aihubmix-protocol-reference.md
-- 线上 bug：llmrouter/preference.go:246 因 supports_thinking=0 硬拒对
-- thinking 变体模型的 thinking=true 请求。本 migration 修正后 bug 自愈。
--
-- ROLLBACK: migrations/20260421_000001_fix_ai_service_thinking_flags_rollback.sql

-- Pre-flight guard: 期望命中 8 行（AiHubMix 所有路由）
SELECT 1 / (
  (SELECT COUNT(*) FROM ai_service s
   JOIN ai_service_route r ON r.model_id = s.id
   JOIN llm_provider p ON p.id = r.provider_id
   WHERE p.name = 'aihubmix')
  - 7
) AS aihubmix_row_guard;
-- 期望 8 行 → 8-7=1 → 1/1 成功
-- 若少于 8 行 → 分母 <=0 → 除零失败

-- 修正 6 行（id 列表：5, 13, 14, 15, 16, 17）
-- 用 model_key 而非 id 做 WHERE，跨环境稳定

-- (1) Claude thinking 变体 → intrinsic (1, 1)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'claude-sonnet-4-6-thinking';

-- (2) DeepSeek base → optional (1, 0)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 0
WHERE model_key = 'deepseek-v3.2';

-- (3) GPT 5.4 base → optional (1, 0)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 0
WHERE model_key = 'gpt-5.4';

-- (4) Gemini thinking 变体 → intrinsic (1, 1)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'gemini-3.1-pro-preview-thinking';

-- (5) DeepSeek thinking 变体 → intrinsic (1, 1)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'deepseek-v3.2-thinking';

-- (6) GPT 5.4 thinking 变体 → intrinsic (1, 1)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'gpt-5.4-thinking';

-- Post-flight verification
SELECT s.model_key, s.supports_thinking, s.thinking_only
FROM ai_service s
WHERE s.model_key IN (
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-thinking',
  'gemini-3.1-pro-preview',
  'gemini-3.1-pro-preview-thinking',
  'deepseek-v3.2',
  'deepseek-v3.2-thinking',
  'gpt-5.4',
  'gpt-5.4-thinking'
) ORDER BY s.id;
```

### 5.2 Rollback

```sql
-- migrations/20260421_000001_fix_ai_service_thinking_flags_rollback.sql
-- 恢复 2026-04-20 之前的 DB 状态（即便那是 buggy 状态，rollback 忠实还原）

UPDATE ai_service SET supports_thinking = 0, thinking_only = 0 WHERE model_key = 'claude-sonnet-4-6-thinking';
UPDATE ai_service SET supports_thinking = 1, thinking_only = 1 WHERE model_key = 'deepseek-v3.2';
UPDATE ai_service SET supports_thinking = 1, thinking_only = 1 WHERE model_key = 'gpt-5.4';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0 WHERE model_key = 'gemini-3.1-pro-preview-thinking';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0 WHERE model_key = 'deepseek-v3.2-thinking';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0 WHERE model_key = 'gpt-5.4-thinking';
```

### 5.3 部署顺序（避免线上不一致）

1. **先部署新 app 代码**（feat 分支 merge 到 develop → CI deploy dev）— 新代码在读旧值时不会崩溃（仅行为保持旧有 buggy 状态）
2. **再跑 migration**——标志修正后 `SavePreference` 自愈，UI model selector 开关激活
3. 窗口 < 5 分钟（Agent C 评估）

---

## §6 计费对账 Spike（Q6=A）

### 6.1 Spike 目标

确认 AiHubMix 对 reasoning_tokens 的计价策略：
- **Option A**：reasoning_tokens 独立计价（可能 > completion token 价）
- **Option B**：reasoning_tokens 并入 completion_tokens 计价

若 A，`pricing_rule` 表需加 `reasoning_price_per_mtok` 列（当前无此列）；若 B，现有 `output_price_per_mtok` 已覆盖，无需改动。

### 6.2 实验方法

发送一次 thinking 调用：
```bash
curl -sS -X POST https://aihubmix.com/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -d '{
    "model": "gpt-5.4",
    "messages": [{"role":"user","content":"Long prompt with ~200 tokens to trigger thinking"}],
    "max_completion_tokens": 500,
    "reasoning_effort": "high"
  }'
```

记录 `usage.completion_tokens_details.reasoning_tokens=N_R` 和 `usage.completion_tokens=N_C`。

等 24 小时后（或立即），登录 AiHubMix 仪表盘 https://aihubmix.com/dashboard，查看该 request 的扣费明细：
- 若扣费 = `N_prompt × input_price + N_C × output_price`，则 reasoning_tokens 并入 completion_tokens 计价（Option B）
- 若扣费 = `N_prompt × input_price + (N_C - N_R) × output_price + N_R × reasoning_price`，则独立计价（Option A）
- 若扣费 ≈ `N_prompt × input_price + N_C × output_price + N_R × output_price` （重复计费），则 Option A with same price（需分列）

### 6.3 Spike 文档

`docs/aihubmix-billing-reconciliation-spike.md`：
- §1 背景（reasoning_tokens 可见性差异）
- §2 实验方法（上述）
- §3 原始 curl 输出 + dashboard 截图
- §4 结论（A/B/C 三选一）
- §5 行动建议（是否需 pricing_rule migration）

### 6.4 时间表

S4 Task 8：编码阶段末期执行（等 T1 代码部署到 dev 后，发一次真实思考调用触发账单）。S5 前完成。

---

## §7 Test Spec

### 7.1 单元测试

| Test file | Coverage |
|-----------|----------|
| `adapter/model_family_test.go` | `InferModelFamily` 覆盖 6 个 family × 代表 slug；-think 后缀精确匹配 |
| `adapter/dmxapi_thinking_test.go` | 4 × 2 矩阵：4 model family × (Thinking=true/false)；verify 出站 body 含/不含 `reasoning_effort`, `max_completion_tokens`, `max_tokens`, 温度正确 |
| `adapter/oai_usage_test.go` | `extractReasoningTokens` 三种情况：nested（OpenAI）/ flat（DeepSeek）/ none（Claude） |
| `adapter/stream_test.go` | 追加一个 case：SSE final chunk 含 `completion_tokens_details.reasoning_tokens`，验证 transport 到 `aiservice.TokenUsage.ReasoningTokens` |
| `registry/store_test.go` | `GetResolvedRoute` 和 `GetResolvedRouteByModelKey` 返回含 `SupportsThinking`, `ThinkingOnly` 的 route |
| `llmrouter/preference_test.go` | 新增：`SavePreference` 对 thinking 变体（supports_thinking=1）的 thinking=true 请求**不再**拒绝（回归保护） |

### 7.2 Fake HTTP server 测试（wire-level 断言）

`adapter/dmxapi_thinking_test.go` 使用 `httptest.NewServer` 捕获出站 body：

```go
func TestDMXAPI_Thinking_InjectsReasoningEffort(t *testing.T) {
    var capturedBody []byte
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedBody, _ = io.ReadAll(r.Body)
        w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
    }))
    defer server.Close()

    route := &registry.ResolvedRoute{
        ProviderModelID: "claude-sonnet-4-6",
        SupportsThinking: true, ThinkingOnly: false,
        Provider: registry.ProviderInfo{BaseURL: server.URL, APIKey: "test"},
    }
    adapter := NewDMXAPIAdapter(...)
    _, err := adapter.Chat(ctx, route, aiservice.ChatRequest{
        Messages: [...], Thinking: true, Temperature: 0.5,
    })
    require.NoError(t, err)

    var body oaiChatRequest
    require.NoError(t, json.Unmarshal(capturedBody, &body))
    assert.Equal(t, "medium", body.ReasoningEffort)
    assert.Equal(t, 1.0, body.Temperature) // overridden from 0.5 → 1
}
```

覆盖必测矩阵：
- Claude base + Thinking=true → `reasoning_effort=medium`, `temperature=1`, `max_tokens=N`
- Claude base + Thinking=false → 无 `reasoning_effort`, `temperature=0.7`（caller 值）
- GPT 5.4 + Thinking=true → `reasoning_effort=medium`, `max_completion_tokens=N`（**不**含 `max_tokens`）
- GPT 5.4 + Thinking=false → **仍**含 `max_completion_tokens`（P1-1 回归）
- Gemini base + Thinking=true + route.ThinkingOnly=true → **不**注入 `reasoning_effort`（避开 Gemini `minimal` 400）
- qwen-turbo + Thinking=true + route.SupportsThinking=false → **不**注入 `reasoning_effort`

### 7.3 Playwright E2E（S5）

4 条关键路径（同 proposal §5，含 P1-3 的非 thinking 模型 skip 路径）。详见 §8。

---

## §8 S5 验证策略

### 8.1 选择：Playwright E2E

理由（Q3=A 决策）：thinking flag 是高风险业务逻辑（直接影响计费和用户感知），须自动回归保护。

### 8.2 关键用户路径（4 条）

1. **Claude thinking 路径**：登录 → Chatbot → 选 Claude 4.6 → 发"用三步推导斐波那契第 10 项" → 至少收到 1 个 `thinking` 事件（`message.reasoning_content` 非空）+ content 非空
2. **SOP thinking 路径**：登录 → SOP 运行某模板 → 执行 → 收到 thinking SSE 事件（任一节点 Claude/DeepSeek）
3. **GPT 5.4 思考无 CoT**：切换到 GPT 5.4 → 发消息 → content 非空 + 前端**不**报错（无 reasoning_content 是预期）+ 检查后端 Langfuse trace 有 `reasoning_tokens > 0`
4. **非 thinking 模型 skip**：切换到 qwen-turbo（ali-dashscope 路由，非 thinking）→ 发消息 → 不收到任何 thinking 事件 + 200 成功（证明 adapter 正确 skip reasoning_effort）

### 8.3 测试文件位置

`numind-web-v3/e2e/aihubmix-thinking-audit.spec.ts`

### 8.4 gstack /qa 补充（可选）

dev 部署后人工用 gstack `/qa` 截图 Langfuse trace 面板验证：
- generation.input.thinking=true 可见
- generation.metadata.resolved_reasoning_effort="medium" 可见
- generation.usage.reasoning_tokens 数值正确（Claude=0 可接受，GPT/Gemini/DeepSeek > 0）

---

## §9 验收标准（最终版）

- [ ] `go test ./internal/pkg/aiservice/... -v` 全通过（新增 12+ 单测）
- [ ] `go test ./internal/numind/biz/llmrouter/... -v` 全通过（preference.go 新增 thinking 变体验收测试）
- [ ] Migration 执行后 `SELECT model_key, supports_thinking, thinking_only FROM ai_service WHERE model_key LIKE '%thinking%' OR model_key IN (8 keys)` 与 §2.5 目标值完全一致
- [ ] Playwright 4 条路径全通过
- [ ] `docs/aihubmix-protocol-reference.md` 存在（S2 已产出）
- [ ] `docs/aihubmix-billing-reconciliation-spike.md` 存在（S4 Task 8 产出）
- [ ] `executor.go`、`chatbot/stream.go` 的 `_ = thinking` 全部删除（grep 验证）
- [ ] Fake HTTP server wire-level 断言 6 个矩阵用例全通过
- [ ] Langfuse dev 环境实测：对一次 Claude thinking 调用，trace 含 `input.thinking=true` + `output.reasoning_content` 非空 + `metadata.resolved_reasoning_effort=medium`
- [ ] `go vet` + `gofmt` 全干净

---

## §10 S4 Task 拆分

**10 个 task**（原子可构建，每个完成可独立验证）：

| # | Task | 文件 | 产物 |
|---|------|------|------|
| 1 | ChatRequest/ChatResponse/ChatChunk + TraceMetadata 类型 | `aiservice/types.go` | 类型定义 + 向后兼容测试 |
| 2 | InferModelFamily helper + 单测 | `adapter/model_family.go` + `model_family_test.go` | 6 family 识别 |
| 3 | oaiChatRequest/oaiUsage 扩展 + extractReasoningTokens | `adapter/adapter.go` + `oai_usage_test.go` | wire struct |
| 4 | DMXAPIAdapter Chat/ChatStream 分派逻辑 + fake-server 单测 | `adapter/dmxapi.go` + `dmxapi_thinking_test.go` | 6 矩阵用例 |
| 5 | runOAIStream 透传 reasoning_tokens + TraceMetadata | `adapter/stream.go` + `stream_test.go` | SSE 单测 |
| 6 | ResolvedRoute + store.go SQL + resolvedRouteRow 对齐 | `registry/registry.go` + `registry/store.go` + `store_test.go` | DB 读回两字段 |
| 7 | DB migration 标志校正 + rollback | `migrations/20260421_000001_*.sql` | 6 行 UPDATE，pre/post guard |
| 8 | 计费对账 spike + 文档 | `docs/aihubmix-billing-reconciliation-spike.md` | 结论 A/B/C + 建议 |
| 9 | tracing.go 追加 resolved metadata（含 stream channel 拦截） | `middleware/tracing.go` | Langfuse 可见 resolved 字段 |
| 10 | SOP/Chatbot 入口 thinking 真实传递 + E2E + S5 策略文档 | `biz/sop/executor.go` + `biz/chatbot/stream.go` + `e2e/*.spec.ts` | 端到端打通 |

**依赖图**：
```
Task 1 (types) ──┬─→ Task 3 (oai request/usage) ──┐
                 │                                 │
                 └─→ Task 5 (stream) ──────────────┼─→ Task 4 (dmxapi dispatch)
                                                   │         │
Task 2 (family) ─────────────────────────────────┘         ↓
                                                       Task 9 (tracing) ← Task 6 (registry)
                                                                              │
                                                                              ↓
                                                                          Task 10 (entry + E2E)
                                                                              │
Task 7 (migration) — 独立，可并行                                             │
Task 8 (spike) — S4 后期，依赖 Task 10 部署 —————————————————————————————————┘
```

---

## §11 已解决的 S1 遗留点

| S1 §7 条目 | S2 解决方案 |
|-----------|-------------|
| 1. oaiUsage.ReasoningTokens wire 路径分派 | §4.3 `extractReasoningTokens` helper 处理 OpenAI 嵌套 / DeepSeek 平级 / Claude 无 三种情况 |
| 2. 计费对账 spike 方法学 | §6 明确步骤 + AiHubMix dashboard 核对 |
| 3. DB 标志修正最终值 | §2.5 表格定锁（基于 T2 实测证据） |
| 4. AiHubMix 对 reasoning_effort 的兜底容忍 | 不加 fallback（NDF §3 "不加错误处理当场景不存在"）—— §4.4 决策表覆盖所有合法组合 |
| 5. OpenRouter 未来迁移路径 | 本 S2 铺好的基础（ChatRequest.Thinking, ResolvedRoute 两字段, inferModelFamily 扩展点）OpenRouter feature 可复用 |

---

## §12 已知 tech debt（不在本期修）

1. AiHubMix API key 硬编码在 `20260416_100000_seed_aihubmix_provider.sql`，未来 `SyncProviderCredentials` 完善后清除
2. `reasoning_effort` 值硬编码 `medium`，未来 openrouter-provider feature 再做用户/admin 可调
3. `pricing_rule` 不含 `reasoning_price_per_mtok` 列——§6 spike 若发现 Option A（独立计价），记 hotfix
4. Gemini 伪流式（4 chunk 批量）前端渲染体验不佳，UI 改动本期 out-of-scope
5. DeepSeek 模型名大小写波动（`DeepSeek-V3.2` vs `deepseek-v3.2`），不能作为 billing key——现有代码已用 `ProviderModelID` 而非 `chunk.Model`，安全

---

## §13 未决问题（S2 gate 由 Opus reviewer + 用户决定）

1. **流式 tracing middleware 拦截 channel 的具体实现**（§4.8）：S4 Task 9 展开
2. **task lint 本地未装**：依据 `parent-self-grant-membership` 先例（S4 gate 用 `go vet + gofmt`），本期沿用相同策略
3. **S4 Task 7 migration 执行时机**：开发 dev 环境先跑；qa/prod 随 release 节奏