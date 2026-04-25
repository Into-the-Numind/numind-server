# Context Budget & Compression — 技术设计 Spec

> NDF S2 技术设计文档（feature: `context-budget-compression`）  
> Created: 2026-04-25  
> 输入：`numind-server/requirements/context-budget-compression.md` + `numind-server/proposals/context-budget-compression-proposal.md`  
> Superpowers: `brainstorming`  
> Status: **DRAFT — 等待 S2 Gate 人类确认设计方向**

**涉及仓库**：`numind-server`、`numind-admin-web`、`numind-web-v3`

---

## §0 Spec Overview

本 feature 建立一套通用 Context Budget & Compression 系统，覆盖模型上下文能力、调用前 token 估算、credits Reserve/Reconcile、上下文触顶压缩、后台摘要缓存、admin 配置校验、用户端输入计数和可观测性。

核心设计决策：

1. **策略输入统一为 `ContextFragment`**：SOP/chatbot/SalesRAG/admin tools/document/agent 场景只负责生产 fragment metadata；Context Budget Manager 不读取 SOP stage/node/template 来决定裁剪。
2. **预算执行放在 AI Gateway route-aware middleware 层**：在每个实际 route 已确定后、provider adapter 前运行预算/压缩/预扣费，保证 primary/fallback 模型能力、pricing、usage、trace 同源。
3. **token profile 独立于旧 `credit_estimation_coefficient`**：新增模型级 token estimation profile。旧 coefficient 只作为未迁移 legacy path 的 fallback，不继续承载新预算语义。
4. **`max_output_tokens` 与 `reserved_output_tokens` 分离**：前者是模型能力上限；后者是一次调用的输出预算，由 operation policy 决定。
5. **压缩边界由程序规则决定**：LLM 只生成摘要内容，不决定删除什么。
6. **后台压缩不阻断主流程**：失败记录事件并在下次同步触顶兜底。
7. **business LLM 调用扣用户积分；compression LLM 调用首版作为平台内部成本记录，不对用户额外扣分**：避免“后台无感压缩”变成用户不可见扣费。后续可通过 policy 打开 chargeable。

### 0.1 推荐方案与备选方案

**方案 A：业务层 preflight Budget Service**

- SOP/chatbot 等业务层在调用 Gateway 前自行 resolve route、预算、压缩、Reserve。
- 优点：改动局部、容易理解。
- 缺点：fallback route 可能与预算 route 不一致；各业务入口容易重复写 preflight；retry/fallback/usage/Reconcile 顺序难统一。

**方案 B：Gateway `ContextBudgetCredits` middleware（推荐）**

- 业务层只生产 `ContextFragment` 并把 user/operation 放入 context。
- Gateway 解析 route 后，在 `Fallback → ContextBudgetCredits → BillingUsageRecord → Retry → Adapter` 顺序中对每个实际 route 做预算、压缩、Reserve/Reconcile 和 usage record。
- 优点：primary/fallback 逐 route 预算；Retry 不会重复 Reserve；usage record、pricing snapshot、credits reservation 使用同一实际 route；所有 LLM 调用共享策略。
- 缺点：需要扩展 Gateway request type、middleware deps 和 credit reservation schema。

**方案 C：adapter/provider 级硬 guard**

- adapter 发请求前简单估算并裁剪 messages。
- 优点：最小侵入。
- 缺点：无法保护 current/critical fragment 语义；无法通用压缩；无法接 Reserve；不可接受。

本 spec 采用方案 B。

---

## §1 Architecture

### 1.1 逻辑图

```text
biz producer
  ├─ SOP producer / Chatbot producer / SalesRAG producer / future producers
  ├─ builds []ContextFragment
  └─ calls aiservice.ChatStream(ctx with billing/user meta, taskID, ChatRequest{ContextFragments})
                         │
                         ▼
AI Gateway
  ResolveTask / ResolveByModelKey
                         │ route has provider/model/capability/pricing
                         ▼
Middleware chain
  Tracing
    → Fallback
      → ContextBudgetCredits
        → BillingUsageRecord
          → Retry
            → Adapter
```

Key order rule:

- `Fallback` is outside `ContextBudgetCredits`, so fallback route is budgeted separately.
- `Retry` is inside `ContextBudgetCredits`, so one reservation covers all retry attempts for the same route and call.
- `BillingUsageRecord` is inside `ContextBudgetCredits` and receives the same actual route used for reservation. This avoids recording fallback success with primary provider/model/pricing.
- `ContextBudgetCredits` wraps streaming responses outside `BillingUsageRecord`, so it can finalize reservations after the stream closes while still letting Billing persist usage with the same route.

### 1.2 New packages

```text
numind-server/internal/pkg/contextbudget/
  fragment.go          # public fragment/value types
  estimator.go         # token estimation profile + estimator
  budget.go            # budget math + validation
  planner.go           # compression/drop/reference planning
  errors.go            # typed errors

numind-server/internal/pkg/aiservice/
  context_renderer.go  # contextbudget.ContextFragment -> aiservice.ChatMessage

numind-server/internal/numind/store/contextbudget.go
  # token profile, budget policy, summary cache, event persistence

numind-server/internal/numind/biz/contextbudget/
  biz.go               # admin CRUD + calibration + async summary orchestration
  producers.go         # shared helpers for producer packages

numind-server/internal/pkg/aiservice/middleware/context_budget.go
  # Gateway middleware: budget + compression + credits reserve/reconcile
```

Package ownership rules:

