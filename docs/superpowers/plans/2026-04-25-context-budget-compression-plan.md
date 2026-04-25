# Context Budget Compression Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a generic ContextFragment-based budget, compression, and credit reservation system for all LLM Gateway calls, with admin configuration, user input counters, observability, and safe fallback behavior.

**Architecture:** Business code produces generic `contextbudget.ContextFragment` values and passes them to the AI Gateway. The Gateway runs route-aware budgeting after fallback route selection and before provider adapters, then performs deterministic compression, budget Reserve/Reconcile, usage recording, and Langfuse metadata emission against the actual provider/model route. Admin web manages token profiles and operation policies; user web only shows character counters for the current user input.

**Tech Stack:** Go 1.24, Gin, GORM, MySQL migrations, Vue 3, TypeScript, Vite, Vitest, AI Gateway middleware, existing credits Reserve/Reconcile, Langfuse metadata.

---

## File Structure

Backend files:

- Create `numind-server/migrations/20260425_172000_context_budget_compression.sql`: forward schema for token profiles, policies, summary cache, event log, credit reservation columns, and default seeds.
- Create `numind-server/migrations/20260425_172000_context_budget_compression_rollback.sql`: reverse schema operations.
- Create `numind-server/internal/pkg/contextbudget/fragment.go`: generic fragment, policy, estimate, plan, and event value types.
- Create `numind-server/internal/pkg/contextbudget/estimator.go`: weighted character-class token estimator.
- Create `numind-server/internal/pkg/contextbudget/budget.go`: model capability and policy validation plus budget math.
- Create `numind-server/internal/pkg/contextbudget/planner.go`: deterministic compression/drop/reference planner that ignores business metadata.
- Create `numind-server/internal/pkg/contextbudget/errors.go`: typed context budget errors.
- Create `numind-server/internal/pkg/contextbudget/*_test.go`: estimator, budget, planner, and error tests.
- Modify `numind-server/internal/pkg/aiservice/types.go`: add `ContextFragments []contextbudget.ContextFragment` to `ChatRequest`.
- Create `numind-server/internal/pkg/aiservice/context_renderer.go`: fragment to `ChatMessage` rendering.
- Modify `numind-server/internal/pkg/aiservice/middleware/chain.go`: add ContextBudget dependency fields and middleware order.
- Create `numind-server/internal/pkg/aiservice/middleware/context_budget.go`: route-aware budget, compression, Reserve/Reconcile, event persistence, stream finalization.
- Modify `numind-server/internal/pkg/aiservice/middleware/billing.go`: merge budget metadata into usage records and preserve actual fallback route snapshots.
- Modify `numind-server/internal/pkg/aiservice/middleware/*_test.go`: fallback billing, streaming finalization, and chain order coverage.
- Create `numind-server/internal/numind/store/contextbudget.go`: GORM store for token profiles, policies, summaries, and events.
- Create `numind-server/internal/numind/store/contextbudget_test.go`: active-version transaction, tenant-scoped summary, and event tests.
- Modify `numind-server/internal/pkg/model/credit.go`: add credit reservation metadata fields.
- Create or modify `numind-server/internal/pkg/model/contextbudget.go`: GORM models for new context budget tables.
- Modify `numind-server/internal/numind/biz/credit/types.go`, `contracts.go`, `credit_service.go`, `estimate.go`: add budget precheck/reserve inputs while preserving legacy-tier dispatch.
- Modify `numind-server/internal/numind/biz/credit/*_test.go`: credits-mode, legacy-tier, idempotency, and reservation metadata tests.
- Create `numind-server/internal/numind/biz/contextbudget/biz.go`: middleware-facing prepare/finalize service plus admin validation and preview helpers.
- Create `numind-server/internal/numind/biz/contextbudget/producers.go`: shared producer helpers.
- Create `numind-server/internal/numind/biz/contextbudget/worker.go`: background summary queue and failure recording.
- Create `numind-server/internal/numind/biz/contextbudget/biz_test.go`: prepare/finalize, admin validation, and preview tests.
- Create `numind-server/internal/numind/biz/contextbudget/worker_test.go`: background summary worker tests.
- Modify `numind-server/internal/numind/biz/sop/executor.go`, `sop.go`: produce SOP run/chat fragments and remove direct gateway-path Reserve where middleware owns it.
- Modify `numind-server/internal/numind/biz/sop/*_test.go`: fragment producer and no double-reserve regression coverage.
- Modify `numind-server/internal/numind/biz/chatbot/chatbot.go`, `stream.go`: produce chatbot fragments.
- Modify `numind-server/internal/numind/biz/chatbot/*_test.go`: chatbot fragment coverage.
- Modify `numind-server/internal/numind/biz/salesrag/salesrag.go`: produce SalesRAG chat/profile/chat-style fragments for Gateway calls.
- Modify `numind-server/internal/numind/biz/salesrag/salesrag_credits_integration_test.go`: SalesRAG fragment and credit regression coverage.
- Modify `numind-server/internal/numind/biz/salesrag/adapter/strategy_router.go`: produce fragments for SalesRAG strategy-selection Gateway calls.
- Modify `numind-server/internal/numind/biz/salesrag/adapter/strategy_router_test.go`: strategy-selection fragment coverage.
- Modify `numind-server/internal/numind/biz/aiservice_admin/biz.go`: require LLM `context_window` and `max_output_tokens`.
- Modify `numind-server/internal/numind/controller/v1/admin_ai/ai_service.go`: return capability validation errors from biz.
- Create `numind-server/internal/numind/controller/v1/admin_contextbudget/context_budget.go`: admin context-budget controller.
- Modify `numind-server/internal/numind/admin_router.go`: register `/v1/admin/context-budget/*`.
- Modify `numind-server/internal/numind/numind.go`: wire context budget store, biz, middleware deps, and worker.

Admin web files:

- Modify `numind-admin-web/src/types/ai.ts`: add typed capability fields for LLM services.
- Modify `numind-admin-web/src/api/ai.ts`: add context budget API clients.
- Create `numind-admin-web/src/views/AIService/ContextBudget.vue`: token profile, policy, recent event, calibration metric, and preview management screen.
- Modify `numind-admin-web/src/views/AIService/ServiceEdit.vue`: validate and edit LLM context fields.
- Modify `numind-admin-web/src/router/index.ts`: route context budget management page.

User web files:

- Create `numind-web-v3/src/utils/inputBudget.ts`: shared character counter helper with a 40,000 character limit.
- Modify `numind-web-v3/src/views/sop/components/StepInput.vue`: SOP current-step counter.
- Modify `numind-web-v3/src/views/sop/components/ChatComposer.vue`: SOP chat counter.
- Modify `numind-web-v3/src/views/chatbot/ChatbotChat.vue`: chatbot input counter.
- Modify `numind-web-v3/src/components/sales/InputArea.vue`: SalesRAG chat input counter.

## Dependency Graph

1. Task 1 has no dependencies.
2. Task 2 depends on Task 1 only for schema names.
3. Task 3 depends on Tasks 1 and 2.
4. Task 4 depends on Tasks 1 and 2.
5. Task 5 depends on Tasks 1, 2, 3, and 4.
6. Task 6 depends on Task 5.
7. Task 7 depends on Tasks 2, 3, 5, and 6.
8. Task 8 depends on Task 7.
9. Task 9 depends on Task 7.
10. Task 10 depends on Task 7.
11. Task 11 depends on Tasks 3, 7, and 8.
12. Task 12 depends on Tasks 3, 7, 8, and 11.
13. Task 13 depends on Task 12.
14. Task 14 depends on Task 13.
15. Task 15 depends on all backend and frontend tasks.

The graph is acyclic and backend API/schema tasks precede frontend tasks.

---

### Task 1: Backend Schema And Seeds

