// Package sop — sop_credits_integration_test.go
//
// Phase 2 Task 2.1 integration tests for the Reserve → LLM → Reconcile
// control-flow inversion in ExecuteNodeStream / ChatAfterRunStream.
//
// Scope: these tests drive the credit-side plumbing with the exact
// Operation + idempotency-key + EstimationInput values that the new sop.go
// passes to ICreditService, and assert on the resulting DB state
// (credit_reservation row, status transitions, items, account balance).
// End-to-end coverage of the LLM call itself is deferred to Playwright
// (Phase 2.5) since SopExecutor is a concrete struct that requires the full
// aiservice Gateway.
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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	membershipmodel "numind-server/internal/pkg/model/membership"
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
	// T6: use per-test named in-memory DB with cache=shared and DO NOT cap
	// MaxOpenConns to 1 — MembershipService.DeductCreditsTx pre-reads on the
	// bare db inside the caller's tx; with MaxOpenConns=1 the pre-read
	// deadlocks against the outer tx.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// AutoMigrate tables that have no MySQL ENUMs.
	// T11: CreditPackage removed — table dropped, archived to legacy_credit_package_archive_20260515.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
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

	// T6: membership tables required by MembershipService.DeductCreditsTx
	// (the new authoritative deduction path post-T6).
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS subscription (
		    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
		    user_id                INTEGER NOT NULL UNIQUE,
		    first_started_at       DATETIME NOT NULL,
		    current_started_at     DATETIME NOT NULL,
		    expires_at             DATETIME NOT NULL,
		    total_months_purchased INTEGER NOT NULL,
		    source                 TEXT NOT NULL DEFAULT 'b2b_grant',
		    granter_user_id        INTEGER,
		    created_at             DATETIME NOT NULL,
		    updated_at             DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS trial_grant (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    user_id           INTEGER NOT NULL UNIQUE,
		    granted_at        DATETIME NOT NULL,
		    expires_at        DATETIME NOT NULL,
		    credits_remaining INTEGER NOT NULL DEFAULT 200,
		    source            TEXT NOT NULL DEFAULT 'b2b_grant',
		    granter_user_id   INTEGER,
		    created_at        DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS credit_cycle (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    user_id           INTEGER NOT NULL,
		    subscription_id   INTEGER NOT NULL,
		    cycle_start       DATETIME NOT NULL,
		    cycle_end         DATETIME NOT NULL,
		    credits_granted   INTEGER NOT NULL DEFAULT 0,
		    credits_remaining INTEGER NOT NULL DEFAULT 0,
		    created_at        DATETIME NOT NULL,
		    updated_at        DATETIME NOT NULL,
		    UNIQUE(user_id, cycle_start)
		)`,
		`CREATE TABLE IF NOT EXISTS user_booster_balance (
		    user_id            INTEGER PRIMARY KEY,
		    credits_remaining  INTEGER NOT NULL DEFAULT 0,
		    updated_at         DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS membership_event (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    user_id           INTEGER NOT NULL,
		    event_type        TEXT NOT NULL,
		    product_type      TEXT NOT NULL,
		    months            INTEGER,
		    quantity          INTEGER,
		    amount_cents      INTEGER NOT NULL DEFAULT 0,
		    source            TEXT NOT NULL,
		    granter_user_id   INTEGER,
		    idempotency_key   TEXT UNIQUE,
		    subscription_id   INTEGER,
		    occurred_at       DATETIME NOT NULL
		)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}

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

	// T11: credit_account.balance dropped; credit_package table archived.
	// Account creation no longer needs/has Balance field.
	now := time.Now()
	acc := model.CreditAccount{UserID: userID, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)
	// T11: no credit_package row — membership tables are the authoritative source.

	// T6: mirror into the new membership tables so
	// MembershipService.DeductCreditsTx (the new authoritative deduction path)
	// can debit the balance. The mirror is type-specific:
	//   - subscription → subscription + credit_cycle
	sub := membershipmodel.Subscription{
		UserID: uint64(userID), FirstStartedAt: now, CurrentStartedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), TotalMonthsPurchased: 1,
		Source: membershipmodel.SourceB2BGrant, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&sub).Error)
	cycle := membershipmodel.CreditCycle{
		UserID: uint64(userID), SubscriptionID: sub.ID,
		CycleStart: now, CycleEnd: now.Add(24 * time.Hour),
		CreditsGranted:   int(balance), //nolint:gosec // test fixture
		CreditsRemaining: int(balance), //nolint:gosec // test fixture
		CreatedAt:        now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&cycle).Error)

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

	// Post legacy-deprecation (T1+T4) the dispatch is gone; every user routes
	// through creditsImpl.
	return &model.User{
		Model: gorm.Model{ID: userID},
	}
}

// buildSopBizWithCredits constructs the struct under test with a real
// ICreditService + pricing calculator so helper assertions can observe the
// full plumbing.
//
// T6: wires MembershipService — legacy creditBiz.DeductCreditsTx fallback was
// removed, so credit_service Reserve/Reconcile require a non-nil membershipSvc.
func buildSopBizWithCredits(t *testing.T, db *gorm.DB) *sopBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	msvc := membership.NewMembershipService(db)
	cs := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, msvc)
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

	// 6. T6: balance now lives in credit_cycle.credits_remaining. Reserved
	// returned to cycle, reconcile charged 90 net.
	var cycleRemaining int64
	require.NoError(t, db.Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, userID,
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, balance-90, cycleRemaining)
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

	// T11: credit_account.balance dropped — verify via credit_cycle.credits_remaining.
	// Full refund → cycle balance back to original.
	var cycleRemain int64
	require.NoError(t, db.Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, userID,
	).Scan(&cycleRemain).Error)
	assert.EqualValues(t, balance, cycleRemain,
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

	// T6: credit_cycle.credits_remaining only decremented once.
	var cycleRemaining int64
	require.NoError(t, db.Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, userID,
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, balance-pre.EstimatedCredits, cycleRemaining,
		"idempotent retry must not double-debit")
}