- `internal/pkg/contextbudget` is neutral and must not import `aiservice`, SOP/chatbot/salesrag packages, or any business package.
- `aiservice.ChatRequest` may import/use `contextbudget.ContextFragment`.
- Rendering fragments to `aiservice.ChatMessage` lives in package `aiservice` or `aiservice/middleware`, not in `contextbudget`, to avoid a Go import cycle.

---

## §2 Core Types

### 2.1 `ContextFragment`

```go
package contextbudget

type FragmentRole string
const (
    RoleImmutable   FragmentRole = "immutable"
    RoleRecent      FragmentRole = "recent"
    RoleDurable     FragmentRole = "durable"
    RoleEvidence    FragmentRole = "evidence"
    RoleWorking     FragmentRole = "working"
    RoleDiscardable FragmentRole = "discardable"
)

type FragmentSource string
const (
    SourceSystem    FragmentSource = "system"
    SourceUser      FragmentSource = "user"
    SourceAssistant FragmentSource = "assistant"
    SourceTool      FragmentSource = "tool"
    SourceFile      FragmentSource = "file"
    SourceKB        FragmentSource = "kb"
    SourceDB        FragmentSource = "db"
    SourceWeb       FragmentSource = "web"
    SourceInternal  FragmentSource = "internal"
)

type ContentType string
const (
    ContentText           ContentType = "text"
    ContentAttachment     ContentType = "attachment"
    ContentToolResult     ContentType = "tool_result"
    ContentReasoning      ContentType = "reasoning"
    ContentSummary        ContentType = "summary"
    ContentStructuredData ContentType = "structured_data"
)

type Compressibility string
const (
    CompressNone      Compressibility = "none"
    CompressSummarize Compressibility = "summarize"
    CompressReference Compressibility = "reference"
    CompressDrop      Compressibility = "drop"
)

type ContextFragment struct {
    ID              string            `json:"id"`
    Role            FragmentRole      `json:"role"`
    Source          FragmentSource    `json:"source"`
    ContentType     ContentType       `json:"content_type"`
    Content         string            `json:"content"`
    Importance      int               `json:"importance"` // 0..100
    Order           int               `json:"order"`
    Compressibility Compressibility   `json:"compressibility"`
    Critical        bool              `json:"critical"`
    ParentID        string            `json:"parent_id,omitempty"`
    SourceReference string            `json:"source_reference,omitempty"`
    Metadata        map[string]string `json:"metadata,omitempty"`

    TokenEstimate   int               `json:"token_estimate,omitempty"`
}
```

Rules:

- `Metadata` may include `sop_run_id`, `node_id`, `chat_session_id`, `document_id`, etc. for logging and trace only.
- `contextbudget` package must not branch on business metadata keys.
- `Content` may be empty only when `Compressibility=reference` and `SourceReference` is non-empty.

### 2.2 Critical derivation

A fragment is critical if any is true:

- `Critical=true`
- `RoleImmutable`
- current request fragment
- source is `system` with `CompressNone`
- user explicit instruction/negative instruction/manual edit/confirmed decision
- fragment metadata has `critical_reason`

Critical fragments may be summarized only when they are too large and `Compressibility=Summarize`; they must never be dropped.

### 2.3 `BudgetPolicy`

```go
type BudgetPolicy struct {
    Operation            string  `json:"operation"`
    ReservedOutputTokens int     `json:"reserved_output_tokens"`
    SafeRatio            float64 `json:"safe_ratio"` // default 0.85
    FixedOverheadTokens  int     `json:"fixed_overhead_tokens"`
    SoftThresholdRatio   float64 `json:"soft_threshold_ratio"` // default 0.70
    HardThresholdRatio   float64 `json:"hard_threshold_ratio"` // default 0.85
    ChargeUser           bool    `json:"charge_user"`
}
```

Default seed values:

| operation | reserved_output_tokens | safe_ratio | fixed_overhead_tokens | soft | hard | charge_user |
|-----------|------------------------|------------|-----------------------|------|------|-------------|
| `sop_run` | 16384 | 0.85 | 512 | 0.70 | 0.85 | true |
| `sop_chat` | 8192 | 0.85 | 512 | 0.70 | 0.85 | true |
| `chatbot_chat` | 8192 | 0.85 | 512 | 0.70 | 0.85 | true |
| `salesrag_chat` | 8192 | 0.85 | 1024 | 0.70 | 0.85 | true |
| `context_compression` | 4096 | 0.85 | 512 | 0.70 | 0.85 | false |
| `default_llm_chat` | 8192 | 0.85 | 512 | 0.70 | 0.85 | true |

### 2.4 Budget formula

```text
safe_input_budget = floor((context_window - reserved_output_tokens - fixed_overhead_tokens) * safe_ratio)
soft_threshold = floor(safe_input_budget * soft_threshold_ratio)
hard_threshold = floor(safe_input_budget * hard_threshold_ratio)
```

Validation:

- `context_window > 0`
- `max_output_tokens > 0`
- `max_output_tokens < context_window`
- `reserved_output_tokens > 0`
- `reserved_output_tokens <= max_output_tokens`
- `reserved_output_tokens + fixed_overhead_tokens < context_window`
- `0.50 <= safe_ratio <= 0.95`
- `safe_input_budget > 0`

---

## §3 Database Design

### 3.1 Existing `ai_service.capability_json`

No new columns are required for `context_window` or `max_output_tokens`; they remain inside `capability_json` and are decoded by `profile.ServiceCapability`.