**Files:**
- Create: `numind-server/migrations/20260425_172000_context_budget_compression.sql`
- Create: `numind-server/migrations/20260425_172000_context_budget_compression_rollback.sql`
- Modify: `numind-server/internal/pkg/model/credit.go`
- Create: `numind-server/internal/pkg/model/contextbudget.go`

**Description:** Add durable storage for token estimation profiles, budget policies, summary cache, budget events, and nullable budget metadata on `credit_reservation`.

**Acceptance Conditions:**
- Migration creates `token_estimation_profile`, `context_budget_policy`, `context_summary`, and `context_budget_event`.
- `credit_reservation.coefficient_id` is nullable and has `estimation_source`, `token_profile_id`, `estimated_prompt_tokens`, `estimated_completion_tokens`, `provider`, `model`, and `context_budget_event_id`.
- Seeds include all default policies from S2 spec with `safe_ratio=0.8500`, and `context_compression.charge_user=0`.
- Rollback drops new tables and new `credit_reservation` columns.

- [ ] **Step 1: Write migration smoke test**

Create `numind-server/internal/pkg/model/contextbudget_schema_test.go`:

```go
package model

import "testing"

func TestContextBudgetModelsHaveTableNames(t *testing.T) {
	cases := map[string]string{
		"token profile": TokenEstimationProfile{}.TableName(),
		"budget policy": ContextBudgetPolicy{}.TableName(),
		"summary":       ContextSummary{}.TableName(),
		"event":         ContextBudgetEvent{}.TableName(),
	}
	want := map[string]string{
		"token profile": "token_estimation_profile",
		"budget policy": "context_budget_policy",
		"summary":       "context_summary",
		"event":         "context_budget_event",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Fatalf("%s table name = %q, want %q", name, got, want[name])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd numind-server && go test ./internal/pkg/model -run TestContextBudgetModelsHaveTableNames -count=1`

Expected: FAIL because the model types are not defined.

- [ ] **Step 3: Add models and SQL**

Add model fields matching the S2 spec. Required GORM shape:

```go
type TokenEstimationProfile struct {
	ID                         uint64 `gorm:"primaryKey"`
	Provider                   string
	Model                      string
	ModelFamily                string
	ServiceType                string
	ProfileJSON                datatypes.JSON
	SafetyMultiplier           float64
	CalibrationMultiplier      float64
	CalibrationSampleCount     int
	CalibrationP50AbsError     *float64
	CalibrationP90AbsError     *float64
	CalibrationP99UnderRatio   *float64
	Version                    uint
	IsActive                   bool
	IsFallback                 bool
	ChangeReason              string
	UpdatedBy                 string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

func (TokenEstimationProfile) TableName() string { return "token_estimation_profile" }
```

Add equivalent `ContextBudgetPolicy`, `ContextSummary`, and `ContextBudgetEvent` models. Use `datatypes.JSON` for JSON columns and pointer numeric fields for nullable metrics.

In the forward SQL, create indexes named exactly as the S2 spec: `idx_tep_lookup`, `idx_tep_family`, `idx_tep_fallback`, `idx_cbp_operation_active`, `uk_summary_scope_hash`, `idx_summary_user_scope`, `idx_summary_status`, `idx_cbe_user_created`, `idx_cbe_operation_created`, `idx_cbe_status_created`, `idx_cr_token_profile`, and `idx_cr_budget_event`.

- [ ] **Step 4: Run model test and SQL parse checks**

Run: `cd numind-server && go test ./internal/pkg/model -run TestContextBudgetModelsHaveTableNames -count=1`

Expected: PASS.

Run: `cd numind-server && rg -n "CREATE TABLE IF NOT EXISTS (token_estimation_profile|context_budget_policy|context_summary|context_budget_event)|ALTER TABLE credit_reservation|safe_ratio|context_compression" migrations/20260425_172000_context_budget_compression.sql`

Expected: output includes all four table names, the `ALTER TABLE credit_reservation` block, `safe_ratio`, and the `context_compression` seed row.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add migrations/20260425_172000_context_budget_compression.sql migrations/20260425_172000_context_budget_compression_rollback.sql internal/pkg/model/credit.go internal/pkg/model/contextbudget.go internal/pkg/model/contextbudget_schema_test.go
git commit -m "feat: add context budget schema"
```

---

### Task 2: Generic Context Budget Package

**Files:**
- Create: `numind-server/internal/pkg/contextbudget/fragment.go`
- Create: `numind-server/internal/pkg/contextbudget/estimator.go`
- Create: `numind-server/internal/pkg/contextbudget/budget.go`
- Create: `numind-server/internal/pkg/contextbudget/planner.go`
- Create: `numind-server/internal/pkg/contextbudget/errors.go`
- Create: `numind-server/internal/pkg/contextbudget/estimator_test.go`
- Create: `numind-server/internal/pkg/contextbudget/budget_test.go`
- Create: `numind-server/internal/pkg/contextbudget/planner_test.go`

**Description:** Build the neutral package for fragment types, conservative token estimation, budget math, typed errors, and deterministic planning. This package must not import `aiservice`, `sop`, `chatbot`, `salesrag`, or `numind/biz`.

**Acceptance Conditions:**
- `go list -deps ./internal/pkg/contextbudget` output has no `internal/pkg/aiservice`, `internal/numind/biz`, `internal/numind/store`, `sop`, `chatbot`, or `salesrag`.
- Estimator classifies Chinese, English, code, JSON, markdown table, symbol-heavy, and mixed text.
- Budget validation enforces `context_window`, `max_output_tokens`, `reserved_output_tokens`, `fixed_overhead_tokens`, and `safe_ratio` constraints.
- Planner never drops critical/current/immutable fragments and does not branch on business metadata keys.

- [ ] **Step 1: Write failing tests**

Create estimator, budget, and planner tests with these names:

```go
func TestEstimatorWeightedCharClassConservative(t *testing.T) {}
func TestBudgetPolicyValidatesReservedOutputAndSafeRatio(t *testing.T) {}
func TestPlannerDoesNotDropCriticalOrCurrentFragments(t *testing.T) {}
func TestPlannerIgnoresBusinessMetadataForRanking(t *testing.T) {}
```

The metadata test must build two fragments with identical role, importance, order, and content, but different `Metadata` maps containing `sop_stage`, `node_id`, and `template_id`; the produced actions must be equal.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/pkg/contextbudget -count=1`

Expected: FAIL because the package is not present.

- [ ] **Step 3: Add neutral types and algorithms**

Public API:

```go
type ContextFragment struct {
	ID              string
	Role            FragmentRole
	Source          FragmentSource
	ContentType     ContentType
	Content         string
	Importance      int
	Order           int
	Compressibility Compressibility
	Critical        bool
	ParentID        string
	SourceReference string
	Metadata        map[string]string
	TokenEstimate   int
}

type ModelCapability struct {
	ContextWindow  int
	MaxOutputToken int
}

type BudgetPolicy struct {
	Operation            string
	ReservedOutputTokens int
	SafeRatio            float64
	FixedOverheadTokens  int
	SoftThresholdRatio   float64
	HardThresholdRatio   float64
	ChargeUser           bool
}

func EstimateFragments(fragments []ContextFragment, profile TokenProfile, fixedOverhead int) EstimateResult
func ComputeBudget(cap ModelCapability, policy BudgetPolicy) (Budget, error)
func PlanCompression(input PlanInput) (Plan, error)
```

Planner action constants:

```go
const (
	ActionReuseSummary ActionType = "reuse_summary"
	ActionReference    ActionType = "reference"
	ActionSummarize    ActionType = "summarize"
	ActionDrop         ActionType = "drop"
	ActionKeep         ActionType = "keep"
)
```

