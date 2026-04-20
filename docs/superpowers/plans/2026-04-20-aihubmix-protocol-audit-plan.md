# aihubmix-protocol-audit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 thinking flag 从用户 preference 真实传递到出站 AiHubMix 请求（修复 `executor.go:109` 和 `chatbot/stream.go:177` 的 `_ = thinking` 半拉子工程），按 T2 实测的 per-model 协议分派请求构造，补全 response 端 `reasoning_tokens` wire 解码，校正 `ai_service` 表中 6 行错误的 thinking 标志（修复 `preference.go:246` 对合法 thinking 变体 request 的硬拒 user-facing bug），产出 AiHubMix 协议权威文档 + 计费对账 spike。

**Architecture:** `ChatRequest.Thinking bool` 端到端管道 + adapter 层 `InferModelFamily` 决策（以 route.SupportsThinking/ThinkingOnly 为准 + family 分派 max_completion_tokens / temp 覆盖）+ `ChatResponse.TraceMetadata` 回传供 Langfuse middleware 读取 + DB 标志校正 migration。保留 Claude `-think` suffix 变体（Q2=A）；Gemini intrinsic 模型用 `"intrinsic"` 哨兵（Q8=B）；Claude base + Thinking=true 强制 temp=1（Q4=A）。

**Tech Stack:** Go 1.24, GORM, MySQL 8.0, Gin, Langfuse SDK, Playwright 1.48 (E2E only).

**Spec**: `numind-server/docs/superpowers/specs/2026-04-20-aihubmix-protocol-audit-design.md`
**T2 protocol reference**: `numind-server/docs/aihubmix-protocol-reference.md` (S2 产出，564 行权威数据)

---

## 文件结构

| 文件 | 操作 | 职责 |
|------|------|------|
| `internal/pkg/aiservice/types.go` | Modify | 加 `ChatRequest.Thinking bool` + `TraceMetadata` 类型 + `ChatResponse/ChatChunk.TraceMetadata` 字段 |
| `internal/pkg/aiservice/adapter/model_family.go` | **Create** | `InferModelFamily` helper + enum |
| `internal/pkg/aiservice/adapter/model_family_test.go` | **Create** | family 识别单测（显式 7 slugs 用例） |
| `internal/pkg/aiservice/adapter/adapter.go` | Modify | `oaiChatRequest` 加 `ReasoningEffort`/`MaxCompletionTokens`；`oaiUsage` 加嵌套 `CompletionTokensDetails` + flat `ReasoningTokens` + `extractReasoningTokens` helper |
| `internal/pkg/aiservice/adapter/adapter_test.go` | Modify | 补 `oaiUsage.extractReasoningTokens` 3 种情况单测（nested/flat/none） |
| `internal/pkg/aiservice/adapter/dmxapi.go` | Modify | `Chat`/`ChatStream` 抽出 `buildOAIRequest` 分派逻辑（family + thinking gating + Claude temp override） |
| `internal/pkg/aiservice/adapter/dmxapi_thinking_test.go` | **Create** | 8 个 fake-server wire-level 断言（6 矩阵 + Claude-think + GPT Thinking=false 回归） |
| `internal/pkg/aiservice/adapter/stream.go` | Modify | `runOAIStream` 签名加 `traceMeta` 参数 + final chunk 填 TraceMetadata + reasoning_tokens 透传 |
| `internal/pkg/aiservice/adapter/stream_test.go` | Modify | 补 SSE 最终 chunk 含 `completion_tokens_details.reasoning_tokens` 的单测 |
| `internal/pkg/aiservice/registry/registry.go` | Modify | `ResolvedRoute` 加 `SupportsThinking`/`ThinkingOnly` bool |
| `internal/pkg/aiservice/registry/store.go` | Modify | 2 个 SELECT 补列 + `resolvedRouteRow` 对齐 + `ResolvedRoute` 构造透传 |
| `internal/pkg/aiservice/registry/store_test.go` | Modify | DB 读回两字段单测 |
| `internal/pkg/aiservice/middleware/tracing.go` | Modify | 非流式 + 流式 response 中捕获 `TraceMetadata` 写入 Langfuse generation |
| `migrations/20260421_000001_fix_ai_service_thinking_flags.sql` | **Create** | 6 行 UPDATE + pre/post guard |
| `migrations/20260421_000001_fix_ai_service_thinking_flags_rollback.sql` | **Create** | 忠实还原 |
| `migrations/20260421_000002_audit_user_model_preference.sql` | **Create** | Q10=A：SELECT 审计脚本 + 条件 UPDATE normalize（DeepSeek/GPT base 误拒修复） |
| `internal/numind/biz/llmrouter/preference_test.go` | Modify | 加 thinking 变体 `SavePreference` 回归保护单测 |
| `internal/numind/biz/sop/executor.go:108-113, 463-467` | Modify | 删 `_ = thinking`，`executeViaGateway` 接 thinking 参数，`ChatRequest{}` 加 `Thinking: thinking` |
| `internal/numind/biz/chatbot/stream.go:172-177` | Modify | 同上 |
| `numind-web-v3/e2e/aihubmix-thinking-audit.spec.ts` | **Create** | 4 条 Playwright 路径 |
| `docs/aihubmix-billing-reconciliation-spike.md` | **Create** | Q9=A：2 次对照 curl 实验 + 结论 |
| `numind-server/build-manifest.yaml` | Modify | 每 task 完成后更新 progress |

---

## Task 1: `ChatRequest.Thinking` + `TraceMetadata` 类型 (types.go)

**目标**：扩展 aiservice gateway 类型定义，为后续 adapter/middleware 改动铺路。纯 append-field，不破坏任何既有 caller（全部用 named-field 构造）。

**Files**: 
- Modify: `internal/pkg/aiservice/types.go`

**依赖**：无

### Step 1.1: 加 `TraceMetadata` struct

- [ ] 在 types.go 文件末尾（TokenUsage 之后）新增：
  ```go
  type TraceMetadata struct {
      ResolvedReasoningEffort string `json:"resolved_reasoning_effort,omitempty"`
      ResolvedModelFamily     string `json:"resolved_model_family,omitempty"`
      TempOverridden          bool   `json:"temp_overridden,omitempty"`
  }
  ```
  - `ResolvedReasoningEffort` 可能值：`""`（未注入）/ `"medium"`（已注入）/ `"intrinsic"`（Q8=B 哨兵，intrinsic 模型思考发生但未注入参数）

### Step 1.2: `ChatRequest` 加 `Thinking` 字段

- [ ] L122-141 ChatRequest struct 末尾加 `Thinking bool`
  - **注意**：json tag 用 `json:"thinking"` **不带 omitempty**（P2-5 修订：trace 层应该能看到 `thinking=false` 的显式选择）
  - Godoc 明确说明"For intrinsic-thinking models (thinking_only=true), this flag is advisory only — thinking always happens"

### Step 1.3: `ChatResponse` 加 `TraceMetadata` 字段

- [ ] L144-157 ChatResponse struct 末尾加 `TraceMetadata *TraceMetadata \`json:"trace_metadata,omitempty"\``
  - 指针类型，nil 时 omitempty 从 JSON 去除

### Step 1.4: `ChatChunk` 加 `TraceMetadata` 字段

- [ ] L160-175 ChatChunk struct 末尾加 `TraceMetadata *TraceMetadata \`json:"trace_metadata,omitempty"\``
  - 仅 `IsFinal=true` 时 adapter 填充，中间 chunk 留 nil

### Step 1.5: 编译验证