Required LLM capability JSON shape:

```json
{
  "service_type": "llm",
  "context_window": 1000000,
  "max_output_tokens": 384000,
  "input_modalities": ["text"],
  "output_modalities": ["text"],
  "features": {"streaming": true, "tool_use": true},
  "capabilities": ["chat"]
}
```

Admin save must reject LLM services where `context_window` or `max_output_tokens` is missing or invalid.

### 3.2 New table: `token_estimation_profile`

Purpose: model/provider-level call-before-token estimation profile.

```sql
CREATE TABLE IF NOT EXISTS token_estimation_profile (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  provider VARCHAR(50) NOT NULL DEFAULT '',
  model VARCHAR(100) NOT NULL DEFAULT '',
  model_family VARCHAR(80) NOT NULL DEFAULT '',
  service_type VARCHAR(30) NOT NULL DEFAULT 'llm_chat',
  profile_json JSON NOT NULL,
  safety_multiplier DECIMAL(8,4) NOT NULL DEFAULT 1.1500,
  calibration_multiplier DECIMAL(8,4) NOT NULL DEFAULT 1.0000,
  calibration_sample_count INT NOT NULL DEFAULT 0,
  calibration_p50_abs_error DECIMAL(8,4) DEFAULT NULL,
  calibration_p90_abs_error DECIMAL(8,4) DEFAULT NULL,
  calibration_p99_under_ratio DECIMAL(8,4) DEFAULT NULL,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  is_active TINYINT(1) NOT NULL DEFAULT 1,
  is_fallback TINYINT(1) NOT NULL DEFAULT 0,
  change_reason VARCHAR(255) DEFAULT NULL,
  updated_by VARCHAR(80) DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_tep_lookup (provider, model, service_type, is_active),
  INDEX idx_tep_family (provider, model_family, service_type, is_active),
  INDEX idx_tep_fallback (is_fallback, service_type, is_active)
);
```

Active-version invariant:

- Biz layer must update profiles in a transaction.
- For the target key, it must lock existing versions using `SELECT ... FOR UPDATE`, deactivate active rows, then insert the new row.
- MySQL cannot express a portable partial unique index for `is_active=1`; therefore concurrency safety is a required biz/store contract and must have a race test similar to `credit_estimation_coefficient`.
- Query paths must sort active rows by `version DESC, id DESC` and warn if more than one active row is found, then use the newest row.

`profile_json` shape:

```json
{
  "version": 1,
  "method": "weighted_char_class",
  "message_overhead_tokens": 4,
  "fragment_overhead_tokens": 2,
  "classes": {
    "zh": {"token_per_char": 0.62},
    "en": {"token_per_char": 0.28},
    "code": {"token_per_char": 0.42},
    "json": {"token_per_char": 0.38},
    "markdown_table": {"token_per_char": 0.45},
    "symbol": {"token_per_char": 0.90},
    "mixed": {"token_per_char": 0.55}
  }
}
```

Lookup order:

1. exact `(provider, model, service_type, is_active=1)`
2. family `(provider, model_family, service_type, is_active=1)`
3. provider fallback `(provider, '', service_type, is_fallback=1, is_active=1)`
4. global fallback `('', '', service_type, is_fallback=1, is_active=1)`

If fallback is used, estimator multiplies by `max(profile.safety_multiplier, 1.30)`.

### 3.3 New table: `context_budget_policy`

```sql
CREATE TABLE IF NOT EXISTS context_budget_policy (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  operation VARCHAR(80) NOT NULL,
  reserved_output_tokens INT NOT NULL,
  safe_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  fixed_overhead_tokens INT NOT NULL DEFAULT 512,
  soft_threshold_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.7000,
  hard_threshold_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  charge_user TINYINT(1) NOT NULL DEFAULT 1,
  description VARCHAR(255) DEFAULT NULL,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  is_active TINYINT(1) NOT NULL DEFAULT 1,
  change_reason VARCHAR(255) DEFAULT NULL,
  updated_by VARCHAR(80) DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_cbp_operation_active (operation, is_active)
);
```

Versioning follows append-only semantics: upsert inserts a new version and deactivates prior active rows for the operation.

Active-version invariant is the same as token profiles: transaction + `SELECT ... FOR UPDATE` + deactivate old active row + insert new active row. S4 must include concurrent admin-save tests.

### 3.4 New table: `context_summary`

Stores async/sync compression results. It must not store cross-user content.

```sql
CREATE TABLE IF NOT EXISTS context_summary (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL,
  owner_user_id BIGINT UNSIGNED DEFAULT NULL
    COMMENT 'parent account owner for B2B2C scopes; equals user_id for standalone users',
  scope_type VARCHAR(40) NOT NULL COMMENT 'sop_run | chatbot_session | salesrag_session | document | internal',
  scope_id VARCHAR(100) NOT NULL,
  source_hash CHAR(64) NOT NULL,
  source_fragment_ids JSON NOT NULL,
  summary_text MEDIUMTEXT NOT NULL,
  summary_token_estimate INT NOT NULL DEFAULT 0,
  original_token_estimate INT NOT NULL DEFAULT 0,
  model VARCHAR(100) NOT NULL DEFAULT '',
  provider VARCHAR(50) NOT NULL DEFAULT '',
  status VARCHAR(20) NOT NULL DEFAULT 'ready' COMMENT 'pending | processing | ready | failed',
  error_message VARCHAR(500) DEFAULT NULL,
  created_by_operation VARCHAR(80) NOT NULL DEFAULT '',
  expires_at DATETIME DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_summary_scope_hash (owner_user_id, scope_type, scope_id, source_hash),
  INDEX idx_summary_user_scope (owner_user_id, scope_type, scope_id, status),
  INDEX idx_summary_status (status, updated_at)
);
```