- [ ] **Step 4: Run tests and import-topology check**

Run: `cd numind-server && go test ./internal/pkg/contextbudget -count=1`

Expected: PASS.

Run: `cd numind-server && go list -deps ./internal/pkg/contextbudget | rg "internal/pkg/aiservice|internal/numind/biz|internal/numind/store|sop|chatbot|salesrag"`

Expected: no output and exit code 1.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/pkg/contextbudget
git commit -m "feat: add generic context budget package"
```

---

### Task 3: Context Budget Store

**Files:**
- Create: `numind-server/internal/numind/store/contextbudget.go`
- Create: `numind-server/internal/numind/store/contextbudget_test.go`

**Description:** Add persistence for active profile/policy lookup, append-only admin saves, summary cache, and budget events. Enforce active-version safety through transactions and row locks.

**Acceptance Conditions:**
- Active profile save locks matching key rows with `SELECT ... FOR UPDATE`, deactivates older rows, and inserts a new active version.
- Active policy save does the same for `operation`.
- Summary lookup requires `owner_user_id`, `scope_type`, `scope_id`, and `source_hash`.
- Tests cover concurrent active saves, tenant isolation, and event insert/update.

- [ ] **Step 1: Write failing store tests**

Required test names:

```go
func TestContextBudgetStore_SaveTokenProfileDeactivatesPriorActive(t *testing.T) {}
func TestContextBudgetStore_SavePolicyDeactivatesPriorActive(t *testing.T) {}
func TestContextBudgetStore_SummaryLookupRequiresOwnerScopeAndHash(t *testing.T) {}
func TestContextBudgetStore_CreateAndPatchEvent(t *testing.T) {}
```

The summary test must insert two summaries with the same `scope_type`, `scope_id`, and `source_hash`, but different `owner_user_id`; lookup for owner 10 must not return owner 20.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/store -run ContextBudgetStore -count=1`

Expected: FAIL because the store is missing.

- [ ] **Step 3: Implement store interface**

Interface:

```go
type ContextBudgetStore interface {
	GetActiveTokenProfile(ctx context.Context, key TokenProfileLookupKey) (*model.TokenEstimationProfile, error)
	SaveTokenProfileVersion(ctx context.Context, input SaveTokenProfileInput) (*model.TokenEstimationProfile, error)
	GetActivePolicy(ctx context.Context, operation string) (*model.ContextBudgetPolicy, error)
	SavePolicyVersion(ctx context.Context, input SavePolicyInput) (*model.ContextBudgetPolicy, error)
	FindReadySummary(ctx context.Context, ownerUserID uint64, scopeType, scopeID, sourceHash string) (*model.ContextSummary, error)
	UpsertSummary(ctx context.Context, summary *model.ContextSummary) error
	CreateEvent(ctx context.Context, event *model.ContextBudgetEvent) error
	PatchEvent(ctx context.Context, id uint64, patch EventPatch) error
}
```

Use `tx.Clauses(clause.Locking{Strength: "UPDATE"})` when reading existing active rows inside save transactions.

- [ ] **Step 4: Run store tests**

Run: `cd numind-server && go test ./internal/numind/store -run ContextBudgetStore -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/store/contextbudget.go internal/numind/store/contextbudget_test.go
git commit -m "feat: add context budget store"
```

---

### Task 4: Credit Budget Reservation API

**Files:**
- Modify: `numind-server/internal/numind/biz/credit/types.go`
- Modify: `numind-server/internal/numind/biz/credit/contracts.go`
- Modify: `numind-server/internal/numind/biz/credit/credit_service.go`
- Modify: `numind-server/internal/numind/biz/credit/estimate.go`
- Modify: `numind-server/internal/numind/biz/credit/credit_service_reserve_test.go`
- Modify: `numind-server/internal/numind/biz/credit/credit_service_reconcile_test.go`

**Description:** Add budget-aware precheck/reserve inputs while preserving current `ICreditService` behavior for legacy-tier users and existing R2 coefficient paths.

**Acceptance Conditions:**
- `CheckAndEstimateBudget` maps normalized operations to existing credit operations.
- `ReserveBudget` writes `estimation_source='context_budget'`, token profile id, prompt/completion estimates, provider/model, and event id.
- Legacy-tier users still bypass Reserve through the existing skip-deduction contract.
- Unknown charged operations fail closed with a typed error; uncharged internal operations may use `default_llm_chat`.

- [ ] **Step 1: Write failing credit tests**

Required test names:

```go
func TestCheckAndEstimateBudget_NormalizesSopNodeExecuteToSopRun(t *testing.T) {}
func TestReserveBudget_WritesContextBudgetMetadata(t *testing.T) {}
func TestCheckAndEstimateBudget_LegacyTierSkipsReserve(t *testing.T) {}
func TestCheckAndEstimateBudget_UnknownChargedOperationFailsClosed(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/biz/credit -run 'Budget|LegacyTierSkipsReserve|UnknownChargedOperation' -count=1`

Expected: FAIL because budget methods are not present.

- [ ] **Step 3: Add inputs and methods**

Add operation normalization:

```go
var budgetOperationMap = map[string]Operation{
	"sop_node_execute": OpSopRun,
	"sop_run":          OpSopRun,
	"sop_chat":         OpSopChat,
	"sop_chat_stream":  OpSopChat,
	"chatbot_chat":     OpChatbotChat,
	"chatbot.stream":   OpChatbotChat,
	"salesrag_chat":    OpSalesRAGChat,
	"salesrag_chat_generate": OpSalesRAGChat,
	"salesrag_strategy_select": OpSalesRAGChat,
	"salesrag_analyze_profile": OpSalesRAGChat,
	"salesrag_analyze_profile_text": OpSalesRAGChat,
	"salesrag_chat_style_text": OpSalesRAGChat,
}
```

Add inputs:

```go
type BudgetPrecheckInput struct {
	UserID                    uint
	Operation                 string
	EstimatedPromptTokens     int
	EstimatedCompletionTokens int
	Provider                  string
	Model                     string
	TokenProfileID            uint64
	ContextBudgetEventID      uint64
}

type BudgetReservationInput struct {
	BudgetPrecheckInput
	EstimatedCredits int64
	IdempotencyKey   string
	Metadata         map[string]string
}
```

Expose:

```go
CheckAndEstimateBudget(ctx context.Context, user *model.User, input BudgetPrecheckInput) (*PrecheckResult, error)
ReserveBudget(ctx context.Context, user *model.User, input BudgetReservationInput) (*Reservation, error)
```

- [ ] **Step 4: Run credit tests**

Run: `cd numind-server && go test ./internal/numind/biz/credit -run 'Budget|LegacyTierSkipsReserve|UnknownChargedOperation' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/biz/credit
git commit -m "feat: add budget-aware credit reservations"
```

---

### Task 5: Gateway Request Rendering And Middleware Order

**Files:**
- Modify: `numind-server/internal/pkg/aiservice/types.go`
- Create: `numind-server/internal/pkg/aiservice/context_renderer.go`
- Create: `numind-server/internal/pkg/aiservice/context_renderer_test.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/chain.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/chain_test.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/billing.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/billing_test.go`

**Description:** Add `ContextFragments` to chat requests, render them inside `aiservice`, and establish the required middleware order: `Tracing → Fallback → ContextBudgetCredits → BillingUsageRecord → Retry → Adapter`.

**Acceptance Conditions:**
- `contextbudget` still does not import `aiservice`.
- Rendering preserves fragment order and maps source roles to `ChatMessage` roles.
- Chain order test proves fallback wraps context budget and retry sits inside billing.
- Billing record uses the route parameter it receives, so fallback success records fallback provider/model/pricing.

- [ ] **Step 1: Write failing tests**

Required test names:

```go
func TestRenderContextFragmentsPreservesOrderAndRoles(t *testing.T) {}
func TestBuildDefaultMiddlewareOrder_ContextBudgetAfterFallbackBeforeBilling(t *testing.T) {}
func TestBillingUsesFallbackRouteProviderModelAndPricing(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/pkg/aiservice ./internal/pkg/aiservice/middleware -run 'RenderContext|MiddlewareOrder|BillingUsesFallbackRoute' -count=1`

Expected: FAIL because rendering and middleware order changes are absent.

- [ ] **Step 3: Add request field, renderer, and chain deps**

Add to `ChatRequest`:

```go
ContextFragments []contextbudget.ContextFragment `json:"context_fragments,omitempty"`
```

Renderer signature:

```go
func RenderContextFragments(fragments []contextbudget.ContextFragment) []ChatMessage
```

Middleware deps additions:

```go
type Deps struct {
	Langfuse           *langfuse.Client
	UsageStore        UsageStore
	Resolver          registry.Registry
	ContextBudget     ContextBudgetService
	CreditService     ContextBudgetCreditService
	Clock             Clock
	Logger            interface{ Warnw(string, ...interface{}); Errorw(string, ...interface{}) }
}
```

Build order:

```go
return Chain(
	Tracing(deps),
	Fallback(deps),
	ContextBudgetCredits(deps),
	Billing(deps),
	Retry(deps),
)
```

- [ ] **Step 4: Run route-aware tests**

Run: `cd numind-server && go test ./internal/pkg/aiservice ./internal/pkg/aiservice/middleware -run 'RenderContext|MiddlewareOrder|BillingUsesFallbackRoute' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/pkg/aiservice/types.go internal/pkg/aiservice/context_renderer.go internal/pkg/aiservice/context_renderer_test.go internal/pkg/aiservice/middleware
git commit -m "feat: route context budget middleware"
```

---

### Task 6: ContextBudgetCredits Middleware

**Files:**
- Create: `numind-server/internal/pkg/aiservice/middleware/context_budget.go`
- Create: `numind-server/internal/pkg/aiservice/middleware/context_budget_test.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/billing.go`

**Description:** Implement route-aware estimate, compression planning, Reserve/Reconcile, budget event persistence, and idempotent streaming finalization.

**Acceptance Conditions:**
- Empty `ContextFragments` skips compression and emits skipped events only for migrated operations.
- Over-budget requests run planner phases and return typed budget errors before provider calls when still oversized.
- Policy `ChargeUser=true` reserves before provider call and reconciles/refunds once.
- Policy `ChargeUser=false` records usage/event cost without user credit reservation.
- Streaming wrapper forwards chunks unchanged and finalizes on final usage, error final chunk, channel close without usage, or context cancellation.

- [ ] **Step 1: Write failing middleware tests**

Required test names:

```go
func TestContextBudgetCredits_UnderBudgetReservesAndRendersFragments(t *testing.T) {}
func TestContextBudgetCredits_OverBudgetFailsBeforeProviderWhenPlannerCannotFit(t *testing.T) {}
func TestContextBudgetCredits_CompressionOperationDoesNotReserveUserCredits(t *testing.T) {}
func TestContextBudgetCredits_StreamFinalUsageReconcilesOnce(t *testing.T) {}
func TestContextBudgetCredits_StreamCloseWithoutUsageFinalizesEstimated(t *testing.T) {}
func TestContextBudgetCredits_ContextCancelledRefundsWithoutUsage(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/pkg/aiservice/middleware -run ContextBudgetCredits -count=1`

Expected: FAIL because the middleware is not implemented.

- [ ] **Step 3: Implement middleware contracts**

Core service interfaces:

```go
type ContextBudgetService interface {
	Prepare(ctx context.Context, input PrepareInput) (*PrepareResult, error)
	Finalize(ctx context.Context, input FinalizeInput) error
}

type ContextBudgetCreditService interface {
	CheckAndEstimateBudget(ctx context.Context, user *model.User, input credit.BudgetPrecheckInput) (*credit.PrecheckResult, error)
	ReserveBudget(ctx context.Context, user *model.User, input credit.BudgetReservationInput) (*credit.Reservation, error)
	FinalizeReservation(ctx context.Context, reservationID uint64, actualCredits int64, reason string) error
	Refund(ctx context.Context, reservationID uint64, reason string) error
}
```

Stream wrapper contract:

```go
func wrapStreamForContextBudget(
	ctx context.Context,
	src <-chan aiservice.ChatChunk,
	finalizer func(FinalizeInput) error,
) <-chan aiservice.ChatChunk
```

Use `sync.Once` inside the wrapper so final chunks, channel close, and context cancellation cannot trigger double finalization.

- [ ] **Step 4: Run middleware tests**

Run: `cd numind-server && go test ./internal/pkg/aiservice/middleware -run ContextBudgetCredits -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/pkg/aiservice/middleware/context_budget.go internal/pkg/aiservice/middleware/context_budget_test.go internal/pkg/aiservice/middleware/billing.go
git commit -m "feat: add context budget gateway middleware"
```

---

### Task 7: Backend Context Budget Prepare And Finalize Biz

**Files:**
- Create: `numind-server/internal/numind/biz/contextbudget/biz.go`
- Create: `numind-server/internal/numind/biz/contextbudget/producers.go`
- Create: `numind-server/internal/numind/biz/contextbudget/biz_test.go`
- Modify: `numind-server/internal/numind/numind.go`

**Description:** Provide the middleware-facing `Prepare/Finalize` implementation plus reusable preview validation helpers. Background summary work is isolated in Task 8.

**Acceptance Conditions:**
- `Prepare` loads actual route capability, active policy, and active token profile; estimates fragments; applies planner; calls compression only when legal deterministic actions require summaries.
- Compression LLM calls use `context_compression`, `ChargeUser=false`, and do not recursively compress themselves.
- Summary upsert and lookup always include `owner_user_id`.
- `Finalize` patches event actual usage, calibration ratio, reserve amount, reconcile delta, and metadata.
- Preview helpers accept `service_id`, `operation`, `fixed_overhead_tokens`, `reserved_output_tokens`, and `safe_ratio`, then return model capability validation and safe budget math.

- [ ] **Step 1: Write failing biz tests**

Required test names:

```go
func TestPrepare_LoadsPolicyProfileAndProducesEvent(t *testing.T) {}
func TestPrepare_UsesSummaryCacheByOwnerScopeAndHash(t *testing.T) {}
func TestPrepare_CompressionCallUsesInternalOperationWithoutUserCharge(t *testing.T) {}
func TestFinalize_PatchesActualUsageAndCalibration(t *testing.T) {}
func TestPreview_UsesServiceIDAndPolicyFieldsForBudgetMath(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/biz/contextbudget -count=1`

Expected: FAIL because the biz package is missing.

- [ ] **Step 3: Implement prepare/finalize service**

Constructor:

```go
func New(store store.ContextBudgetStore, opts Options) *Biz

type Options struct {
	Compressor Compressor
	Clock      func() time.Time
	Logger     interface{ Warnw(string, ...interface{}); Errorw(string, ...interface{}) }
}
```

Compression guard:

```go
if input.Operation == "context_compression" {
	return b.prepareWithoutCompression(ctx, input)
}
```

Preview input:

```go
type PreviewInput struct {
	ServiceID             uint64
	Operation             string
	FixedOverheadTokens   int
	ReservedOutputTokens  int
	SafeRatio             float64
}
```

- [ ] **Step 4: Run biz tests**

Run: `cd numind-server && go test ./internal/numind/biz/contextbudget -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/biz/contextbudget internal/numind/numind.go
git commit -m "feat: add context budget service"
```

---

### Task 8: Background Summary Worker