- [ ] `go build ./internal/pkg/aiservice/...` 通过
- [ ] `go vet ./internal/pkg/aiservice/...` 干净

**Acceptance criteria**：编译干净 + godoc 清楚说明 `"intrinsic"` 哨兵语义。

---

## Task 2: `InferModelFamily` helper (model_family.go) — P1-2 修订

**目标**：独立 helper + 单测，为 Task 4 adapter dispatch 提供基础。**P1-2 修订**：前缀匹配改为显式枚举分支，避免 `gpt-50-xxx` 之类意外命中。

**Files**:
- Create: `internal/pkg/aiservice/adapter/model_family.go`
- Create: `internal/pkg/aiservice/adapter/model_family_test.go`

**依赖**：Task 1（不引用任何 Task 1 类型，但同 package，需 Task 1 先落地避免编译失败）

### Step 2.1: 创建 model_family.go

- [ ] 内容：
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

  // InferModelFamily dispatches based on provider_model_id prefix.
  // Matching order: Claude -think suffix (exact Claude check) → OpenAI reasoning
  // prefix (strict enumeration of gpt-5, gpt-5-, gpt-5., o1, o1-, o3, o3-, o4, o4-) →
  // generic family prefixes → fallback Generic.
  // Exported for unit tests.
  func InferModelFamily(providerModelID string) ModelFamily {
      // Claude -think suffix variant (highest priority)
      if strings.HasPrefix(providerModelID, "claude-") && strings.HasSuffix(providerModelID, "-think") {
          return ModelFamilyClaudeThinkingSlug
      }

      // OpenAI reasoning family — strict enumeration (P1-2 tightened)
      switch {
      case providerModelID == "gpt-5",
          strings.HasPrefix(providerModelID, "gpt-5-"),
          strings.HasPrefix(providerModelID, "gpt-5."),
          providerModelID == "o1",
          strings.HasPrefix(providerModelID, "o1-"),
          providerModelID == "o3",
          strings.HasPrefix(providerModelID, "o3-"),
          providerModelID == "o4",
          strings.HasPrefix(providerModelID, "o4-"):
          return ModelFamilyOpenAIReasoning
      }

      // Generic family prefixes
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

### Step 2.2: 创建 model_family_test.go — 显式矩阵用例（P1-2）

- [ ] 表驱动测试覆盖：
  | providerModelID | 期望 family |
  |-----------------|-------------|
  | `gpt-5` | openai-reasoning |
  | `gpt-5.4` | openai-reasoning |
  | `gpt-5-preview` | openai-reasoning |
  | `gpt-5-2026-03-05` | openai-reasoning |
  | `gpt-50-anything`（collision probe）| generic |
  | `o1` / `o1-preview` | openai-reasoning |
  | `o3` / `o3-mini` | openai-reasoning |
  | `o4-turbo` | openai-reasoning |
  | `o10-anything`（collision probe）| generic |
  | `claude-sonnet-4-6` | claude |
  | `claude-sonnet-4-6-think` | claude-thinking-suffix |
  | `claude-haiku-4-6-think`（虚构但合法）| claude-thinking-suffix |
  | `gemini-3.1-pro-preview` | gemini |
  | `gemini-3.1-pro-preview-think`（T2 证实 404 但 DB 可能有）| gemini（不是 claude-thinking-suffix！）|
  | `deepseek-v3.2` | deepseek |
  | `deepseek-v3.2-think` | deepseek |
  | `qwen-turbo` | generic |
  | `text-embedding-v4` | generic |
  | `""`（空串）| generic |

### Step 2.3: 运行测试

- [ ] `go test ./internal/pkg/aiservice/adapter/ -run TestInferModelFamily -v` 全通过

**Acceptance criteria**：18 个用例全绿，包括 2 个 collision probe 返回 generic。

---

## Task 3: `oaiChatRequest` + `oaiUsage` + `extractReasoningTokens` (adapter.go)

**目标**：扩展 wire 层请求/响应 struct，为 DMXAPI adapter 提供字段承载。

**Files**:
- Modify: `internal/pkg/aiservice/adapter/adapter.go`
- Modify: `internal/pkg/aiservice/adapter/adapter_test.go`

**依赖**：Task 1（不强依赖但逻辑上后）

### Step 3.1: `oaiChatRequest` 扩展（~L138-150）

- [ ] 加两字段：
  ```go
  MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
  ReasoningEffort     string `json:"reasoning_effort,omitempty"`
  ```

### Step 3.2: `oaiUsage` 扩展（~L212-217）

- [ ] 加两字段 + 新嵌套类型：
  ```go
  type oaiUsage struct {
      PromptTokens              int                         `json:"prompt_tokens"`
      CompletionTokens          int                         `json:"completion_tokens"`
      TotalTokens               int                         `json:"total_tokens"`
      CompletionTokensDetails   *oaiCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
      ReasoningTokens           int                         `json:"reasoning_tokens,omitempty"`
  }

  type oaiCompletionTokensDetails struct {
      ReasoningTokens int `json:"reasoning_tokens,omitempty"`
  }
  ```

### Step 3.3: 加 `extractReasoningTokens` helper

- [ ] method on `*oaiUsage`:
  ```go
  // extractReasoningTokens returns the reasoning token count from whichever
  // wire path the provider used. Returns 0 when the provider doesn't surface
  // reasoning tokens at all (e.g. Claude via AiHubMix folds them into completion_tokens).
  // OpenAI family uses nested `completion_tokens_details.reasoning_tokens`;
  // DeepSeek via AiHubMix also uses nested (per T2 §8 raw evidence);
  // flat ReasoningTokens field retained for defensive compatibility with
  // providers that may emit at top level.
  func (u *oaiUsage) extractReasoningTokens() int {
      if u == nil {
          return 0
      }
      if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens > 0 {
          return u.CompletionTokensDetails.ReasoningTokens
      }
      return u.ReasoningTokens
  }
  ```

### Step 3.4: 单测（`adapter_test.go`）

- [ ] `TestOAIUsage_ExtractReasoningTokens` 表驱动覆盖：
  | 场景 | Usage JSON | 期望返回 |
  |------|-----------|---------|
  | nested（OpenAI/Gemini/DeepSeek）| `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210,"completion_tokens_details":{"reasoning_tokens":42}}` | 42 |
  | flat | `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210,"reasoning_tokens":17}` | 17 |
  | none（Claude）| `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210}` | 0 |
  | nil pointer | nil | 0 |
  | nested=0 fallback flat=5 | `{"completion_tokens_details":{"reasoning_tokens":0},"reasoning_tokens":5}` | 5 |

### Step 3.5: 编译 + 测试

- [ ] `go build ./internal/pkg/aiservice/adapter/...`
- [ ] `go test ./internal/pkg/aiservice/adapter/ -run TestOAIUsage -v`

**Acceptance criteria**：5 个 case 全绿，`oaiChatRequest.ReasoningEffort` JSON marshal 正确 omitempty。

---

## Task 4: DMXAPI adapter `buildOAIRequest` + dispatch (dmxapi.go) — P2-6 补 Claude-think 测试

**目标**：集中 per-family 分派逻辑到 `buildOAIRequest`，Chat/ChatStream 共用。Wire-level 断言测试用 fake HTTP server。

**Files**:
- Modify: `internal/pkg/aiservice/adapter/dmxapi.go`
- Create: `internal/pkg/aiservice/adapter/dmxapi_thinking_test.go`

**依赖**：Task 2（InferModelFamily）+ Task 3（oaiChatRequest 新字段）

### Step 4.1: 加 `buildOAIRequest` helper 方法

- [ ] 在 dmxapi.go 中新增（位置：Chat 方法之前）：
  ```go
  // buildOAIRequest assembles the OpenAI-compatible request payload and the
  // trace metadata record. Centralizes per-family dispatch so Chat and
  // ChatStream stay DRY.
  func (a *DMXAPIAdapter) buildOAIRequest(
      route *registry.ResolvedRoute,
      req aiservice.ChatRequest,
      stream bool,
  ) (oaiChatRequest, *aiservice.TraceMetadata) {
      family := InferModelFamily(route.ProviderModelID)
      meta := &aiservice.TraceMetadata{
          ResolvedModelFamily: string(family),
      }

      oaiReq := oaiChatRequest{
          Model:       route.ProviderModelID,
          Messages:    convertMessages(req.Messages),
          Temperature: req.Temperature,
          Stream:      stream,
      }
      if stream {
          oaiReq.StreamOptions = &oaiStreamOptions{IncludeUsage: true}
      }
      if req.ResponseFormat != "" {
          oaiReq.ResponseFormat = &oaiResponseFormat{Type: string(req.ResponseFormat)}
      }

      // Max tokens dispatch (family-based, independent of Thinking flag) — P1-1 原则
      if family == ModelFamilyOpenAIReasoning {
          oaiReq.MaxCompletionTokens = req.MaxTokens
      } else {
          oaiReq.MaxTokens = req.MaxTokens
      }

      // Thinking → reasoning_effort gating (spec §3.2 decision table)
      // Rule: inject "medium" only for optional-thinking models (SupportsThinking && !ThinkingOnly)
      // For intrinsic (ThinkingOnly=true) models, use "intrinsic" sentinel (Q8=B) without wire injection
      if req.Thinking && route.SupportsThinking {
          if route.ThinkingOnly {
              meta.ResolvedReasoningEffort = "intrinsic"
          } else {
              oaiReq.ReasoningEffort = "medium"
              meta.ResolvedReasoningEffort = "medium"
          }
      }

      // Claude base + Thinking=true → force temperature=1 (Q4=A)
      // Note: Claude -think suffix variant already has AiHubMix-enforced temp=1; our adapter
      // doesn't need to touch it (family=claude-thinking-suffix skips this branch).
      if family == ModelFamilyClaude && req.Thinking && req.Temperature != 1 {
          oaiReq.Temperature = 1
          meta.TempOverridden = true
      }

      return oaiReq, meta
  }
  ```

### Step 4.2: 改 `Chat` 使用 `buildOAIRequest`

- [ ] L69-116：替换 oaiChatRequest 构造为 `oaiReq, traceMeta := a.buildOAIRequest(route, req, false)`
- [ ] 在构造 `aiservice.ChatResponse` 返回值处加：
  ```go
  resp.TraceMetadata = traceMeta
  resp.Usage.ReasoningTokens = oaiResp.Usage.extractReasoningTokens()
  ```

### Step 4.3: 改 `ChatStream` 使用 `buildOAIRequest`

- [ ] L120-144：替换 oaiChatRequest 构造为 `oaiReq, traceMeta := a.buildOAIRequest(route, req, true)`
- [ ] 将 `traceMeta` 传给 `runOAIStream`（Task 5 会改签名）：
  ```go
  go runOAIStream(resp.Body, ch, a.Name(), route.ProviderModelID, traceMeta)
  ```

### Step 4.4: 创建 `dmxapi_thinking_test.go` — 8 wire-level 断言（P2-6 修订）

fake HTTP server 捕获出站 body，断言字段：

- [ ] **case 1 Claude base + Thinking=true**：
  - Route: `ProviderModelID="claude-sonnet-4-6", SupportsThinking=true, ThinkingOnly=false`
  - Req: `Thinking=true, Temperature=0.5, MaxTokens=500`
  - Assert: `body.ReasoningEffort="medium"`, `body.Temperature=1.0`（覆盖）, `body.MaxTokens=500`, `body.MaxCompletionTokens=0`
  - Assert: `resp.TraceMetadata.ResolvedReasoningEffort="medium"`, `resp.TraceMetadata.ResolvedModelFamily="claude"`, `resp.TraceMetadata.TempOverridden=true`

- [ ] **case 2 Claude base + Thinking=false**：
  - Route: same
  - Req: `Thinking=false, Temperature=0.7, MaxTokens=500`
  - Assert: `body.ReasoningEffort=""`, `body.Temperature=0.7`（保留）, `body.MaxTokens=500`
  - Assert: `resp.TraceMetadata.ResolvedReasoningEffort=""`, `resp.TraceMetadata.TempOverridden=false`

- [ ] **case 3 GPT 5.4 + Thinking=true**：
  - Route: `ProviderModelID="gpt-5.4", SupportsThinking=true, ThinkingOnly=false`
  - Req: `Thinking=true, MaxTokens=500`
  - Assert: `body.ReasoningEffort="medium"`, `body.MaxCompletionTokens=500`, `body.MaxTokens=0`（不含）

- [ ] **case 4 GPT 5.4 + Thinking=false (P1-1 回归保护)**：
  - Route: same
  - Req: `Thinking=false, MaxTokens=500`
  - Assert: `body.MaxCompletionTokens=500`（仍用）, `body.MaxTokens=0`, `body.ReasoningEffort=""`

- [ ] **case 5 Gemini intrinsic (P1-3 Q8=B)**：
  - Route: `ProviderModelID="gemini-3.1-pro-preview", SupportsThinking=true, ThinkingOnly=true`
  - Req: `Thinking=true`
  - Assert: `body.ReasoningEffort=""`（不发，避免 Gemini minimal 400）
  - Assert: `resp.TraceMetadata.ResolvedReasoningEffort="intrinsic"`（哨兵值）

- [ ] **case 6 qwen-turbo non-thinking skip**：
  - Route: `ProviderModelID="qwen-turbo", SupportsThinking=false, ThinkingOnly=false`
  - Req: `Thinking=true`
  - Assert: `body.ReasoningEffort=""`（不发，模型不支持）
  - Assert: `resp.TraceMetadata.ResolvedReasoningEffort=""`

- [ ] **case 7 Claude -think variant (P2-6 补充)**：
  - Route: `ProviderModelID="claude-sonnet-4-6-think", SupportsThinking=true, ThinkingOnly=true`
  - Req: `Thinking=true, Temperature=0.5`
  - Assert: `body.ReasoningEffort=""`（intrinsic 不发）
  - Assert: `body.Temperature=0.5`（**不**覆盖，因 family=claude-thinking-suffix 跳过 Claude 温度覆盖分支；AiHubMix 服务器会自己强制 1）
  - Assert: `resp.TraceMetadata.ResolvedReasoningEffort="intrinsic"`, `resp.TraceMetadata.ResolvedModelFamily="claude-thinking-suffix"`, `resp.TraceMetadata.TempOverridden=false`

- [ ] **case 8 DeepSeek base + Thinking=true**：
  - Route: `ProviderModelID="deepseek-v3.2", SupportsThinking=true, ThinkingOnly=false`
  - Req: `Thinking=true, Temperature=0.7`
  - Assert: `body.ReasoningEffort="medium"`, `body.MaxTokens=500`, `body.Temperature=0.7`（不覆盖，非 Claude family）

### Step 4.5: 运行测试

- [ ] `go test ./internal/pkg/aiservice/adapter/ -run TestDMXAPI -v` 8 case 全绿

**Acceptance criteria**：wire-level body 与 TraceMetadata 全部与决策表一致。

---

## Task 5: `runOAIStream` reasoning_tokens + TraceMetadata 透传 (stream.go) — P1-1 流式细节

**目标**：把 SSE final chunk 的 reasoning_tokens 透传到 `aiservice.TokenUsage`，并让 final chunk 携带 `TraceMetadata` 供 middleware 消费。**P1-1 修订**：本 task 明确 Langfuse SDK 实际调用方式 + stream wrapper 捕获逻辑。

**Files**:
- Modify: `internal/pkg/aiservice/adapter/stream.go`
- Modify: `internal/pkg/aiservice/adapter/stream_test.go`

**依赖**：Task 1（TraceMetadata 类型） + Task 3（oaiUsage.extractReasoningTokens）

### Step 5.1: 改 `runOAIStream` 签名加 `traceMeta` 参数

- [ ] L36 签名：
  ```go
  func runOAIStream(
      r io.ReadCloser,
      ch chan<- aiservice.ChatChunk,
      provider string,
      defaultModel string,
      traceMeta *aiservice.TraceMetadata,
  )
  ```

### Step 5.2: 透传 reasoning_tokens 到 TokenUsage

- [ ] L94-98 `aiservice.TokenUsage` 构造加一行：
  ```go
  usage := &aiservice.TokenUsage{
      PromptTokens:     chunk.Usage.PromptTokens,
      CompletionTokens: chunk.Usage.CompletionTokens,
      TotalTokens:      chunk.Usage.TotalTokens,
      ReasoningTokens:  chunk.Usage.extractReasoningTokens(),
  }
  ```

### Step 5.3: final chunk 填 `TraceMetadata`

- [ ] L145-152 final chunk 发射处：
  ```go
  ch <- aiservice.ChatChunk{
      IsFinal:       true,
      FinishReason:  finishReason,
      Usage:         usage,
      Model:         modelName,
      Provider:      provider,
      TraceMetadata: traceMeta,
  }
  ```

### Step 5.4: 单测（`stream_test.go`）

- [ ] `TestRunOAIStream_TransportsReasoningTokens`：
  - Fake SSE feed 含 `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"completion_tokens_details":{"reasoning_tokens":42}}}`
  - Feed traceMeta `&aiservice.TraceMetadata{ResolvedReasoningEffort:"medium"}`
  - 收集 channel chunks
  - Assert: final chunk 的 `Usage.ReasoningTokens=42`, `TraceMetadata.ResolvedReasoningEffort="medium"`

- [ ] `TestRunOAIStream_NilTraceMeta`：
  - Feed traceMeta=nil（向后兼容）
  - Assert: final chunk 的 `TraceMetadata=nil`，不 panic

### Step 5.5: 所有其他 runOAIStream 调用点更新

- [ ] grep `runOAIStream\(` 找所有 caller：
  - `dmxapi.go`（Task 4 已改）
  - 其他 adapter 若有（ali.go / volc.go）：统一传 `nil` 为 traceMeta（这些 adapter 本期不覆盖 TraceMetadata 语义）

### Step 5.6: 编译 + 测试

- [ ] `go build ./internal/pkg/aiservice/...` + `go test ./internal/pkg/aiservice/adapter/ -run TestRunOAIStream -v`

**Acceptance criteria**：reasoning_tokens 正确从 SSE 透传到 TokenUsage；final chunk 含正确 TraceMetadata；nil 兼容。

---

## Task 6: `ResolvedRoute` 扩展 + store.go SQL (registry)

**目标**：让路由解析把 `ai_service.supports_thinking`/`thinking_only` 读到 `ResolvedRoute`，供 adapter gating 使用。

**Files**:
- Modify: `internal/pkg/aiservice/registry/registry.go`
- Modify: `internal/pkg/aiservice/registry/store.go`
- Modify: `internal/pkg/aiservice/registry/store_test.go`

**依赖**：无（并行 Task 1-5）

### Step 6.1: `ResolvedRoute` 加两字段

- [ ] registry.go L51-63 struct 末尾加：
  ```go
  SupportsThinking bool
  ThinkingOnly     bool
  ```

### Step 6.2: `resolvedRouteRow` 加对应两字段

- [ ] store.go L85-102 加 `SupportsThinking bool` / `ThinkingOnly bool`

### Step 6.3: `GetResolvedRoute` SQL 加两列

- [ ] store.go L306-332 SELECT 加 `s.supports_thinking, s.thinking_only`（放在 `s.is_active` 之后）
- [ ] 对应 Scan 目标顺序同步（GORM 用 struct tag 自动映射；若用原始 Scan 则按列顺序写对）

### Step 6.4: `GetResolvedRouteByModelKey` SQL 同步

- [ ] store.go L395+（第二查询）同样补列

### Step 6.5: `ResolvedRoute` 构造透传

- [ ] 在构造返回 route 对象的地方加：
  ```go
  SupportsThinking: row.SupportsThinking,
  ThinkingOnly:     row.ThinkingOnly,
  ```
  两处查询都需改

### Step 6.6: store_test.go 补单测

- [ ] `TestStore_GetResolvedRoute_ReadsThinkingFlags`：用内存 SQLite（与 parent-self-grant feature 同款）插入一行 ai_service + ai_service_route + llm_provider，调用 `GetResolvedRoute`，assert `route.SupportsThinking` 和 `ThinkingOnly` 正确读回。覆盖 (true, false), (true, true), (false, false) 三种组合

### Step 6.7: 编译 + 测试

- [ ] `go test ./internal/pkg/aiservice/registry/ -v`

**Acceptance criteria**：3 组合读回正确，两个查询语句都生效。

---

## Task 7a: DB migration — 修正 `ai_service` 标志 (migration SQL)

**目标**：校正 6 行错误值。**P1-6 修订**：migration 执行时机在 Task 10 之后（见依赖图）。

**Files**:
- Create: `migrations/20260421_000001_fix_ai_service_thinking_flags.sql`
- Create: `migrations/20260421_000001_fix_ai_service_thinking_flags_rollback.sql`

**依赖**：Task 6（registry 代码能读新列）+ Task 10（app 层改动 deploy 后）

### Step 7a.1: 创建 migration SQL

- [ ] 按 S2 spec §5.1 写完整 SQL（含 pre/post guard + 6 UPDATE），Head comment 明确引用 T2 实测依据

### Step 7a.2: 创建 rollback SQL + pre-check guard (S3 P2-F)

- [ ] 按 S2 spec §5.2 写回滚。**S3 P2-F 修订**：在 rollback 前加 pre-check guard，确认 migration 已完整执行（6 行全部更新成目标值）才允许 rollback：
  ```sql
  -- Pre-flight: 确认 migration 已完整执行（否则 rollback 会破坏半更新状态）
  SELECT 1 / (
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'claude-sonnet-4-6-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'deepseek-v3.2' AND supports_thinking = 1 AND thinking_only = 0) +
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'gpt-5.4' AND supports_thinking = 1 AND thinking_only = 0) +
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'gemini-3.1-pro-preview-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'deepseek-v3.2-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
    (SELECT COUNT(*) FROM ai_service
     WHERE model_key = 'gpt-5.4-thinking' AND supports_thinking = 1 AND thinking_only = 1)
    - 5
  ) AS rollback_prerequisite_check;
  -- 期望 6 行全匹配 → 6-5=1 → 1/1 成功；若少于 6 → 除零失败，migration 未完整执行，拒绝 rollback
  ```

### Step 7a.3: Dev DB 执行时机

- [ ] 本 task 产物只是 SQL 文件，不实际跑
- [ ] **实际执行** 在 Task 4+5 合并之前（见部署序 Step 2），提前修 DB 让 preference.go:246 bug 自愈窗口最小化（S3 P1-B 修订）

**Acceptance criteria**：SQL 语法正确（`mysql --dry-run` 或本地 sqlite 解析通过），rollback 含 pre-check guard。

---

## Task 7b: `user_model_preference` 存量审计 (migration SQL) — Q10=A

**目标**：检查存量 user preference 行在 migration 前后是否有语义变化。**Q10=A 决策**：跑审计 SQL + 条件 UPDATE normalize 少量偏差。

**Files**:
- Create: `migrations/20260421_000002_audit_user_model_preference.sql`

**依赖**：Task 7a

### Step 7b.1: 审计 SQL

- [ ] 创建 migration 文件，含两段：

  **Part A 审计只读**：
  ```sql
  -- 列出受影响的所有 user_model_preference 行（thinking 字段可能语义漂移）
  SELECT p.user_id, p.feature, p.model_key, p.thinking,
         s.supports_thinking AS current_supports, s.thinking_only AS current_only
  FROM user_model_preference p
  JOIN ai_service s ON s.model_key = p.model_key
  WHERE s.model_key IN (
      'claude-sonnet-4-6-thinking',
      'deepseek-v3.2',
      'gpt-5.4',
      'gemini-3.1-pro-preview-thinking',
      'deepseek-v3.2-thinking',
      'gpt-5.4-thinking'
  );
  ```

  **Part B 防御性 normalize**（S3 P1-4 修订）：
  ```sql
  -- DeepSeek/GPT base 的存量偏好防御性处理：
  -- 理论上：migration 7a 前，preference.go:242 会在 thinking_only=1 时强推 thinking=true，
  -- 所以 user_model_preference 中这两个 model_key 的 thinking=false 行**理论上不存在**。
  -- 实践上：如果手动 SQL / 旧代码缺陷 / migration 执行时机异常等导致存在 thinking=false 行，
  -- migration 7a 后这些行会真的生效（原来被强推为 true）。为消除此不确定性，
  -- 防御性把两个 base model 的 thinking=false 行改为 1，与用户原体验对齐。
  -- 若执行后 affected_rows=0（预期情况），说明无历史缺陷，此步骤等同 no-op。
  UPDATE user_model_preference
  SET thinking = 1
  WHERE model_key IN ('deepseek-v3.2', 'gpt-5.4')
    AND thinking = 0;
  -- 验证：SHOW WARNINGS; 或 SELECT ROW_COUNT(); 记录实际更新行数到 audit log
  ```

- [ ] Head comment 说明 Q10=A 决策 + Part A 是审计、Part B 是防御性 normalize

### Step 7b.2: 审计输出 sink（S3 P2-E 修订）

- [ ] Part A 的 SELECT 输出必须落盘，否则 migration runner 会丢弃。实施方式：
  - 选项 A：migration 文件本身不含 Part A SELECT，而是在 `migrations/audit/` 新建独立 shell 脚本 `run_pref_audit.sh` 供运维手动跑并 tee 到 log
  - 选项 B：migration 文件内 Part A 写成 `INSERT INTO audit_log (...) SELECT ...` 形式，让审计数据落到新建的 `audit_log` 表
  - **本期选 A**：避免引入新表；产出：
    - `migrations/audit/20260421_preference_audit.sh`（可执行 shell，含 `mysql ... | tee audit/20260421_preference_audit_output.log`）
    - `.gitkeep` 在 `migrations/audit/` 目录（首次创建）
    - SOP：S4 Task 7b 完成时手动执行该 shell，输出 log 可 commit 到 repo 作审计留存

### Step 7b.3: Rollback

- [ ] `migrations/20260421_000002_audit_user_model_preference_rollback.sql`：rollback 不恢复原数据（无法区分原来是 0 还是 1），说明"无法精准 rollback，仅还原结构"
- [ ] Rollback 仅针对 Part B 的 UPDATE：`UPDATE user_model_preference SET thinking = 0 WHERE ...` **不可**，因为会把本来应为 1 的也改为 0。记录为"无 rollback，Part B 影响极小（预期 0 行受影响），不需逆操作"

**Acceptance criteria**：SQL 语法正确，Part A SELECT 在 shell 脚本中可复现，Part B 只动两个 model_key 的行。

---

## Task 8: 计费对账 spike (docs + curl) — Q9=A 提前执行

**目标**：Q9=A 决策 — spike 提前到 S4 早期执行，规避 AiHubMix dashboard 结算时延风险。**P1-5 修订**：单 curl 无法区分 Option A/B，需 2 次对照实验（low vs high reasoning_effort）。

**Files**:
- Create: `docs/aihubmix-billing-reconciliation-spike.md`

**依赖**：AiHubMix API key（已在 seed SQL）

### Step 8.1: 两次对照 curl 实验

- [ ] **实验 1（low reasoning_effort）**：
  ```bash
  curl -sS -X POST https://aihubmix.com/v1/chat/completions \
    -H "Authorization: Bearer sk-vdu..." \
    -H "Content-Type: application/json" \
    -d '{
      "model": "gpt-5.4",
      "messages": [{"role":"user","content":"<固定 200 token prompt>"}],
      "max_completion_tokens": 500,
      "reasoning_effort": "low"
    }' | tee /tmp/spike-experiment-1.json
  ```
  记录 `N_prompt_1, N_completion_1, N_reasoning_1`

- [ ] **实验 2（high reasoning_effort）**：
  - 同 prompt 同 `max_completion_tokens`，改 `reasoning_effort: "high"`
  - 记录 `N_prompt_2, N_completion_2, N_reasoning_2`
  - 期望 `N_reasoning_2 >> N_reasoning_1`

### Step 8.2: 检查 AiHubMix dashboard 结算时延

- [ ] 发送实验 1 后，轮询 dashboard https://aihubmix.com/dashboard：
  - T+0 min: 查一次
  - T+5 min: 查一次
  - T+30 min: 查一次
  - T+3 hr: 查一次
  - 记录 dashboard 显示该 request 扣费金额的最早时间

### Step 8.3: 计算对账

- [ ] **判定逻辑**：
  - 令 `input_price = $a/M`, `output_price = $b/M`（AiHubMix 官方 GPT 5.4 价目）
  - 实验 1 理论成本：
    - Option A 独立：`E1_A = N_prompt_1 × a + (N_completion_1 - N_reasoning_1) × b + N_reasoning_1 × r`（r 是 reasoning 独立价）
    - Option B 并入：`E1_B = N_prompt_1 × a + N_completion_1 × b`
  - Dashboard 实测 `E1_actual` 与 E1_A, E1_B 比对
  - 对实验 2 重复，两组数据给出 `r` 解方程
- [ ] 得出结论：A（独立计价，记录 r）/ B（并入 completion 价）/ 误差过大无法判定

### Step 8.4: 写 spike doc

- [ ] `docs/aihubmix-billing-reconciliation-spike.md` 章节：
  - §1 背景：reasoning_tokens 计价策略不确定性
  - §2 实验方法：2 次对照 curl（本 step）
  - §3 原始 curl 输出（含完整 JSON）
  - §4 Dashboard 结算时延实测
  - §5 对账计算 + 判定结果（A/B/C）
  - §6 行动建议：是否需 `pricing_rule` migration；如是，scope 独立 hotfix；如否，记录"已验证 AiHubMix 并入 completion 计价"
  - §7 附：Claude/Gemini/DeepSeek 同样方法跑一次（若时间允许）

### Step 8.5: 执行窗口

- [ ] 本 task 在 S4 早期（Task 1-3 完成后即可执行，不依赖其他代码 merge）
- [ ] 即使 dashboard 结算 > 1d，也能提前发起，S5 前一天完成结论
- [ ] 若 dashboard 实测 > 3d，触发 escalation：spike 结论延后到 S7 后独立 follow-up，但本期 pricing_rule 不改

**Acceptance criteria**：2 次实验数据齐全 + dashboard 截图 + 结论明确（A/B/C）。

---

## Task 9: Langfuse tracing middleware 改造 — S3-review-v2 修订

**目标**：把 `TraceMetadata` 写入 Langfuse generation 的 output map。**S3 review P0-2 修订**：实测 `internal/pkg/langfuse/helpers.go` 现有 SDK 不暴露 `UpdateGeneration` 或 `WithGenMetadata`。采用现有 pattern：沿用 `tracing.go:65` 的 `WithGenOutput(map{"metadata":...})` 把 resolved 字段嵌入 output map 的 metadata key。

**Files**:
- Modify: `internal/pkg/aiservice/middleware/tracing.go`

**依赖**：Task 1（TraceMetadata 类型） + Task 4（adapter 填 TraceMetadata） + Task 5（stream final chunk 填 TraceMetadata）

### Step 9.1: SDK pattern 确认（S3 已完成）

**S3 gate review 时实测 Langfuse SDK surface**（helpers.go）：
- ✅ `CreateGeneration(traceID, genID, opts ...GenOption)` — 创建 generation
- ✅ `EndGeneration(genID, opts ...GenOption)` — 结束 generation，支持 `WithGenOutput`
- ❌ `UpdateGeneration` / `WithGenMetadata` / `UpdateGenerationMetadata` 均**不存在**
- ✅ 现有 pattern（tracing.go:65）：`WithGenOutput(map[string]interface{}{"metadata": meta})` 把 metadata 嵌入 output map

**决策**：沿用现有 pattern。在 `EndGeneration` 调用的 `WithGenOutput` map 里把 `resolved_reasoning_effort` / `resolved_model_family` / `temp_overridden` 作为 `metadata` sub-map 的 keys，与现有 meta 合并。Langfuse UI 会在 generation 的 output 标签页展示完整 JSON，运营人员可 grep/filter。

### Step 9.2: 非流式 response 嵌入 TraceMetadata

- [ ] 在 tracing.go 非流式分支（~L156-171），修改 `safeOutput` 或直接扩展 `WithGenOutput` map：
  ```go
  // 构造 output map，合并 existing metadata 和 TraceMetadata
  outputMap := safeOutput(resp, meta).(map[string]interface{})  // 需确认 safeOutput 返回 map
  if cr, ok := resp.(*aiservice.ChatResponse); ok && cr.TraceMetadata != nil {
      // 合并到 output.metadata 子 map
      existingMeta, _ := outputMap["metadata"].(map[string]interface{})
      if existingMeta == nil {
          existingMeta = map[string]interface{}{}
      }
      existingMeta["resolved_reasoning_effort"] = cr.TraceMetadata.ResolvedReasoningEffort
      existingMeta["resolved_model_family"] = cr.TraceMetadata.ResolvedModelFamily
      existingMeta["temp_overridden"] = cr.TraceMetadata.TempOverridden
      outputMap["metadata"] = existingMeta
  }
  opts = append(opts, langfuse.WithGenOutput(outputMap))
  langfuse.EndGeneration(observationID, opts...)
  ```
- [ ] 若 `safeOutput` 返回非 map 类型，先 read safeOutput 实现决定怎么改（S4 Step 9.2 打开该文件确认）

### Step 9.3: 流式 channel 拦截 + final chunk TraceMetadata 捕获

- [ ] tracing.go stream middleware wrapper goroutine（~L81-129）在捕获 `lastUsage`/`lastModel` 的循环中加 `lastTraceMeta`：
  ```go
  var lastTraceMeta *aiservice.TraceMetadata
  for chunk := range upstreamCh {
      if chunk.IsFinal {
          if chunk.Usage != nil {
              lastUsage = chunk.Usage
          }
          if chunk.Model != "" {
              lastModel = chunk.Model
          }
          if chunk.TraceMetadata != nil {
              lastTraceMeta = chunk.TraceMetadata
          }
      }
      outCh <- chunk  // 透传
  }
  ```
- [ ] 循环结束后构造 EndGeneration output map（同 Step 9.2 的方式）并调用：
  ```go
  outputMap := safeOutput(nil, meta).(map[string]interface{})
  if lastTraceMeta != nil {
      // 同 Step 9.2 合并逻辑
      ...
  }
  opts = append(opts, langfuse.WithGenOutput(outputMap))
  langfuse.EndGeneration(observationID, opts...)
  ```

### Step 9.4: 单元测试（可选）

- [ ] 若 `tracing_test.go` 已存在且有 stub Langfuse client，加断言 "chat response with TraceMetadata ⇒ EndGeneration output map contains `metadata.resolved_reasoning_effort`"
- [ ] 若无测试基础设施，跳过（依靠 S5 dev Langfuse 实测验证）

**Acceptance criteria**：Langfuse UI 对一次 Claude thinking 调用的 trace 面板在 output 标签能看到 `{"metadata": {"resolved_reasoning_effort":"medium", "resolved_model_family":"claude", "temp_overridden":true}}`（S5 验证）。

---

## Task 10: SOP/Chatbot 入口 + Playwright E2E

**目标**：删 `_ = thinking` 占位，真实传递 Thinking 到 `ChatRequest`。+ Playwright 4 路径 E2E 验证端到端行为。

**Files**:
- Modify: `internal/numind/biz/sop/executor.go:108-113, 463-467`
- Modify: `internal/numind/biz/chatbot/stream.go:172-177`
- Modify: `internal/numind/biz/llmrouter/preference_test.go`（加 thinking 变体回归保护）
- Create: `numind-web-v3/e2e/aihubmix-thinking-audit.spec.ts`

**依赖**：Task 1（ChatRequest.Thinking 字段） + Task 4（adapter 已接住 Thinking）

### Step 10.1: executor.go 改造

- [ ] L109 删除 `_ = thinking // 待 Task 9 后续接通 Gateway thinking 模式`
- [ ] L108 `executeViaGateway` 调用点（L113）加 thinking 参数
- [ ] `executeViaGateway` 函数签名（若存在）加 `thinking bool`
- [ ] L463 `aiservice.ChatRequest{}` 字面量末尾加 `Thinking: thinking`

### Step 10.2: chatbot/stream.go 改造

- [ ] L172 `gatewayReq` 字面量末尾加 `Thinking: thinking`
- [ ] L177 删除 `_ = thinking`

### Step 10.3: grep 验证消除占位

- [ ] `grep -r "_ = thinking" internal/numind/` → 期望 0 结果

### Step 10.4: llmrouter preference 回归保护测试（S3 P1-D 修订：加反向 case）

- [ ] `preference_test.go` 加 `TestSavePreference_ThinkingVariantModel_Accepts`：
  - Seed ai_service 一行 `model_key="claude-sonnet-4-6-thinking", supports_thinking=1, thinking_only=1`
  - 调 `SavePreference(userID, feature, "claude-sonnet-4-6-thinking", thinking=true)`
  - Assert: 无 error，DB 行 thinking=1
- [ ] **P1-D 新增**：加 `TestSavePreference_ThinkingOnlyNoLongerForces_DeepSeek`：
  - Seed post-migration 状态 `model_key="deepseek-v3.2", supports_thinking=1, thinking_only=0`（migration 7a 改为 optional 后）
  - 调 `SavePreference(userID, feature, "deepseek-v3.2", thinking=false)`
  - Assert: 无 error，DB 行 thinking=0（**不**被 `preference.go:242` 强推为 1；migration 7a 后该模型已非 thinking_only）
- [ ] **P1-D 补充**：加 `TestSavePreference_ThinkingOnlyStillForces_Gemini`：
  - Seed 维持状态 `model_key="gemini-3.1-pro-preview", supports_thinking=1, thinking_only=1`（migration 7a 不改 Gemini base）
  - 调 `SavePreference(userID, feature, "gemini-3.1-pro-preview", thinking=false)`
  - Assert: 无 error，DB 行 thinking=1（`preference.go:242` 仍强推为 1；Gemini intrinsic 保留该语义）

### Step 10.5: Playwright E2E（S3 P1/P2 修订：从 4 → 8 条路径）

- [ ] 创建 `e2e/aihubmix-thinking-audit.spec.ts` 含 **8 条路径**（原 4 + S3 新增 4）：

  **原 4 条**：
  1. Claude thinking → 收到 thinking event + reasoning_content 非空
  2. SOP thinking → 收到 thinking SSE event
  3. GPT 5.4 → content 非空 + 前端不报错（无 reasoning_content 是预期）
  4. qwen-turbo（非 thinking 模型）→ 不收到 thinking event + 200

  **S3 新增 4 条（P2-5 scenarios）**：
  5. **Thinking=false 显式路径**：用户在 Chatbot 切到 Claude base → 关闭 thinking 开关 → 发消息 → 不收到 thinking event + content 非空 + 请求 body 不含 reasoning_effort（捕获 network request 断言）
  6. **Claude -think 变体路径**：用户在 ModelSelector 选 `claude-sonnet-4-6-thinking`（若 UI 暴露该 model_key；若未暴露则跳过并在 qa doc §6 记录限制）→ 发消息 → 收到 thinking event（因 -think 变体原生思考）
  7. **Gemini intrinsic 路径**：切到 Gemini base → 发消息 → **仍**收到 thinking event（因 intrinsic 思考不可关）+ 后端 trace metadata `resolved_reasoning_effort="intrinsic"`（可通过 qa doc §2 Langfuse 截图间接验证）
  8. **Preference 保存-thinking 变体 bug 回归**：切到 Claude thinking 变体 → 打开 thinking 开关 → 保存 → **不**收到 400 错误（migration 7a 前会 400，本期修复后应 200）

- [ ] 使用 `e2e/auth.setup.ts` 的 stored auth state
- [ ] 使用 `$E2E_USERNAME` / `$E2E_PASSWORD` 环境变量登录（avoid hardcoded credentials）
- [ ] 每条路径独立 `test(...)` block，失败隔离
- [ ] 使用 Playwright network capture (`page.route` 或 `page.on('request')`) 验证出站 API body 含/不含 `reasoning_effort`（case 5 特别需要）

### Step 10.6: 编译 + 测试

- [ ] `go build ./...` + `go vet ./...`
- [ ] `go test ./internal/numind/biz/... -v`
- [ ] `cd numind-web-v3 && npm run test:e2e -- aihubmix-thinking-audit.spec.ts`（S5 执行，本 task 只 create 文件）

**Acceptance criteria**：
- Go 侧编译 + 单测全绿
- grep 确认 `_ = thinking` 消失
- Playwright spec 文件语法正确（`npx playwright --list` 加载成功，S5 实际执行）

---

## Task 11: S5 验证策略文档 — NDF §10 要求

**目标**：独立 task 明确 S5 执行计划，确保 Playwright 覆盖够 + Langfuse 实测点清单。由 S3 Opus reviewer 审 plan 时一并审查。

**Files**:
- Create: `docs/superpowers/qa/2026-04-20-aihubmix-protocol-audit-qa.md`（在 S5 执行时填充结果）

**依赖**：Task 10 Playwright 规范定稿

### Step 11.1: S5 执行清单（plan 阶段写，S5 填结果）

- [ ] 文档骨架：
  - §1 Playwright E2E 4 路径执行结果（pass/fail + 截图或日志）
  - §2 Langfuse dev 实测（手动打开 Langfuse UI 验证一次 Claude thinking 调用的 trace 含所有期望字段）
  - §3 gstack /qa 补充截图（可选）：登录 dev 环境，切到 Chatbot，发一条深度思考消息，截图验证前端 thinking event 渲染正常
  - §4 DB migration 执行前后 verification query 结果
  - §5 计费 spike 结论引用
  - §6 已知偏差（如 Gemini 伪流式渲染体验、GPT 5.4 无 content 展示）

**Acceptance criteria**：骨架存在，S5 时补填。S3 plan 提交时只需骨架。

---

## 依赖图（P1-6 修订 — authoring vs execution 分离）

### Authoring（可并行）

```
Task 1 (types) ──────────────┬─→ Task 3 (oai wire) ──┐
                             │                       │
                             └─→ Task 5 (stream) ────┼─→ Task 4 (dmxapi dispatch)
                                                     │         │
Task 2 (family) ─────────────────────────────────────┘         │
                                                               ↓
Task 6 (registry) ─────────────────────────────────→ Task 9 (tracing)
                                                               │
                                                               ↓
                                                          Task 10 (entry + E2E)

Task 7a (migration SQL 文件) — 独立 authoring
Task 7b (pref audit SQL 文件) — 独立 authoring
Task 8 (spike) — 独立 authoring（S4 早期执行）
Task 11 (S5 qa 骨架) — 独立 authoring
```

### Execution / Deploy 序（S3 review-v2 修订 — P1-B pref bug 窗口最小化）

```
0. Task 8 spike 实验 curl：可在 S4 开始任意时刻发起（raw curl 不依赖 Numind 代码）
1. Task 1+2+3+6 合并并部署到 dev（基础类型 + registry 两字段读）
2. Task 7a migration 执行（DB 标志校正；此时 app 仍是旧代码拒 thinking=true，bug 未愈，但 DB 清洁）
3. Task 7b migration 执行（pref 审计 + normalize；旧 app 读新值不崩，行为等同）
4. Task 4+5 合并部署（adapter dispatch + stream transport — S3 P1-A：这两 task 签名互锁，MUST 同一 commit 或 back-to-back）
5. Task 10 合并部署（Thinking 从 executor/stream.go 真实流到 adapter；此刻 preference.go:246 bug 彻底自愈 + Thinking 管道激活）
6. Task 9 合并部署（Langfuse metadata 回传；可与 4+5 同批次）
7. Task 11 S5 qa 补填（整期部署完后）
```

**P1-A 修订**：Task 4（dmxapi.go 调 `runOAIStream(..., traceMeta)`）与 Task 5（`runOAIStream` 加 traceMeta 参数）**必须同一 commit 或连续两 commit** — 签名互锁，中间状态 `go build` 失败。实施者选 **方案 A**（合并单 commit）或 **方案 B**（Task 5 先 commit 加参数但保留旧 4-arg 调用点的兼容 wrapper，Task 4 后 commit 移除兼容 wrapper）。本 plan 默认方案 A（简单）。

**P1-B 修订**：Migration 7a 现在**提前**到 Task 4+5 部署之前。这样：
- 部署窗口 0→2 之间：app 旧代码 + DB 旧值 → 用户看到旧 bug（保持现状，没变坏）
- 部署窗口 2→4 之间：app 旧代码 + DB 新值 → 旧代码读新值不崩（preference.go:246 仍按新值做判断，thinking 变体现在 supports_thinking=1 因此请求通过）—— **bug 开始自愈**
- 部署窗口 4→5 之间：app 新代码 + DB 新值 → 完全正常
- 实际 user-facing bug 修复窗口：从 Task 7a migration 执行瞬间起（而非 Task 10 部署后）

**P1-C 修订**：Task 8 spike 实验 curl 不需要任何 Numind 代码变化（直接打 aihubmix.com），可在 S4 第 0 天发起，与 app 开发并行。即使 dashboard 结算慢 1-3 天也不阻塞 S5。

**其他关键约束**：
- Task 7a/7b SQL 文件 authoring 可与 Task 1-6 并行
- 全部完成后进入 S5 整体验证
- 跨仓库：Task 10 Step 10.5 的 Playwright spec 文件在 **numind-web-v3 repo**，走该仓库自己的 develop 分支 commit 流程（不是 numind-server）

---

## S5 验证策略（规则 10 要求，独立明确）

**选择**: Playwright E2E（Q3=A 决策锁定）

**理由**: thinking flag 是高风险业务逻辑（穿过 SOP/Chatbot 两大入口 + 影响计费 + 影响用户感知行为），须自动回归保护。gstack `/qa` 是一次性人工截图验收，不产生持久回归；仅后端 TDD 覆盖不到前端 SSE event 处理。

**关键用户路径**（4 条，Task 10 Step 10.5 已规定）：
1. Claude thinking 路径
2. SOP 思考节点路径
3. GPT 5.4 无 CoT 路径（前端不报错验证）
4. 非 thinking 模型 skip 路径

**后端单测覆盖**：
- adapter.InferModelFamily（18 用例）
- adapter.DMXAPI buildOAIRequest（8 wire-level 矩阵）
- adapter.oaiUsage.extractReasoningTokens（5 case）
- adapter.runOAIStream（SSE reasoning_tokens + TraceMetadata 透传）
- registry.GetResolvedRoute thinking 两字段读回
- llmrouter.SavePreference thinking 变体回归保护

**补充手动验证**（S5 qa doc §2）：
- Langfuse dev 面板实测一次 Claude thinking 调用的 trace 字段

---

## Build-manifest 更新检查点

每完成一个 task：
- [ ] `progress.completed_tasks += 1`
- [ ] `progress.current_task` 描述下一个 task
- [ ] 加一条 `decisions` 记录本 task 的关键决策或踩坑

两阶段 review 完成后：
- [ ] `progress.reviewed_tasks += 1`

S4 结束时：
- [ ] `progress.completed_tasks == total_tasks (11)` 且 `reviewed_tasks == completed_tasks`
- [ ] stage: `S4-done`

---

## 已解决的 S2 + S3 review P0/P1/P2（本 plan v2 覆盖）

### S2 Opus review（PASS_WITH_CONCERNS，0 P0 + 6 P1 + 7 P2）

| S2 Issue | Plan 覆盖位置 |
|----------|--------------|
| P1-1 流式 tracing 具体实现 | Task 9 Step 9.1 Langfuse SDK 实测（已改为 S3 完成的实测结论）+ Step 9.3 stream wrapper |
| P1-2 model family 前缀太宽松 | Task 2 Step 2.1 显式枚举 + Step 2.2 collision probe 测试 |
| P1-3 Gemini intrinsic 信号表达 | Task 4 Step 4.1 `"intrinsic"` 哨兵值（Q8=B） |
| P1-4 user_model_preference 存量审计 | Task 7b Q10=A |
| P1-5 spike 方法学 | Task 8 Step 8.1 2 次对照实验 + Step 8.2 dashboard 时延实测 |
| P1-6 Task 依赖图 authoring vs execution | 依赖图章节已分离 |
| P2-1 `-think` 仅对 Claude 有效的 comment | Task 2 Step 2.1 godoc |
| P2-2 `Tools` passthrough 缺失 | Tech debt 章节登记，不在本期 |
| P2-3 temp override 对 temp=0 的影响 | 已接受（Q4=A 决策，godoc 声明） |
| P2-4 `extractReasoningTokens` 是否简化 | 保留（防御性兼容 flat 写法） |
| P2-5 Thinking bool omitempty | Task 1 Step 1.2 去 omitempty |
| P2-6 Claude-think variant 测试 | Task 4 Step 4.4 case 7 |
| P2-7 scope 检查 | Q11=A 确认保留 TraceMetadata |

### S3 Opus review v1（FAIL，2 P0 + 4 P1 + 6 P2 — plan v2 修订）

| S3 Issue | Plan v2 覆盖位置 |
|----------|------------------|
| **P0-1 spec 文件不存在** | 已恢复（`git checkout develop -- spec_path`），commit 7013e94 已在 develop |
| **P0-2 Langfuse SDK 不匹配** | Task 9 Step 9.1 改为实测结论 + Step 9.2/9.3 用现有 `WithGenOutput(map{"metadata":...})` pattern，不需新 SDK |
| P1-A Task 4+5 签名互锁 | 部署序 Step 4 明确"Task 4+5 MUST 同 commit 或 back-to-back" |
| P1-B pref save bug 窗口 | 部署序修订：migration 7a 提前到 Task 4+5 合并之前，bug 自愈窗口从 Task 10 部署提前到 7a 执行瞬间 |
| P1-C Task 8 独立启动 | 部署序 Step 0：spike 可在 S4 第 0 天发起，不依赖 Task 4 merge |
| P1-D 反向测试 | Task 10 Step 10.4 加 2 个 case（ThinkingOnly 不再强推 + Gemini 仍强推） |
| P2-A tech debt 章节 | 见下方新增"§12 Tech Debt（不在本期）"章节 |
| P2-B Task 10 混合三事项 | 本期不拆（实施者可自行三 commit）；acceptance 按整体 task 验收 |
| P2-C 跨仓库 e2e | 部署序"关键约束"明确 Playwright spec 在 numind-web-v3 repo |
| P2-D 并行 authoring 冲突 | 部署序按顺序：Tasks 1+2+3+6 先部署（dmxapi.go/stream.go 不冲突），Task 4+5 后合并（修共享文件） |
| P2-E audit SQL 输出 sink | Task 7b Step 7b.2 改为 shell 脚本 + log 落盘 |
| P2-F rollback pre-check guard | Task 7a Step 7a.2 加 pre-check divide-by-zero guard |

---

## §12 Tech Debt（不在本期）

以下项目 S2 spec 或 S3 review 明确识别但不在本 feature scope 内，记录以防 S4 意外扩张：

1. **AiHubMix API key 硬编码**（`migrations/20260416_100000_seed_aihubmix_provider.sql`）→ 等 `SyncProviderCredentials` 机制完善后迁移，独立 hotfix
2. **`reasoning_effort` 硬编码 medium** → 未来 openrouter-provider feature 恢复时做用户/admin 可调
3. **`pricing_rule.reasoning_price_per_mtok` 缺列** → 若 Task 8 spike 判定 Option A（reasoning 独立计价），独立 hotfix 加列 + pricing migration
4. **Gemini 伪流式渲染体验**（4 chunk 批量）→ 前端改动，独立 UX improvement feature
5. **`ChatRequest.Tools` passthrough 缺失**（S2 review P2-2）→ dmxapi.go 当前忽略 `req.Tools`，独立 function-calling feature
6. **admin ServiceEdit.vue 标签 `thinking_only（仅 thinking 模式可用）`** → 建议改为 `仅 thinking 模式专用，强制启用思考`（S3 Agent C 提议），独立 UI tweak hotfix
7. **AiHubMix `-think` 后缀 on Gemini/GPT 返 400**（但 DB 不存在这些 provider_model_id，所以本期不触发）→ 未来 LinkAPI 接入时注意