Tenant rule:

- Every summary query and worker rebuild must constrain by `owner_user_id` and `scope_type/scope_id`.
- `owner_user_id` is the parent account id for child users; for normal users it equals `user_id`.
- No summary lookup may use only `scope_type/scope_id`.

### 3.5 New table: `context_budget_event`

Audit and observability table. It stores metadata only; it must not store full prompt content.

```sql
CREATE TABLE IF NOT EXISTS context_budget_event (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT UNSIGNED DEFAULT NULL,
  operation VARCHAR(80) NOT NULL,
  task_id VARCHAR(80) DEFAULT NULL,
  provider VARCHAR(50) NOT NULL DEFAULT '',
  model VARCHAR(100) NOT NULL DEFAULT '',
  context_window INT NOT NULL DEFAULT 0,
  max_output_tokens INT NOT NULL DEFAULT 0,
  reserved_output_tokens INT NOT NULL DEFAULT 0,
  fixed_overhead_tokens INT NOT NULL DEFAULT 0,
  safe_ratio DECIMAL(5,4) NOT NULL DEFAULT 0.8500,
  safe_input_budget INT NOT NULL DEFAULT 0,
  estimated_before INT NOT NULL DEFAULT 0,
  estimated_after INT NOT NULL DEFAULT 0,
  actual_prompt_tokens INT DEFAULT NULL,
  actual_completion_tokens INT DEFAULT NULL,
  reserve_amount BIGINT DEFAULT NULL,
  reconcile_delta BIGINT DEFAULT NULL,
  compression_actions JSON DEFAULT NULL,
  dropped_fragment_count INT NOT NULL DEFAULT 0,
  summarized_fragment_count INT NOT NULL DEFAULT 0,
  critical_fragment_count INT NOT NULL DEFAULT 0,
  calibration_ratio DECIMAL(10,4) DEFAULT NULL,
  token_profile_id BIGINT UNSIGNED DEFAULT NULL,
  budget_policy_id BIGINT UNSIGNED DEFAULT NULL,
  reservation_id BIGINT UNSIGNED DEFAULT NULL,
  usage_record_id BIGINT UNSIGNED DEFAULT NULL,
  status VARCHAR(30) NOT NULL DEFAULT 'ok' COMMENT 'ok | compressed | failed | skipped',
  error_code VARCHAR(80) DEFAULT NULL,
  metadata JSON DEFAULT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_cbe_user_created (user_id, created_at),
  INDEX idx_cbe_operation_created (operation, created_at),
  INDEX idx_cbe_status_created (status, created_at)
);
```

### 3.6 Existing `credit_reservation` changes

`credit_reservation.coefficient_id` currently assumes R2 char coefficient. New budget reservations need token profile snapshots.

Migration:

```sql
ALTER TABLE credit_reservation
  MODIFY coefficient_id BIGINT UNSIGNED NULL,
  ADD COLUMN estimation_source VARCHAR(30) NOT NULL DEFAULT 'credit_coefficient'
    COMMENT 'credit_coefficient | context_budget',
  ADD COLUMN token_profile_id BIGINT UNSIGNED DEFAULT NULL,
  ADD COLUMN estimated_prompt_tokens INT NOT NULL DEFAULT 0,
  ADD COLUMN estimated_completion_tokens INT NOT NULL DEFAULT 0,
  ADD COLUMN provider VARCHAR(50) NOT NULL DEFAULT '',
  ADD COLUMN model VARCHAR(100) NOT NULL DEFAULT '',
  ADD COLUMN context_budget_event_id BIGINT UNSIGNED DEFAULT NULL,
  ADD INDEX idx_cr_token_profile (token_profile_id),
  ADD INDEX idx_cr_budget_event (context_budget_event_id);
```

Existing rows remain valid with `estimation_source='credit_coefficient'`.

---

## §4 Token Estimation

### 4.1 Estimation algorithm

For each fragment:

1. Classify content into text classes: zh/en/code/json/markdown_table/symbol/mixed.
2. Sum `ceil(class_char_count * class.token_per_char)`.
3. Add `fragment_overhead_tokens`.
4. Multiply by `calibration_multiplier`.
5. Multiply by `safety_multiplier`.
6. Round up.

Total prompt estimate:

```text
estimated_prompt_tokens =
  sum(fragment estimates) +
  message_overhead_tokens * rendered_message_count +
  fixed_overhead_tokens
```

The estimator must prefer conservative over under-estimated values.

### 4.2 Calibration

After provider usage is available:

```text
calibration_ratio = actual_prompt_tokens / estimated_prompt_tokens_before_safety
absolute_error = abs(actual_prompt_tokens - estimated_prompt_tokens) / actual_prompt_tokens
```

S4 implementation may update profile metrics automatically, but active multiplier changes require either:

- admin review and explicit save, or
- a bounded automatic update rule documented in code and audit log.

Initial S4 should record metrics and expose them in admin; automatic multiplier mutation is deferred unless S3 explicitly scopes it.

### 4.3 Evaluation dataset

S5 must include a local deterministic dataset:

- Chinese sales copy
- English prose
- Go/TypeScript code
- Markdown table
- JSON payload
- symbol-heavy text
- mixed Chinese/English/URL/emoji-like symbols
- attachment reference text
- RAG evidence chunks