**Files:**
- Create: `numind-server/internal/numind/biz/contextbudget/worker.go`
- Create: `numind-server/internal/numind/biz/contextbudget/worker_test.go`
- Modify: `numind-server/internal/numind/numind.go`

**Description:** Add the background no-interruption summary queue and worker. It may reuse Task 7 prepare helpers, but it owns job enqueueing, summary upsert, and failure recording.

**Acceptance Conditions:**
- Worker jobs include `user_id`, `owner_user_id`, `scope_type`, `scope_id`, `source_hash`, source fragment ids, and operation.
- Worker queries and upserts summaries with `owner_user_id`; it never looks up summaries by only `scope_type/scope_id`.
- Compression failure stores `context_summary.status='failed'` plus `error_message` and writes `context_budget_event.status='failed'`.
- Main request continues when async summary creation fails.

- [ ] **Step 1: Write failing worker tests**

Required test names:

```go
func TestSummaryWorker_UpsertsReadySummaryByOwnerScopeAndHash(t *testing.T) {}
func TestSummaryWorkerFailureStoresFailedSummaryWithoutBlockingCaller(t *testing.T) {}
func TestSummaryWorkerDoesNotLookupSummaryWithoutOwnerUserID(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/biz/contextbudget -run SummaryWorker -count=1`

Expected: FAIL because the worker is not implemented.

- [ ] **Step 3: Implement worker**

Worker queue entry:

```go
type SummaryJob struct {
	UserID            uint64
	OwnerUserID       uint64
	ScopeType         string
	ScopeID           string
	SourceHash        string
	SourceFragmentIDs []string
	Fragments         []contextbudget.ContextFragment
	Operation         string
}
```

Failure persistence:

```go
summary.Status = "failed"
summary.ErrorMessage = truncateError(err, 500)
_ = b.store.UpsertSummary(ctx, summary)
_ = b.store.PatchEvent(ctx, eventID, store.EventPatch{Status: "failed", ErrorCode: "compression_failed"})
```

- [ ] **Step 4: Run worker tests**

Run: `cd numind-server && go test ./internal/numind/biz/contextbudget -run SummaryWorker -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/biz/contextbudget/worker.go internal/numind/biz/contextbudget/worker_test.go internal/numind/numind.go
git commit -m "feat: add context summary worker"
```

---

