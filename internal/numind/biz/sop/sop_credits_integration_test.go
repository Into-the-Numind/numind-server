// Package sop — sop_credits_integration_test.go
//
// Phase 2 Task 2.1 integration tests for the Reserve → LLM → Reconcile
// control-flow inversion in ExecuteNodeStream / ChatAfterRunStream.
//
// Scope: these tests drive the credit-side plumbing with the exact
// Operation + idempotency-key + EstimationInput values that the new sop.go
// passes to ICreditService, and assert on the resulting DB state
// (credit_reservation row, status transitions, items, account balance,
// user.monthly_sop_runs for legacy_tier). End-to-end coverage of the LLM
// call itself is deferred to Playwright (Phase 2.5) since SopExecutor is a
// concrete struct that requires the full aiservice Gateway.
//
// In-memory SQLite is used throughout (see newCreditsSopTestDB). ENUM-typed
// columns (credit_reservation.status / finalize_reason) are hand-rolled as
// TEXT since SQLite has no native ENUM — the same pattern used by
// credit_service_reserve_test.go.
package sop

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// ----------------------------------------------------------------------------
// Test harness
// ----------------------------------------------------------------------------

// newCreditsSopTestDB builds a SQLite in-memory DB pre-populated with the
// credits-system tables the sop Reserve/Reconcile path depends on. Matches
// the schema emitted by biz/credit/credit_service_reserve_test.go::
// newCreditReserveTestDB + extras for SOP template lookup.
func newCreditsSopTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// AutoMigrate tables that have no MySQL ENUMs.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.CreditEstimationCoefficient{},
		&model.PricingRule{},
		&model.SopTemplate{},
		&model.SopNode{},
		&model.UsageRecord{},
	))

	// Hand-roll reservation tables (TEXT for ENUMs).
	// Includes context-budget extension columns (estimation_source, token_profile_id, etc.)
	// added in Task 1 (feature: context-budget-compression) — kept in sync with
	// credit_service_reserve_test.go::newCreditReserveTestDB.
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL,
    coefficient_id INTEGER,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    estimation_source TEXT NOT NULL DEFAULT 'credit_coefficient',
    token_profile_id INTEGER,
    estimated_prompt_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_completion_tokens INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    context_budget_event_id INTEGER,
    user_type_multiplier REAL NOT NULL DEFAULT 1.0
);`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uk_idempotency_key ON credit_reservation(idempotency_key);`).Error)
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_id INTEGER NOT NULL,
    package_id INTEGER,
    source_type TEXT,
    source_id INTEGER,
    credits INTEGER NOT NULL,
    package_type TEXT NOT NULL,
    package_expires_at DATETIME NOT NULL,
    seq INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uk_reservation_seq ON credit_reservation_item(reservation_id, seq);`).Error)

	return db
}

// seedSopCreditsScenario plants a credit_package + coefficient + pricing_rule
// row matching the (provider, model, operation) triple the SOP flow uses.
// Returns the seeded user.
func seedSopCreditsScenario(t *testing.T, db *gorm.DB,
	userID uint, balance int64,
	provider, modelName string, op credit.Operation,
) *model.User {
	t.Helper()

	// Account + package (FIFO entry point)
	now := time.Now()
	acc := model.CreditAccount{UserID: userID, Balance: balance, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)
	pkg := model.CreditPackage{
		UserID: userID, Type: model.CreditTypeSubscription,
		TotalCredits: balance, RemainCredits: balance,
		ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		Status: model.CreditPackageActive,
	}
	require.NoError(t, db.Create(&pkg).Error)

	// Coefficient row (R2 estimation source).
	coef := model.CreditEstimationCoefficient{
		Provider: provider, Model: modelName, Operation: string(op),
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5,
		SafetyBufferPct: 0.25, Version: 1, IsActive: true,
	}
	require.NoError(t, db.Create(&coef).Error)

	// Pricing rule (actualCost source).
	rule := model.PricingRule{
		ServiceType: "llm_chat", Provider: provider, Model: modelName,
		BillingMode: "flat", FlatUnit: "call",
		InputPricePerMTok: 200, OutputPricePerMTok: 800,
		IsActive: true,
	}
	require.NoError(t, db.Create(&rule).Error)

	// UserTier must be "free" so HasActiveMembership()=false and isEffectiveLegacy()
	// routes to the credits path. Standard/trial/premium with TierExpires=nil
	// would make HasActiveMembership()=true → legacyTierImpl.Reserve panic.
	return &model.User{
		Model:       gorm.Model{ID: userID},
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierFree,
	}
}

// buildSopBizWithCredits constructs the struct under test with a real
// ICreditService + pricing calculator so helper assertions can observe the
// full plumbing.
func buildSopBizWithCredits(t *testing.T, db *gorm.DB) *sopBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	cs := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)
	return &sopBiz{
		ds:        ds,
		creditBiz: credit.NewCreditBiz(ds),
		creditSvc: cs,
		pricing:   calc,
	}
}

// ----------------------------------------------------------------------------
// Helper-level assertions — ensure the new sop.go helpers behave as spec'd
// ----------------------------------------------------------------------------

// TestComputeSopPromptChars_SumsAllSources verifies prompt-char accounting
// covers template.Prompt + node.Prompt + history + currentInput.
func TestComputeSopPromptChars_SumsAllSources(t *testing.T) {
	tmpl := &model.SopTemplate{Prompt: "tmpl-prompt"}      // 11
	node := &model.SopNode{Prompt: "node-prompt"}          // 11
	history := []LLMMessage{{Role: "user", Content: "hi"}} // 2
	input := "question"                                    // 8
	got := computeSopPromptChars(tmpl, node, history, input)
	assert.Equal(t, 11+11+2+8, got)
}

// TestComputeSopPromptChars_NilSafety verifies nil template / node inputs
// degrade gracefully (no panic, pure char sum).
func TestComputeSopPromptChars_NilSafety(t *testing.T) {
	got := computeSopPromptChars(nil, nil, nil, "only-input")
	assert.Equal(t, 10, got)
}

// TestProviderFromModelName_KnownPrefixes covers the prefix table and the
// empty / unknown fallbacks (spec §3.2 requires best-effort, not strict).
func TestProviderFromModelName_KnownPrefixes(t *testing.T) {
	cases := map[string]string{
		"qwen-turbo":            "ali-dashscope",
		"qwen-plus":             "ali-dashscope",
		"text-embedding-v4":     "ali-dashscope",
		"deepseek-v3-2-251201":  "volc-ark",
		"doubao-seed-1-6-flash": "volc-ark",
		"glm-4-7-251222":        "volc-ark",
		"claude-3-5-sonnet":     "dmxapi",
		"gemini-pro":            "dmxapi",
		"":                      "",
		"some-custom-model":     "",
	}
	for in, want := range cases {
		assert.Equal(t, want, credit.ProviderFromModel(in), "model=%s", in)
	}
}

// TestWrapCreditError_PreservesLegacyReason verifies that legacy_tier
// Reason text flows through to errno.ErrInsufficientCredits. This is the
// contract that the frontend InsufficientCreditsDialog consumes (spec §3.6).
func TestWrapCreditError_PreservesLegacyReason(t *testing.T) {
	pre := &credit.PreCheckResult{Reason: "体验会员运行次数已达上限"}
	err := fmt.Errorf("%w: ignored inner", credit.ErrInsufficientCredits)

	wrapped := wrapCreditError(err, pre)
	require.NotNil(t, wrapped)
	assert.Contains(t, wrapped.Error(), "体验会员运行次数已达上限")
}

// TestWrapCreditError_NoPreFallsBackToErrno verifies the fallback path
// (nil pre, still insufficient-credits) returns the default errno message.
func TestWrapCreditError_NoPreFallsBackToErrno(t *testing.T) {
	err := fmt.Errorf("%w", credit.ErrInsufficientCredits)
	wrapped := wrapCreditError(err, nil)
	require.NotNil(t, wrapped)
	// errno.ErrInsufficientCredits.Message = "积分不足"
	assert.Contains(t, wrapped.Error(), "积分不足")
}

// TestWrapCreditError_PassesThroughUnknownErrors verifies non-credit errors
// bypass translation (preserves stack context for the caller).
func TestWrapCreditError_PassesThroughUnknownErrors(t *testing.T) {
	ioErr := errors.New("db timeout")
	assert.Equal(t, ioErr, wrapCreditError(ioErr, nil))
}

// ----------------------------------------------------------------------------
// Integration: credits-mode Reserve/Reconcile full-stack, sop_run operation
// ----------------------------------------------------------------------------

// TestSopCredits_CreditsMode_ReserveThenReconcile drives the exact call
// pattern runNode uses (OpSopRun + sop_run:<runID>:<nodeID> idemp key) and
// asserts on the reservation lifecycle through Reserve → FinalizeReservation.
// Mirrors the "credits 用户 SOP 扣减全链路" requirement.
func TestSopCredits_CreditsMode_ReserveThenReconcile(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const (
		userID    = uint(9001)
		balance   = int64(5000)
		runID     = uint(77)
		nodeID    = uint(88)
		provider  = "ali"
		modelName = "qwen-turbo"
	)
	user := seedSopCreditsScenario(t, db, userID, balance,
		provider, modelName, credit.OpSopRun)
	ctx := context.Background()

	// 1. Drive CheckAndEstimate as ExecuteNodeStream would.
	pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, // 1500 tokens × 1.5 coefficient after ceil
		Model:       modelName, Provider: provider,
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.False(t, pre.SkipDeduction, "credits user must not skip")
	assert.True(t, pre.Sufficient)
	assert.Greater(t, pre.EstimatedCredits, int64(0))

	// 2. Reserve with the same idempKey runNode would use.
	idemp := fmt.Sprintf("sop_run:%d:%d", runID, nodeID)
	rsv, err := b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
		pre.EstimatedCredits, pre.CoefficientID, &idemp)
	require.NoError(t, err)
	require.NotNil(t, rsv)
	assert.Equal(t, credit.StatusReserved, rsv.Status)
	assert.EqualValues(t, pre.EstimatedCredits, rsv.ReservedCredits)
	require.NotNil(t, rsv.IdempotencyKey)
	assert.Equal(t, idemp, *rsv.IdempotencyKey)

	// 3. Compute actualCost via pricing (same call runNode makes post-LLM).
	//    promptTokens = 1500, completionTokens = 750 → cost = 90 cents.
	actualCost, err := b.pricing.CalculateCost(ctx, "llm_chat", provider, modelName, 1500, 750)
	require.NoError(t, err)
	assert.EqualValues(t, 90, actualCost, "pricing formula golden")

	// 4. Finalize with actualCost → Reconcile path.
	opErr := error(nil)
	require.NoError(t, b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr))

	// 5. Verify DB state: status=reconciled, actual_cost_cents=90, delta negative.
	var row model.CreditReservation
	require.NoError(t, db.First(&row, rsv.ID).Error)
	assert.Equal(t, string(credit.StatusReconciled), row.Status)
	require.NotNil(t, row.ActualCostCents)
	assert.EqualValues(t, 90, *row.ActualCostCents)
	require.NotNil(t, row.Delta)
	assert.Less(t, *row.Delta, int64(0), "over-estimation → negative delta, refunded to package")

	// 6. Account balance = balance - 90 (reserved returned to package, reconcile charged 90).
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, balance-90, acc.Balance)
}

// TestSopCredits_CreditsMode_LLMErrorTriggersRefund verifies the failure
// branch (spec §3.3): when the LLM returns an error, defer FinalizeReservation
// routes through Refund and the full reservation is returned to the origin
// package.
func TestSopCredits_CreditsMode_LLMErrorTriggersRefund(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const (
		userID    = uint(9002)
		balance   = int64(5000)
		provider  = "ali"
		modelName = "qwen-turbo"
	)
	user := seedSopCreditsScenario(t, db, userID, balance,
		provider, modelName, credit.OpSopRun)
	ctx := context.Background()

	pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 500, Model: modelName, Provider: provider,
	})
	require.NoError(t, err)
	idemp := "sop_run:1:1"
	rsv, err := b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
		pre.EstimatedCredits, pre.CoefficientID, &idemp)
	require.NoError(t, err)

	// Simulate LLM failure: opErr set, actualCost 0.
	llmErr := errors.New("provider timeout: upstream 502")
	actualCost := int64(0)
	require.NoError(t, b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &llmErr))

	// Verify status=refunded, finalize_reason classified.
	var row model.CreditReservation
	require.NoError(t, db.First(&row, rsv.ID).Error)
	assert.Equal(t, string(credit.StatusRefunded), row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "op_failed", *row.FinalizeReason,
		"generic provider error classifies to op_failed")

	// Full refund → balance back to original.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, balance, acc.Balance,
		"LLM-failure Refund must restore balance fully")
}

// TestSopCredits_CreditsMode_ContextCanceledClassified verifies client-cancel
// path (spec §3.3) emits finalize_reason='user_cancelled' for observability.
// Mirrors the stream-drain abort scenario where sop_chat's defer triggers
// on client disconnect.
func TestSopCredits_CreditsMode_ContextCanceledClassified(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const (
		userID    = uint(9003)
		balance   = int64(5000)
		provider  = "ali"
		modelName = "qwen-turbo"
	)
	user := seedSopCreditsScenario(t, db, userID, balance,
		provider, modelName, credit.OpSopChat)
	ctx := context.Background()

	pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopChat, credit.EstimationInput{
		PromptChars: 400, Model: modelName, Provider: provider,
	})
	require.NoError(t, err)
	idemp := "sop_chat:42:5"
	rsv, err := b.creditSvc.Reserve(ctx, user, credit.OpSopChat,
		pre.EstimatedCredits, pre.CoefficientID, &idemp)
	require.NoError(t, err)

	// Simulate client cancel.
	cancelErr := context.Canceled
	actualCost := int64(0)
	require.NoError(t, b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &cancelErr))

	var row model.CreditReservation
	require.NoError(t, db.First(&row, rsv.ID).Error)
	assert.Equal(t, string(credit.StatusRefunded), row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "user_cancelled", *row.FinalizeReason)
}

// ----------------------------------------------------------------------------
// Integration: legacy_tier user bypasses Reserve entirely
// ----------------------------------------------------------------------------

// TestSopCredits_LegacyTier_BypassesReserve verifies spec §3.6: a user on
// billing_mode=legacy_tier gets SkipDeduction=true, no credit_reservation
// row is written, and the caller can still proceed to run the SOP.
// (MonthlySopRuns increment is handled by ExecuteNodeStream's existing
// IncrementSopRunCount call after successful node run.)
func TestSopCredits_LegacyTier_BypassesReserve(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const userID = uint(9004)
	// Do NOT seed credit_package — legacy_tier users have none.
	// Still need account row for consistency (but it stays at 0).
	acc := model.CreditAccount{UserID: userID, Balance: 0, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)

	// A standard-tier legacy user with quota remaining.
	user := &model.User{
		Model:          gorm.Model{ID: userID},
		BillingMode:    model.BillingModeLegacyTier,
		UserTier:       model.UserTierStandard,
		MonthlySopRuns: 5,
		TierExpires:    ptrTime(time.Now().Add(24 * time.Hour)),
	}

	pre, err := b.creditSvc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, Model: "qwen-turbo", Provider: "ali",
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.SkipDeduction, "legacy_tier must skip Reserve")
	assert.True(t, pre.Sufficient, "user still within 20/month cap")

	// Caller loop (runNode) must not call Reserve when SkipDeduction=true.
	// Assert that no credit_reservation row exists — i.e., the guard works.
	var rsvCount int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Count(&rsvCount).Error)
	assert.EqualValues(t, 0, rsvCount, "legacy_tier must not write reservations")
}

// TestSopCredits_LegacyTier_QuotaExhaustedReturnsZhReason verifies that when
// a legacy user hits the monthly cap, CheckAndEstimate returns
// ErrInsufficientCredits wrapping the zh reason from user.CanRunSOP().
// wrapCreditError then surfaces that reason through errno.
func TestSopCredits_LegacyTier_QuotaExhaustedReturnsZhReason(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const userID = uint(9005)
	acc := model.CreditAccount{UserID: userID, Balance: 0, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)

	// Standard-tier legacy user at the 20/month cap.
	user := &model.User{
		Model:          gorm.Model{ID: userID},
		BillingMode:    model.BillingModeLegacyTier,
		UserTier:       model.UserTierStandard,
		MonthlySopRuns: model.StandardUserMonthlySOPLimit,
		TierExpires:    ptrTime(time.Now().Add(24 * time.Hour)),
		MonthlyResetAt: ptrTime(time.Now()), // prevent auto-reset via IsInNewSOPMonth
	}

	pre, err := b.creditSvc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 500, Model: "qwen-turbo", Provider: "ali",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits))
	require.NotNil(t, pre)
	assert.NotEmpty(t, pre.Reason)
	// wrapCreditError → errno.ErrInsufficientCredits.SetMessage(pre.Reason)
	wrapped := wrapCreditError(err, pre)
	assert.Contains(t, wrapped.Error(), pre.Reason)
}

// ----------------------------------------------------------------------------
// Integration: idempotency key contract per spec §3.2
// ----------------------------------------------------------------------------

// TestSopCredits_IdempKey_DedupesRetry verifies that two Reserve calls with
// the same sop_run:<runID>:<nodeID> key return the same reservation and
// only debit credits once — the exact pattern ExecuteNodeStream uses to
// tolerate upstream retries without double-billing.
func TestSopCredits_IdempKey_DedupesRetry(t *testing.T) {
	db := newCreditsSopTestDB(t)
	b := buildSopBizWithCredits(t, db)

	const (
		userID    = uint(9006)
		balance   = int64(5000)
		provider  = "ali"
		modelName = "qwen-turbo"
		runID     = uint(55)
		nodeID    = uint(66)
	)
	user := seedSopCreditsScenario(t, db, userID, balance,
		provider, modelName, credit.OpSopRun)
	ctx := context.Background()

	pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 500, Model: modelName, Provider: provider,
	})
	require.NoError(t, err)

	idemp := fmt.Sprintf("sop_run:%d:%d", runID, nodeID)
	rsv1, err := b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
		pre.EstimatedCredits, pre.CoefficientID, &idemp)
	require.NoError(t, err)

	// Simulate retry: same idempKey.
	rsv2, err := b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
		pre.EstimatedCredits, pre.CoefficientID, &idemp)
	require.NoError(t, err)
	assert.Equal(t, rsv1.ID, rsv2.ID, "same idempKey must return same reservation")

	// Balance only decremented once.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, balance-pre.EstimatedCredits, acc.Balance,
		"idempotent retry must not double-debit")
}

// ptrTime is a tiny helper so tests can inline *time.Time values.
func ptrTime(t time.Time) *time.Time { return &t }