Gate metrics:

- P50 absolute error <= 5%
- P90 absolute error <= 10%
- No systematic under-estimation at P99 after safety multiplier

If exact provider tokenizer is unavailable, actual usage from seeded fixture responses or recorded usage rows may be used for evaluation.

---

## §5 Budget + Compression Flow

### 5.1 Gateway middleware flow

For each `Chat` / `ChatStream` request, `ContextBudgetCredits` runs after `Fallback` has selected the actual route:

1. If `req.ContextFragments` is empty:
   - legacy path: skip compression; still emit `context_budget_event.status='skipped'` for migrated operations only.
   - S4 must migrate SOP Gateway and chatbot so they always send fragments.
2. Normalize operation from `billing.FromContext(ctx).Operation`; fallback to `default_llm_chat` only for uncharged internal calls.
3. Read `route.Capability.ContextWindow` and `route.Capability.MaxOutputTokens`.
4. Validate policy and model capability.
5. Load token estimation profile.
6. Estimate fragments and total prompt.
7. If `estimated <= safe_input_budget`, render fragments to messages and continue.
8. If `estimated > safe_input_budget`, run compression planner.
9. Re-estimate after every compression phase.
10. If still over budget after all legal actions, return typed context budget error before provider call.
11. If policy `ChargeUser=true` and user billing context exists, run credit precheck and Reserve credits.
12. Inject budget metadata into context so inner `BillingUsageRecord` writes the same event/reservation ids.
13. Call provider via `BillingUsageRecord → Retry → Adapter`.
14. For non-streaming responses, finalize reservation immediately after the inner handler returns.
15. For streaming responses, return a wrapped channel and finalize only after the wrapped stream observes terminal chunk, stream close, cancellation, or missing usage timeout.
16. Persist/update `context_budget_event`; attach event id to usage record metadata.

### 5.1.1 Route-aware billing requirement

`BillingUsageRecord` must be moved inside `Fallback` and inside `ContextBudgetCredits`:

```text
Tracing → Fallback → ContextBudgetCredits → BillingUsageRecord → Retry → Adapter
```

Required behavior:

- Billing builds `UsageRecord` from the route parameter it receives, not from the original primary route.
- Fallback success must record fallback provider/model/pricing.
- ContextBudgetCredits reservation provider/model must match the UsageRecord provider/model.
- Budget metadata is passed via context to Billing and merged into `usage_record.metadata`.
- Existing tests that assert fallback `IsFallback=true` must be extended to assert fallback provider/model/pricing snapshots.

### 5.1.2 Streaming finalization contract

For `ChatStream`, `ContextBudgetCredits` must wrap the returned `<-chan aiservice.ChatChunk>` like Billing already does.

Wrapper rules:

- Reserve is created before returning the stream channel.
- The wrapper forwards every chunk unchanged.
- It records final usage from terminal chunk `chunk.IsFinal && chunk.Usage != nil`.
- If terminal chunk has `chunk.Err != nil`, finalize as error:
  - if usage exists, Reconcile with actual cost;
  - otherwise Refund with reason mapped from the error (`provider_timeout`, `user_cancelled`, or `op_failed`).
- If the channel closes without final usage, finalize with estimated cost and mark `calibration_skipped=true`.
- If caller context is cancelled before final usage, Refund unless a usage-bearing terminal chunk has already been observed.
- Finalization must be idempotent; double terminal chunks or double close must not double Reconcile/Refund.

### 5.2 Compression planner phases

Planner is deterministic and metadata-driven:

1. **Reuse summary cache**: replace matching old fragment groups with ready `context_summary` fragments.
2. **Reference large evidence/file fragments**: replace long attachment/evidence content with source reference + short preserved facts when source is retrievable.
3. **Summarize compressible durable/recent/tool fragments**: call `context.compression.summary` task profile to generate source-linked summaries.
4. **Drop low-value discardable fragments**: lowest importance, oldest order first.
5. **Drop non-critical working fragments**: oldest first.
6. **Minimal recent fallback**: keep all immutable/critical/current request and only newest recent fragments that fit.

The planner must never inspect business metadata keys to rank fragments.

### 5.3 Compression LLM prompt contract

Compression prompt input:

- source fragment ids
- content
- required facts to preserve
- maximum target tokens
- output schema

Output schema:

```json
{
  "summary": "...",
  "preserved_facts": [
    {"text": "...", "source_fragment_id": "frag-1"}
  ],
  "source_fragment_ids": ["frag-1", "frag-2"]
}
```

Validation:

- All output `source_fragment_ids` must be subset of input.
- Empty summary is failure.
- Summary fragment gets `ContentType=summary`, `Compressibility=none`, `Source=internal`, `ParentID` or `SourceReference` pointing to source ids.

### 5.4 Async background compression

Trigger:

- After a successful business LLM call, if accumulated scope estimate exceeds `soft_threshold`.

Scope key examples:

- SOP run: `scope_type=sop_run`, `scope_id=<run_id>`
- Chatbot: `scope_type=chatbot_session`, `scope_id=<session_id>`

Execution:

- enqueue by inserting/upserting `context_summary(status='pending')`
- in-process worker picks pending rows
- worker rebuilds fragments from the scope producer
- runs summary generation using operation `context_compression`
- writes `ready` or `failed`

Failure:

- never blocks user request
- logs warn
- writes `context_budget_event.status='failed'`
- next sync over-budget request can still compress inline