### Task 9: SOP Gateway Producer Migration

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/executor.go`
- Modify: `numind-server/internal/numind/biz/sop/sop.go`
- Modify: `numind-server/internal/numind/biz/sop/sop_credits_integration_test.go`
- Create or modify: `numind-server/internal/numind/biz/sop/context_fragments_test.go`

**Description:** Migrate SOP node execution and SOP chat to emit generic fragments, use normalized operation metadata, and avoid double Reserve on the Gateway path.

**Acceptance Conditions:**
- Current node input/current chat message is critical and never trimmed by producer logic.
- Historical outputs, attachments, tool results, and durable run facts are fragments with generic roles and compressibility.
- Gateway request includes `ContextFragments`; rendered messages are left to Gateway middleware.
- Existing legacy-tier dispatch and SOP run count behavior remains intact.
- Gateway path does not do both old direct Reserve and new middleware Reserve.

- [ ] **Step 1: Write failing SOP producer tests**

Required test names:

```go
func TestSOPNodeExecutionBuildsCurrentInputAsCriticalFragment(t *testing.T) {}
func TestSOPGatewayPathSendsContextFragments(t *testing.T) {}
func TestSOPGatewayPathDoesNotDoubleReserveCredits(t *testing.T) {}
func TestSOPChatBuildsConversationFragments(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/biz/sop -run 'ContextFragments|DoesNotDoubleReserve|SOPChatBuildsConversation' -count=1`

Expected: FAIL because SOP does not yet send fragments.

- [ ] **Step 3: Implement producer helpers in SOP call sites**

Fragment mapping:

```go
contextbudget.ContextFragment{Role: contextbudget.RoleImmutable, Source: contextbudget.SourceSystem, Compressibility: contextbudget.CompressNone, Critical: true}
contextbudget.ContextFragment{Role: contextbudget.RoleRecent, Source: contextbudget.SourceUser, Compressibility: contextbudget.CompressNone, Critical: true}
contextbudget.ContextFragment{Role: contextbudget.RoleDurable, Source: contextbudget.SourceAssistant, Compressibility: contextbudget.CompressSummarize}
contextbudget.ContextFragment{Role: contextbudget.RoleEvidence, Source: contextbudget.SourceFile, Compressibility: contextbudget.CompressReference}
```

Before `aiservice.ChatStream`:

```go
req := aiservice.ChatRequest{
	ContextFragments: fragments,
	MaxTokens:        maxTokens,
	Temperature:      temperature,
	Thinking:         thinking,
}
ctx = billing.WithBillingMeta(ctx, run.UserID, "sop_node_execute", billing.Metadata(
	"run_id", billing.FormatUint(runID),
	"node_id", billing.FormatUint(nodeID),
	"trace_id", traceID,
))
```

- [ ] **Step 4: Run SOP tests**

Run: `cd numind-server && go test ./internal/numind/biz/sop -run 'ContextFragments|DoesNotDoubleReserve|SOPChatBuildsConversation|LegacyTier_BypassesReserve' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/biz/sop
git commit -m "feat: migrate sop calls to context fragments"
```

---

### Task 10: Chatbot And SalesRAG Producer Migration

**Files:**
- Modify: `numind-server/internal/numind/biz/chatbot/chatbot.go`
- Modify: `numind-server/internal/numind/biz/chatbot/stream.go`
- Modify: `numind-server/internal/numind/biz/chatbot/chatbot_test.go`
- Modify: `numind-server/internal/numind/biz/salesrag/salesrag.go`
- Modify: `numind-server/internal/numind/biz/salesrag/salesrag_credits_integration_test.go`
- Modify: `numind-server/internal/numind/biz/salesrag/adapter/strategy_router.go`
- Modify: `numind-server/internal/numind/biz/salesrag/adapter/strategy_router_test.go`

**Description:** Move chatbot and SalesRAG LLM calls to the same fragment contract without introducing SOP-specific strategy branches.

**Acceptance Conditions:**
- Chatbot current user message is critical/recent and conversation history is recent/durable according to recency.
- SalesRAG evidence chunks use `RoleEvidence` and `SourceKB`; references are allowed when source references exist.
- Operations normalize to `chatbot_chat`, `salesrag_chat`, `salesrag_strategy_select`, `salesrag_analyze_profile`, and `salesrag_chat_style_text`.
- No code in `contextbudget` imports or checks chatbot/SalesRAG metadata keys.

- [ ] **Step 1: Write failing producer tests**

Required test names:

```go
func TestChatbotStreamBuildsCurrentMessageAsCriticalFragment(t *testing.T) {}
func TestChatbotStreamUsesChatbotChatOperation(t *testing.T) {}
func TestSalesRAGBuildsEvidenceFragmentsWithoutSOPMetadata(t *testing.T) {}
func TestSalesRAGProfileAndChatStyleUseFragments(t *testing.T) {}
func TestSalesRAGStrategyRouterUsesFragments(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/biz/chatbot ./internal/numind/biz/salesrag/... -run 'Context|Fragment|ChatbotChat|SalesRAG' -count=1`

Expected: FAIL where migrated producers are absent.

- [ ] **Step 3: Implement producers**

Chatbot request shape:

```go
req := aiservice.ChatRequest{
	ContextFragments: fragments,
	MaxTokens:        maxTokens,
	Temperature:      temperature,
}
ctx = billing.WithBillingMeta(ctx, userID, "chatbot_chat", billing.Metadata(
	"chat_session_id", sessionID,
	"trace_id", traceID,
))
```

SalesRAG evidence shape:

```go
contextbudget.ContextFragment{
	Role:            contextbudget.RoleEvidence,
	Source:          contextbudget.SourceKB,
	ContentType:     contextbudget.ContentText,
	Compressibility: contextbudget.CompressReference,
	SourceReference: chunkID,
	Importance:      scoreToImportance(score),
}
```

- [ ] **Step 4: Run producer tests**

Run: `cd numind-server && go test ./internal/numind/biz/chatbot ./internal/numind/biz/salesrag/... -run 'Context|Fragment|ChatbotChat|SalesRAG' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/biz/chatbot internal/numind/biz/salesrag
git commit -m "feat: migrate chat producers to context fragments"
```

---

### Task 11: Admin Context Budget API

**Files:**
- Create: `numind-server/internal/numind/controller/v1/admin_contextbudget/context_budget.go`
- Create: `numind-server/internal/numind/controller/v1/admin_contextbudget/context_budget_test.go`
- Modify: `numind-server/internal/numind/admin_router.go`
- Modify: `numind-server/internal/numind/biz/aiservice_admin/biz.go`
- Modify: `numind-server/internal/numind/biz/aiservice_admin/biz_test.go`
- Modify: `numind-server/internal/numind/controller/v1/admin_ai/ai_service.go`

**Description:** Expose admin APIs for token profiles, policies, preview, and LLM service capability validation.

**Acceptance Conditions:**
- Endpoints:
  - `GET /v1/admin/context-budget/token-profiles`
  - `POST /v1/admin/context-budget/token-profiles`
  - `PUT /v1/admin/context-budget/token-profiles/:id`
  - `DELETE /v1/admin/context-budget/token-profiles/:id`
  - `GET /v1/admin/context-budget/token-profiles/history?provider=...&model=...&service_type=...`
  - `GET /v1/admin/context-budget/policies`
  - `PUT /v1/admin/context-budget/policies/:operation`
  - `POST /v1/admin/context-budget/preview`
  - `GET /v1/admin/context-budget/events`
- LLM service create/update rejects missing or invalid `context_window` and `max_output_tokens`.
- Controller layer binds/validates and delegates; business rules live in biz layer.

- [ ] **Step 1: Write failing controller and biz tests**

Required test names:

```go
func TestAdminContextBudgetRoutesAreRegistered(t *testing.T) {}
func TestAdminContextBudgetTokenProfileHistoryUsesLookupQuery(t *testing.T) {}
func TestAdminContextBudgetPreviewReturnsBudgetMath(t *testing.T) {}
func TestAdminContextBudgetEventsReturnsRecentMetadataOnly(t *testing.T) {}
func TestAIServiceLLMRequiresContextWindowAndMaxOutputTokens(t *testing.T) {}
func TestAIServiceRejectsReservedOutputGreaterThanMaxOutputViaPolicyPreview(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/numind/controller/v1/admin_contextbudget ./internal/numind/biz/aiservice_admin -run 'ContextBudget|ContextWindow|ReservedOutput' -count=1`

Expected: FAIL because routes and validation are absent.

- [ ] **Step 3: Implement API contracts**

Preview request:

```go
type PreviewRequest struct {
	ServiceID            uint64  `json:"service_id" binding:"required"`
	Operation            string  `json:"operation" binding:"required"`
	FixedOverheadTokens  int     `json:"fixed_overhead_tokens" binding:"required"`
	ReservedOutputTokens int     `json:"reserved_output_tokens" binding:"required"`
	SafeRatio            float64 `json:"safe_ratio" binding:"required"`
}
```

Preview response:

```go
type PreviewResponse struct {
	ContextWindow        int      `json:"context_window"`
	MaxOutputTokens      int      `json:"max_output_tokens"`
	ReservedOutputTokens int      `json:"reserved_output_tokens"`
	SafeInputBudget      int      `json:"safe_input_budget"`
	Valid                bool     `json:"valid"`
	Warnings             []string `json:"warnings"`
}
```

Recent events response items must include ids, counts, status, provider, model, operation, reserve/reconcile numbers, and timestamps; they must not include full prompt text, rendered messages, or fragment content.

AI service LLM validation:

```go
func validateLLMCapability(cap profile.ServiceCapability) error {
	if cap.ContextWindow <= 0 {
		return errors.New("context_window is required for llm services")
	}
	if cap.MaxOutputTokens <= 0 || cap.MaxOutputTokens >= cap.ContextWindow {
		return errors.New("max_output_tokens must be positive and less than context_window")
	}
	return nil
}
```

- [ ] **Step 4: Run admin API tests**

Run: `cd numind-server && go test ./internal/numind/controller/v1/admin_contextbudget ./internal/numind/biz/aiservice_admin -run 'ContextBudget|ContextWindow|ReservedOutput' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/controller/v1/admin_contextbudget internal/numind/admin_router.go internal/numind/biz/aiservice_admin internal/numind/controller/v1/admin_ai/ai_service.go
git commit -m "feat: add admin context budget api"
```

---

### Task 12: Observability And Evaluation Fixtures

**Files:**
- Modify: `numind-server/internal/pkg/aiservice/middleware/tracing.go`
- Modify: `numind-server/internal/pkg/aiservice/middleware/billing.go`
- Create: `numind-server/internal/pkg/contextbudget/evaluation_test.go`
- Create: `numind-server/internal/pkg/contextbudget/testdata/evaluation_cases.json`
- Create: `numind-server/docs/context-budget-observability.md`

**Description:** Add event metadata to billing and Langfuse traces, protect prompt content from logs, and add deterministic estimation evaluation data for S5.

**Acceptance Conditions:**
- `usage_record.metadata` includes `context_budget_event_id`, `token_profile_id`, `budget_policy_id`, `estimated_prompt_tokens`, `estimated_completion_tokens`, `safe_input_budget`, and `compression_actions`.
- Langfuse metadata includes the same scalar IDs/counts, not full prompt or fragment content.
- Evaluation dataset covers Chinese copy, English prose, Go/TypeScript code, markdown table, JSON, symbol-heavy text, mixed text, attachment reference, and RAG evidence chunks.
- Evaluation test reports P50/P90/P99-under metrics and fails when deterministic thresholds regress.

- [ ] **Step 1: Write failing observability tests**

Required test names:

```go
func TestBillingMetadataIncludesContextBudgetIDsWithoutPromptContent(t *testing.T) {}
func TestTracingMetadataIncludesContextBudgetSummaryWithoutPromptContent(t *testing.T) {}
func TestTokenEstimatorEvaluationDatasetMeetsThresholds(t *testing.T) {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-server && go test ./internal/pkg/aiservice/middleware ./internal/pkg/contextbudget -run 'ContextBudgetIDs|TracingMetadata|EvaluationDataset' -count=1`

Expected: FAIL because metadata and fixtures are absent.

- [ ] **Step 3: Implement metadata and fixtures**

Metadata keys:

```go
map[string]any{
	"context_budget_event_id": estimated.EventID,
	"token_profile_id":       estimated.TokenProfileID,
	"budget_policy_id":       estimated.BudgetPolicyID,
	"estimated_prompt_tokens": estimated.PromptTokens,
	"safe_input_budget":       estimated.SafeInputBudget,
	"compression_actions":     estimated.ActionSummary,
}
```

Never include `fragment.Content`, `ChatMessage.Content.Text`, or rendered prompt text in metadata, logs, or docs examples.

- [ ] **Step 4: Run observability tests**

Run: `cd numind-server && go test ./internal/pkg/aiservice/middleware ./internal/pkg/contextbudget -run 'ContextBudgetIDs|TracingMetadata|EvaluationDataset' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/pkg/aiservice/middleware internal/pkg/contextbudget/evaluation_test.go internal/pkg/contextbudget/testdata/evaluation_cases.json docs/context-budget-observability.md
git commit -m "feat: add context budget observability"
```

---

### Task 13: Admin Web Configuration UI

**Files:**
- Modify: `numind-admin-web/src/types/ai.ts`
- Modify: `numind-admin-web/src/api/ai.ts`
- Create: `numind-admin-web/src/views/AIService/ContextBudget.vue`
- Modify: `numind-admin-web/src/views/AIService/ServiceEdit.vue`
- Modify: `numind-admin-web/src/router/index.ts`

**Description:** Add admin UI to configure token profiles, operation policies, recent events, calibration metrics, preview validation, and LLM service context capability fields.

**Acceptance Conditions:**
- Admin can list, create/update/deactivate token profiles.
- Token profile rows show calibration sample count, P50/P90 absolute error, and P99 under-estimation ratio.
- Admin can edit budget policies for `sop_run`, `sop_chat`, `chatbot_chat`, `salesrag_chat`, `context_compression`, and `default_llm_chat`.
- Recent Events tab lists `context_budget_event` metadata without prompt content.
- Preview sends `service_id`, `operation`, `fixed_overhead_tokens`, `reserved_output_tokens`, and `safe_ratio`; it shows `context_window`, `max_output_tokens`, safe input budget, valid flag, and warnings.
- LLM service edit screen validates `context_window` and `max_output_tokens` before save.
- UI remains consistent with existing admin design and does not expose token math to user web.

- [ ] **Step 1: Write failing TypeScript and component tests**

Add tests under the existing admin web test location or create `numind-admin-web/src/views/AIService/__tests__/ContextBudget.spec.ts` if the repo already uses Vitest. Required test names:

```ts
it("validates llm context fields before saving a service", () => {})
it("renders policy rows with safe ratio and reserved output tokens", () => {})
it("sends preview requests with service id operation and policy fields", () => {})
it("renders recent event rows without prompt content", () => {})
it("renders token profile calibration metrics", () => {})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-admin-web && npm run test:unit -- ContextBudget`

Expected: FAIL after adding tests for new types/components before implementation.

- [ ] **Step 3: Implement API types, route, page, and service edit fields**

API client signatures:

```ts
export function listContextBudgetPoliciesApi(): Promise<ContextBudgetPolicy[]>
export function updateContextBudgetPolicyApi(operation: string, payload: UpdateContextBudgetPolicyRequest): Promise<ContextBudgetPolicy>
export function listTokenProfilesApi(params: TokenProfileQuery): Promise<PagedResult<TokenEstimationProfile>>
export function saveTokenProfileApi(payload: SaveTokenProfileRequest): Promise<TokenEstimationProfile>
export function listContextBudgetEventsApi(params: ContextBudgetEventQuery): Promise<PagedResult<ContextBudgetEvent>>
export function previewContextBudgetApi(payload: PreviewContextBudgetRequest): Promise<PreviewContextBudgetResponse>
```

Local validation:

```ts
function validateLLMCapability(capability: Record<string, unknown>): string | null {
  const contextWindow = Number(capability.context_window)
  const maxOutputTokens = Number(capability.max_output_tokens)
  if (!Number.isFinite(contextWindow) || contextWindow <= 0) return "context_window 必须大于 0"
  if (!Number.isFinite(maxOutputTokens) || maxOutputTokens <= 0) return "max_output_tokens 必须大于 0"
  if (maxOutputTokens >= contextWindow) return "max_output_tokens 必须小于 context_window"
  return null
}
```

- [ ] **Step 4: Run admin web checks**

Run: `cd numind-admin-web && npm run lint && npm run type-check`

Expected: PASS.

Run: `cd numind-admin-web && npm run test:unit -- ContextBudget`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-admin-web
git add src/types/ai.ts src/api/ai.ts src/views/AIService/ContextBudget.vue src/views/AIService/ServiceEdit.vue src/router/index.ts
git commit -m "feat: add context budget admin UI"
```

---

### Task 14: User Web Input Counters

**Files:**
- Create: `numind-web-v3/src/utils/inputBudget.ts`
- Create: `numind-web-v3/src/utils/inputBudget.spec.ts`
- Modify: `numind-web-v3/src/views/sop/components/StepInput.vue`
- Modify: `numind-web-v3/src/views/sop/components/ChatComposer.vue`
- Modify: `numind-web-v3/src/views/chatbot/ChatbotChat.vue`
- Modify: `numind-web-v3/src/components/sales/InputArea.vue`

**Description:** Show simple `x / 40000` character counters for user-entered input areas. Do not expose token estimates or model budget internals in user-facing UI.

**Acceptance Conditions:**
- SOP current-step input shows live count.
- SOP chat and chatbot input show live count.
- SalesRAG input shows live count when a SalesRAG input component exists.
- Counter turns warning style at 85% and error style above 100%.
- Above 40,000 characters shows an inline error next to the counter; submit is not blocked in v1 because backend context budget remains authoritative.
- Text fits on mobile and desktop without overlapping buttons or input content.

- [ ] **Step 1: Write failing utility and component tests**

Utility tests:

```ts
import { describe, expect, it } from "vitest"
import { getInputBudgetState } from "./inputBudget"

describe("getInputBudgetState", () => {
  it("returns normal warning and error states around the 40000 limit", () => {
    expect(getInputBudgetState("a".repeat(10)).state).toBe("normal")
    expect(getInputBudgetState("a".repeat(34000)).state).toBe("warning")
    expect(getInputBudgetState("a".repeat(40001)).state).toBe("error")
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd numind-web-v3 && npm run test:unit -- inputBudget`

Expected: FAIL because `inputBudget.ts` is missing after tests reference it.

- [ ] **Step 3: Implement counter helper and UI**

Helper:

```ts
export const INPUT_CHARACTER_LIMIT = 40000
export const INPUT_WARNING_RATIO = 0.85

export type InputBudgetState = "normal" | "warning" | "error"

export function getInputBudgetState(value: string) {
  const count = Array.from(value ?? "").length
  const ratio = count / INPUT_CHARACTER_LIMIT
  const state: InputBudgetState =
    count > INPUT_CHARACTER_LIMIT ? "error" : ratio >= INPUT_WARNING_RATIO ? "warning" : "normal"
  return { count, limit: INPUT_CHARACTER_LIMIT, label: `${count} / ${INPUT_CHARACTER_LIMIT}`, state }
}
```

Each component should render a compact counter near the existing send/run action area. When `state === "error"`, show inline copy `输入超过 40000 字，系统可能需要压缩上下文` next to the counter and keep the existing send/run behavior unchanged.

- [ ] **Step 4: Run user web checks**

Run: `cd numind-web-v3 && npm run lint && npm run type-check`

Expected: PASS. Existing warnings unrelated to this feature may remain, but new errors must be zero.

Run: `cd numind-web-v3 && npm run test:unit -- inputBudget`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd numind-web-v3
git add src/utils/inputBudget.ts src/utils/inputBudget.spec.ts src/views/sop/components/StepInput.vue src/views/sop/components/ChatComposer.vue src/views/chatbot/ChatbotChat.vue src/components/sales/InputArea.vue
git commit -m "feat: show input budget counters"
```

---

### Task 15: End-To-End Verification And NDF Closeout Prep

**Files:**
- Modify: `numind-server/build-manifest.yaml`
- Create: `numind-server/docs/superpowers/verification/context-budget-compression-s5.md`

**Description:** Run the backend and frontend verification suite, capture S5 evidence, and prepare NDF manifest state for S5/S6 without mixing unrelated worktree changes.

**S5 验证策略（NDF Rule 10）：**

- **后端 TDD（已覆盖）**：Tasks 1–12 每个任务的 `_test.go` 文件覆盖 estimator/budget/planner/store active-version/middleware order/streaming finalization/Reserve/Reconcile/legacy-tier dispatch/preview/事件 metadata 等核心业务逻辑；S5 通过 `go test ./...` 一次性回归。
- **Playwright E2E（管理端高风险路径）**：管理端 token profile CRUD、budget policy 编辑、preview 流程必须有 Playwright spec，因为这些页面写入会直接改变线上 LLM 调用的预扣 credits 行为，属于高风险业务路径，需要持久化回归保护。Spec 文件路径：`numind-admin-web/e2e/context-budget.spec.ts`（如管理端目前无 e2e 目录，则 S5 优先添加最小骨架）。
- **gstack `/qa`（用户端低风险 UX）**：用户端 SOP/chatbot/SalesRAG `x / 40000` 计数器为纯前端 UX 反馈，无业务侧效，gstack `/qa` 截图验证足够。**诚实声明**：选择 gstack `/qa` 意味着该路径未来修改时无自动回归保护，需手动重新运行 /qa；如果未来计数器逻辑被业务侧用于硬阻断 submit，需要升级为 Playwright spec。
- **Langfuse 本地校验**：S5 至少跑一次本地 SOP 调用，从 Langfuse UI 确认 generation metadata 包含 `context_budget_event_id`、`safe_input_budget`、`compression_actions` 等字段且不含 prompt 原文。

**关键用户路径列表（S5 必须按序验证）：**

1. Admin → AIService → ServiceEdit：编辑 LLM 服务，改 `context_window`/`max_output_tokens` 为非法值，保存被前端+后端拒绝（Playwright）。
2. Admin → AIService → ContextBudget：创建一个新的 `token_estimation_profile` 行，提交后版本号递增、旧版被设为 inactive（Playwright）。
3. Admin → AIService → ContextBudget → Policies：编辑 `sop_run` 的 `reserved_output_tokens` 或 `safe_ratio`，提交后 preview 接口返回的 `safe_input_budget` 同步变化（Playwright）。
4. Admin → AIService → ContextBudget → Recent Events：超预算请求触发后，事件列表展示对应行，含 `compression_actions` 但不含 prompt content（Playwright）。
5. 后端：SOP 节点执行触发 Gateway 调用 → DB `context_budget_event` 行写入 + `credit_reservation.estimation_source='context_budget'`（后端 integration test）。
6. 后端：构造 fragments 总长 > safe_input_budget 的 SOP 请求 → 返回 `ErrContextTooLarge` 且**未**调用 provider（后端 integration test）。
7. 后端：legacy-tier 用户 SOP 调用 → `Reserve` 被 skip、`monthly_sop_runs` 仍递增（后端 integration test，Task 4 已覆盖）。
8. 用户端：SOP/chatbot/SalesRAG 输入框分别在 ≤ 33999、34000、40001 字符处计数器状态切换 normal/warning/error（gstack `/qa`）。

**Acceptance Conditions:**
- Backend lint and tests pass or have documented pre-existing failures with exact command output summary.
- Admin web lint/type-check pass.
- User web lint/type-check pass.
- Playwright spec for the four admin paths above is added under `numind-admin-web/e2e/` and runs green at least once locally.
- gstack `/qa` screenshot evidence for user input counter (3 character thresholds) is attached to the S5 verification doc.
- Local S5 verification doc records migration apply/rollback check, admin API smoke test, user counter smoke test, Langfuse metadata check, and context budget event check.
- Manifest progress counts completed/reviewed tasks accurately.

- [ ] **Step 1: Run full backend verification**

Run:

```bash
cd numind-server
task lint
go test ./...
```

Expected: `task lint` exits 0 and `go test ./...` exits 0. If a package already fails on `develop` before this feature branch, record the package, test name, and failure line in the S5 doc.

- [ ] **Step 2: Run admin web verification**

Run:

```bash
cd numind-admin-web
npm run lint
npm run type-check
npm run test:unit -- ContextBudget
```

Expected: all commands exit 0.

- [ ] **Step 3: Run user web verification**

Run:

```bash
cd numind-web-v3
npm run lint
npm run type-check
npm run test:unit -- inputBudget
```

Expected: all commands exit 0.

- [ ] **Step 4: Capture S5 verification doc**

Create `numind-server/docs/superpowers/verification/context-budget-compression-s5.md` with:

```markdown
# Context Budget Compression S5 Verification

## Commands

- `cd numind-server && task lint`: PASS
- `cd numind-server && go test ./...`: PASS
- `cd numind-admin-web && npm run lint && npm run type-check && npm run test:unit -- ContextBudget`: PASS
- `cd numind-web-v3 && npm run lint && npm run type-check && npm run test:unit -- inputBudget`: PASS

## Runtime Smoke

- Migration apply（dev SSH 执行 02-apply.sql）: PASS
- Migration rollback（dev SSH 执行 04-rollback.sql 然后再 apply 一次）: PASS
- Admin preview endpoint（Playwright spec admin path #3）: PASS
- SOP Gateway under-budget call（构造 fragments 总长 < safe_input_budget 的 SOP 节点请求 → DB 出现 `context_budget_event.status='ok'` 行）: PASS
- SOP Gateway over-budget compression path（构造 fragments 总长 > safe_input_budget 触发 planner，断言事件 `compression_actions` 非空、`status='compressed'`，并验证 provider 被调用一次而非多次）: PASS
- Stream finalization with final usage（Task 6 集成测试 `TestContextBudgetCredits_StreamFinalUsageReconcilesOnce` 覆盖；S5 通过 `go test ./...` 回归）: PASS
- Stream close without usage（Task 6 集成测试 `TestContextBudgetCredits_StreamCloseWithoutUsageFinalizesEstimated` 覆盖；S5 通过 `go test ./...` 回归）: PASS
- User input counter（gstack `/qa` 截图：分别输入 10/34000/40001 字符，状态 normal/warning/error）: PASS
- Langfuse metadata excludes prompt content（本地跑 SOP 节点后从 Langfuse UI 抽查 generation metadata，确认包含 budget IDs，**不包含** fragment content 或 prompt 原文）: PASS
- `context_budget_event` row created and patched（同 SOP under-budget 步骤；用 `SELECT * FROM context_budget_event ORDER BY id DESC LIMIT 1` 确认 reserve/reconcile delta、actual usage 已 patch）: PASS
```

- [ ] **Step 5: Update manifest and commit verification docs**

```bash
cd numind-server
git add build-manifest.yaml docs/superpowers/verification/context-budget-compression-s5.md
git commit -m "docs: record context budget verification"
```

---

## Spec Coverage Map

- Generic `ContextFragment` strategy, no SOP stage/node/template coupling: Tasks 2, 5, 9, 10.
- Admin web configuration: Tasks 11 and 13.
- Token estimation and pre-call credit Reserve/Reconcile: Tasks 2, 4, 6, 7.
- `context_window`, `max_output_tokens`, `reserved_output_tokens`, and `safe_ratio=0.85`: Tasks 1, 2, 11, 13.
- Intelligent compression: Tasks 2, 6, 7.
- Background no-interruption compression: Task 8.
- Failure fallback behavior: Tasks 6, 7, 8, 15.
- Observability through events, usage metadata, and Langfuse metadata: Task 12.
- Streaming finalization: Tasks 6 (wrapper + sync.Once finalize) and 7 (event patch in Finalize biz method).
- Route-aware fallback billing: Tasks 5 and 6.
- User input `x / 40000` counters: Task 14.

## Execution Notes

- Keep backend commits in `numind-server`, admin commits in `numind-admin-web`, and user web commits in `numind-web-v3`; these are separate Git repositories.
- Do not include unrelated untracked files in any commit.
- Run `git status --short` before every commit and stage only files listed in the task.
- Start S4 only after S3 gate approval.