---

## §6 Credits Integration

### 6.1 Context requirements

Business code must call Gateway with billing context:

```go
ctx = billing.WithBillingMeta(ctx, userID, string(credit.OpSopRun), map[string]string{
    "biz_ref_type": "sop_run",
    "biz_ref_id":   strconv.FormatUint(uint64(runID), 10),
})
```

If billing context is absent:

- budget/compression still runs
- credits Reserve is skipped
- event records `metadata.billing_context_missing=true`
- this is allowed for internal/admin non-user operations only

### 6.1.1 Operation normalization

Billing operation strings and credit operation strings are currently not identical. `ContextBudgetCredits` must normalize operation before selecting policy or charging credits.

| incoming `billing.Operation` | normalized budget operation | credit operation | charge user |
|------------------------------|-----------------------------|------------------|-------------|
| `sop_node_execute` | `sop_run` | `credit.OpSopRun` | true |
| `sop_run` | `sop_run` | `credit.OpSopRun` | true |
| `sop_chat` | `sop_chat` | `credit.OpSopChat` | true |
| `chatbot_chat` / `chatbot.stream` | `chatbot_chat` | new `credit.OpChatbotChat` if not present | true |
| `salesrag_chat` | `salesrag_chat` | `credit.OpSalesragChat` | true |
| `context_compression` | `context_compression` | none | false |
| unknown with user billing context | error `ErrContextConfigInvalid` | none | false |
| unknown without user billing context | `default_llm_chat` | none | false |

S4 must not silently charge `default_llm_chat` for a user operation. Unknown user operations are configuration errors.

### 6.1.2 Legacy-tier dispatch

Gateway-side charging must preserve existing effective-legacy behavior:

1. If `ChargeUser=true`, load a fresh user by `billing.UserID` before Reserve.
2. Call `ICreditService.CheckAndEstimate` or an equivalent new budget-aware precheck that preserves `isEffectiveLegacy`.
3. If precheck returns `SkipDeduction=true`, do not create a reservation.
4. If legacy membership is insufficient, return the same typed insufficient error semantics used by existing business-layer callers.

`ReserveBudget` must never bypass legacy dispatch. It should be exposed on `ICreditService`, not called directly on `creditsImpl`.

### 6.2 New credit service method

Add a budget-specific reserve API instead of forcing old coefficient semantics:

```go
type BudgetReservationInput struct {
    Operation                 Operation
    EstimatedCredits          int64
    EstimatedPromptTokens     int
    EstimatedCompletionTokens int
    Provider                  string
    Model                     string
    TokenProfileID            *uint64
    ContextBudgetEventID      *uint64
    IdempotencyKey            *string
}

ReserveBudget(ctx context.Context, user *model.User, in BudgetReservationInput) (*Reservation, error)
```

`ReserveBudget` uses the same FIFO package deduction and reservation item logic as existing `Reserve`, with:

- `estimation_source='context_budget'`
- `coefficient_id=NULL`
- token/profile/model/provider snapshots populated

Existing `Reserve` remains for legacy R2 callers during migration.

`CheckAndEstimateBudget` companion:

```go
type BudgetPrecheckInput struct {
    Operation                 Operation
    EstimatedCredits          int64
    EstimatedPromptTokens     int
    EstimatedCompletionTokens int
    Provider                  string
    Model                     string
    TokenProfileID            *uint64
}

CheckAndEstimateBudget(ctx context.Context, user *model.User, in BudgetPrecheckInput) (*PreCheckResult, error)
```

This method reuses existing balance and legacy-tier logic, but accepts token-budget-estimated credits instead of `PromptChars`.

### 6.3 Estimated credits

Before provider call:

```text
estimated_cost_cents = pricing.CalculateCost(
  service_type=llm_chat,
  provider=route.Provider.Name,
  model=route.ServiceKey or provider model mapping policy,
  prompt_tokens=estimated_prompt_tokens,
  completion_tokens=reserved_output_tokens
)
estimated_credits = max(1, ceil(estimated_cost_cents))
```

Use the same pricing source of truth as `usage_record.cost_cents`.

### 6.4 Reconcile

After provider returns usage:

```text
actual_cost_cents = pricing.CalculateCost(
  prompt_tokens=usage.prompt_tokens,
  completion_tokens=usage.completion_tokens
)
```

Then call existing `Reconcile(reservationID, actual_cost_cents)`.

If usage is missing:

- use estimated cost as actual for Finalize
- set usage_record `is_estimated=true`
- event metadata `calibration_skipped=true`

### 6.5 Compression call charging

For v1:

- `context_compression` policy has `charge_user=false`
- compression LLM calls still create `usage_record` for platform cost observability
- no user credit reservation is created for compression

This avoids hidden background charges. A later policy change can make compression chargeable if product decides to expose it.

---

## §7 API Contracts

All admin endpoints require admin token and use `core.WriteResponse`.

### 7.1 Token profile APIs

#### `GET /v1/admin/context-budget/token-profiles`

Query:

```json
{
  "provider": "dmxapi",
  "model": "deepseek-v4-pro",
  "service_type": "llm_chat",
  "is_active": "active|inactive|all",
  "page": 1,
  "page_size": 20
}
```

Response:

```json
{
  "list": [
    {
      "id": 1,
      "provider": "dmxapi",
      "model": "deepseek-v4-pro",
      "model_family": "deepseek",
      "service_type": "llm_chat",
      "profile_json": {},
      "safety_multiplier": 1.15,
      "calibration_multiplier": 1.0,
      "calibration_sample_count": 42,
      "calibration_p50_abs_error": 0.03,
      "calibration_p90_abs_error": 0.08,
      "calibration_p99_under_ratio": 0.01,
      "version": 2,
      "is_active": true,
      "is_fallback": false,
      "updated_by": "admin",
      "updated_at": "2026-04-25T16:00:00+08:00"
    }
  ],
  "total": 1
}
```

#### `GET /v1/admin/context-budget/token-profiles/history`

Required query: `provider`, `model`, `service_type`. Returns all versions.

#### `POST /v1/admin/context-budget/token-profiles`

Creates a new active version, demoting prior active rows for the same lookup key.

#### `PUT /v1/admin/context-budget/token-profiles/:id`

Appends a new version using the body values. Does not update in place.

#### `DELETE /v1/admin/context-budget/token-profiles/:id`

Soft-deactivates the row.

### 7.2 Budget policy APIs

#### `GET /v1/admin/context-budget/policies`

Returns active policy list, with history optionally via `is_active=all`.

#### `PUT /v1/admin/context-budget/policies/:operation`

Appends a new active policy version. Validates budget fields without a model; model-specific validation happens in preview and service edit.

### 7.3 Preview API

#### `POST /v1/admin/context-budget/preview`

Body:

```json
{
  "service_id": 24,
  "operation": "sop_run",
  "fixed_overhead_tokens": 512,
  "reserved_output_tokens": 16384,
  "safe_ratio": 0.85
}
```

Response:

```json
{
  "context_window": 1000000,
  "max_output_tokens": 384000,
  "reserved_output_tokens": 16384,
  "safe_input_budget": 835638,
  "valid": true,
  "warnings": []
}
```

### 7.4 AI Service API changes

Existing endpoints:

- `GET /v1/admin/ai/services/:id`
- `POST /v1/admin/ai/services`
- `PUT /v1/admin/ai/services/:id`
- `GET /v1/admin/ai/capability-schema`

Changes:

- LLM capability schema marks `context_window` and `max_output_tokens` required.
- Create/update validates the capability fields server-side.
- Response shape remains backward-compatible because fields stay in `capability_json`.

---

## §8 Frontend Specs

### 8.1 Admin Web

Pages:

- `AIService/ServiceEdit.vue`
  - show typed fields for `context_window` and `max_output_tokens`
  - show safe budget preview using active/default policy
  - validate on blur and save
  - show risk hint: these fields affect Reserve, compression, and failure rate

- New or renamed page under AI Services group:
  - path: `/ai-services/context-budget`
  - DataTable tabs or segmented view: Token Profiles / Budget Policies / Recent Events
  - CRUD token profiles
  - CRUD budget policies
  - view calibration metrics

UI hard rules:

- Use existing `DataTable`, `AppButton`, `AppInput`, `AppSelect`, `ConfirmModal`, `AppToast`.
- No card grid for admin management lists.
- All async views handle loading/empty/error/success.
- Validation happens on blur and on submit.

### 8.2 User Web

Apply `x / 40000` character counter to high-frequency text inputs:

- SOP current step input
- Chatbot input
- SalesRAG input if present in `numind-web-v3`

Rules:

- `40000` is a UX character limit only.
- Do not display token budget or “还能输入约 X 字”.
- Attachments, KB, web/db results are excluded from this counter.
- On exceed, block submit or show inline error.

---

## §9 Producer Contracts

### 9.1 SOP producer

SOP must map runtime context to fragments:

- system/node instruction → `immutable`, `source=system`, `compressibility=none`
- current user input/current step request → `recent`, `source=user`, `critical=true`, `compressibility=none`
- previous assistant outputs → `durable` or `recent`, `source=assistant`, `compressibility=summarize`
- previous attachments → `evidence`, `source=file`, `compressibility=reference|summarize`
- historical reasoning → `working`, `source=assistant`, `content_type=reasoning`, `compressibility=drop|summarize`

SOP producer may include node ids in metadata for audit only.

### 9.2 Chatbot producer

- system persona/config → immutable
- current message → recent + critical
- recent chat turns → recent
- older chat turns → durable + summarize
- mounted KB chunks → evidence + reference/summarize

### 9.3 Future producers

SalesRAG/admin tools/document processing must implement the same producer interface. They must not add policy branches to `contextbudget`.

---

## §10 Error Handling

Typed errors:

```go
var (
    ErrContextConfigInvalid = errors.New("context budget: invalid config")
    ErrTokenProfileMissing  = errors.New("context budget: token profile missing")
    ErrContextTooLarge      = errors.New("context budget: context too large")
    ErrCurrentInputTooLarge = errors.New("context budget: current input too large")
    ErrCompressionFailed    = errors.New("context budget: compression failed")
)
```

HTTP/user mapping:

- current input too large: “当前输入太长，请减少本次输入或附件后重试。”
- context too large after legal compression: “上下文过长，系统已尝试压缩但仍超过模型限制，请减少历史内容或附件后重试。”
- config invalid: admin-visible configuration error; user path returns generic retry/contact support message.

Gateway behavior:

- budget/config errors happen before provider call and are non-retryable
- provider retry/fallback still works for provider errors
- compression failure in sync path falls back to deterministic reference/drop phases before returning error
- async compression failure never bubbles to user

---

## §11 Observability

### 11.1 Langfuse topology

Root traces:

- SOP keeps existing SOP trace; Gateway generations attach under it.
- Chatbot keeps `chatbot-chat` trace; Gateway generations attach under it.
- Compression uses generation name `context.compression.summary`.

Gateway business LLM generation metadata:

```json
{
  "context_budget_event_id": 123,
  "context_window": 1000000,
  "max_output_tokens": 384000,
  "reserved_output_tokens": 16384,
  "safe_ratio": 0.85,
  "fixed_overhead_tokens": 512,
  "safe_input_budget": 835638,
  "estimated_before": 120000,
  "estimated_after": 42000,
  "compression_actions": ["summary", "reference"],
  "dropped_fragment_count": 2,
  "summarized_fragment_count": 4,
  "critical_fragment_count": 3,
  "token_profile_id": 7,
  "token_profile_fallback": false,
  "calibration_skipped": false
}
```

### 11.2 UsageRecord metadata

`usage_record.metadata` must include:

- `context_budget_event_id`
- `estimated_prompt_tokens_before`
- `estimated_prompt_tokens_after`
- `reserved_output_tokens`
- `safe_input_budget`
- `compression_status`
- `token_profile_id`
- `budget_policy_id`
- `reservation_id` if charged

### 11.3 Logs

Warn-level logs:

- token profile fallback
- compression failed
- context still over budget after compression
- provider usage missing
- calibration outlier

Info-level logs:

- compression action summary
- async compression queued/completed

No full prompt content in logs.

---

## §12 Migration Strategy

Phase order for S4:

1. Add DB tables and model/store/biz scaffolding.
2. Add token estimator and budget math tests.
3. Add Gateway request extension and `ContextBudgetCredits` middleware in no-charge/dry-run mode behind policy.
4. Migrate SOP Gateway producer to fragments.
5. Migrate chatbot producer to fragments.
6. Enable credits Reserve/Reconcile through middleware for migrated operations; remove duplicate business-layer Reserve for those paths.
7. Add admin UI for capabilities, token profiles, policies.
8. Add user web `x / 40000` counters.
9. Add async summary worker.
10. Run evaluation and E2E.

Compatibility:

- Existing LLM calls without fragments continue working initially.
- Non-migrated operations keep old R2 `credit_estimation_coefficient` path until explicitly migrated.
- `credit_reservation.coefficient_id` becomes nullable for new context-budget reservations.

Rollback:

- Disable active `context_budget_policy` or feature flag to skip budget middleware for business calls.
- Revert producers to message-only calls.
- New tables can remain inert.
- Reservations with `estimation_source=context_budget` remain auditable.

---

## §13 Testing Plan

### 13.1 Backend unit tests

- token estimator classifies text classes and applies multipliers
- fallback token profile lookup order
- budget formula and invalid config validation
- compression planner never drops critical/immutable/current fragments
- planner ranking ignores SOP metadata
- renderer preserves message order
- Gateway middleware order: Fallback → ContextBudgetCredits → BillingUsageRecord → Retry
- Retry does not create multiple reservations
- fallback route gets its own budget
- usage missing finalizes reservation with estimated cost
- compression call `charge_user=false` creates no reservation

### 13.2 Backend integration tests

- SOP producer emits required fragments for a synthetic run
- Chatbot producer emits system/current/history/KB fragments
- ReserveBudget writes reservation snapshot and FIFO items
- Reconcile updates context_budget_event with actual usage
- admin CRUD versioning for profiles and policies
- AI service update rejects invalid `context_window/max_output_tokens`

### 13.3 Frontend tests

- Admin profile list loading/empty/error/success
- Admin profile create/update validation
- ServiceEdit safe budget preview
- User input `x / 40000` counter and submit blocking

### 13.4 S5 verification

- `task lint`
- `go test ./...`
- `npm run lint && npm run type-check` in both frontends
- Playwright for changed user/admin input/config flows
- Langfuse local verification: at least one SOP/chatbot call shows budget metadata and generation usage
- Token estimation evaluation report with P50/P90/P99

---

## §14 PRD Coverage Proof

| PRD requirement | Spec coverage |
|-----------------|---------------|
| 通用 `ContextFragment`，不绑定 SOP stage/node/template | §2, §5.2, §9 |
| context_window / max_output_tokens / reserved_output_tokens | §2.3, §2.4, §3.1, §8.1 |
| 85% safe ratio | §2.3, §2.4 |
| token 预估驱动 Reserve 和 context budget | §4, §5.1, §6 |
| provider usage 仅用于 Reconcile/校准 | §4.2, §6.4 |
| profile 缺失 fallback + 更高 multiplier | §3.2, §4 |
| 智能压缩 | §5.2, §5.3 |
| 后台无感压缩 | §5.4, §6.5 |
| 失败兜底 | §5.2, §10 |
| admin web 配置/校验 | §7, §8.1 |
| 用户端 `x / 40000` | §8.2 |
| 可观测性 | §11 |
| 不自动切换模型 | §0, §5.1；fallback 仍仅用于 provider failure，不为扩窗自动切换 |
| 当前请求不裁剪 | §2.2, §5.2, §9 |

---

## §15 S3 Planning Notes

S3 must split backend before frontend:

1. DB migrations + models + stores
2. estimator/budget/planner pure package
3. credit reservation extension
4. Gateway middleware
5. SOP producer migration
6. Chatbot producer migration
7. admin APIs
8. admin UI
9. user input counters
10. async worker + summary cache
11. observability/evaluation/S5 fixtures

Do not combine producer migration with core budget package in one task; each producer must be independently buildable and testable.
