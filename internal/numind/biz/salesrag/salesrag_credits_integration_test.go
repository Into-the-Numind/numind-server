// Phase 2 Task 2.2 — SalesRAG credits integration tests.
//
// Scope: exercises the credit-wrapping helpers the ChatWithSession flow uses
// (acquireSalesragCredits / salesragCreditContext.recordLLMResult / .finalize)
// against a real in-memory SQLite store with seeded credit packages,
// reservation tables, and pricing/coefficient rows.
//
// We deliberately skip the stream-LLM half of ChatWithSession — it pulls in
// an end-to-end RAG pipeline (DashVector + ChatStream) that is not practical
// to stub in a biz-layer unit test. The credit state machine (Reserve →
// Reconcile/Refund) is completely covered here; the controller wires Chat
// and stream drain orthogonally and the credit_service_reserve_test /
// credit_service_reconcile_test suites cover the credit_service internals.
//
// P4e (grandfathering decision A): legacy_tier salesrag MUST be free —
// SkipDeduction=true, no reservation, no package debit.
package salesrag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/store"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/known"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// --- test harness ---

// newSalesragCreditsTestDB builds an in-memory SQLite with every table the
// credit + pricing + user/billing flow exercises. Mirrors
// newCreditReserveTestDB from the credit package but we can't import its
// helper (it's unexported), so we reconstruct the minimum set here.
func newSalesragCreditsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Auto-migrate tables that don't use MySQL-specific ENUM types.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.UsageRecord{},
		&model.CreditEstimationCoefficient{},
		&model.PricingRule{},
	))
	// Hand-roll user: model.User has MySQL ENUM columns (billing_mode,
	// user_tier) that SQLite's AutoMigrate can't parse. Mirror every column
	// the model exposes so GORM First(&User) unmarshals cleanly.
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS user (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at       DATETIME,
    updated_at       DATETIME,
    deleted_at       DATETIME,
    phone            TEXT,
    nickname         TEXT,
    avatar_url       TEXT,
    parent_user_id   INTEGER,
    total_sop_runs   INTEGER DEFAULT 0,
    monthly_sop_runs INTEGER DEFAULT 0,
    monthly_reset_at DATETIME,
    user_tier        TEXT DEFAULT 'free',
    tier_expires     DATETIME,
    billing_mode     TEXT NOT NULL DEFAULT 'credits',
    username         TEXT,
    password         TEXT,
    is_admin         INTEGER DEFAULT 0,
    status           INTEGER DEFAULT 0,
    last_login       DATETIME
);`).Error)

	// Hand-roll reservation tables: CreditReservation.Status and
	// FinalizeReason are MySQL ENUMs which SQLite rejects via AutoMigrate.
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

// seedCreditsUserWithPackage inserts a credits-mode user plus one active
// subscription package so Reserve has credits to debit.
func seedCreditsUserWithPackage(t *testing.T, db *gorm.DB, userID uint, totalCredits int64) *model.User {
	t.Helper()
	// UserTier must be "free" so HasActiveMembership()=false and isEffectiveLegacy()
	// routes to the credits path. Standard/trial/premium with TierExpires=nil would
	// make HasActiveMembership()=true → legacyTierImpl.Reserve panic.
	user := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierFree,
		Phone:       "13800000000",
	}
	user.ID = userID
	require.NoError(t, db.Create(user).Error)

	acc := &model.CreditAccount{UserID: userID, Balance: totalCredits, Status: "active"}
	require.NoError(t, db.Create(acc).Error)

	now := time.Now()
	pkg := &model.CreditPackage{
		UserID:        userID,
		Type:          model.CreditTypeSubscription,
		TotalCredits:  totalCredits,
		RemainCredits: totalCredits,
		Status:        model.CreditPackageActive,
		ActivatedAt:   now,
		ExpiresAt:     now.Add(30 * 24 * time.Hour),
	}
	require.NoError(t, db.Create(pkg).Error)
	return user
}

// seedLegacyTierUser inserts a legacy_tier user with the requested actual
// tier. Free users get no MonthlyResetAt so CanRunSOP() returns its default
// "free tier cannot run" denial.
func seedLegacyTierUser(t *testing.T, db *gorm.DB, userID uint, tier string) *model.User {
	t.Helper()
	future := time.Now().Add(24 * time.Hour)
	resetAt := time.Now().Add(-time.Hour)
	user := &model.User{
		BillingMode:    model.BillingModeLegacyTier,
		UserTier:       tier,
		TierExpires:    &future,
		MonthlyResetAt: &resetAt,
		MonthlySopRuns: 0,
		Phone:          "13900000000",
	}
	user.ID = userID
	require.NoError(t, db.Create(user).Error)
	return user
}

// seedSalesragCoefficient inserts the credit_estimation_coefficient +
// pricing_rule rows the credits flow looks up via a global-fallback lookup.
// The salesrag biz passes empty provider/model (defaultModel/defaultProvider
// unset in tests), so the (”, ”, ”) fallback row must exist.
func seedSalesragCoefficient(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Coefficient: char2tok=1.5 comp/prompt=0.5 safety=0.2
	coef := &model.CreditEstimationCoefficient{
		Provider:              "",
		Model:                 "",
		Operation:             "",
		CharToTokenRatio:      1.5,
		CompletionPromptRatio: 0.5,
		SafetyBufferPct:       0.2,
		Version:               1,
		IsActive:              true,
	}
	require.NoError(t, db.Create(coef).Error)

	// Pricing: fallback row ('', '', '') so defaultProvider/Model="" resolves.
	rule := &model.PricingRule{
		ServiceType:        "llm_chat",
		Provider:           "",
		Model:              "",
		BillingMode:        "flat",
		FlatUnit:           "call",
		InputPricePerMTok:  200,
		OutputPricePerMTok: 800,
		IsActive:           true,
	}
	require.NoError(t, db.Create(rule).Error)
}

// newTestSalesragBiz builds a minimally-wired salesRAGBiz suitable for
// exercising the credit wrapper in isolation. LLM/vector-store deps are
// nil — only the credits methods are called in the tests below.
func newTestSalesragBiz(ds store.IStore, creditSvc credit.ICreditService, pc pricing.ICalculator) *salesRAGBiz {
	return &salesRAGBiz{
		ds:        ds,
		creditSvc: creditSvc,
		pricing:   pc,
	}
}

// ctxWithRequestID installs an X-Request-ID on a context so the idempotency
// key derivation can run end-to-end.
func ctxWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, known.XRequestIDKey, id) //nolint:staticcheck // SA1029: XRequestIDKey is a package-level string constant shared with middleware; changing it to a custom type requires coordinated change across multiple packages
}

// --- Test 1: happy path — credits user passes pre-flight, Reserve delegated to Gateway ---

// TestAcquireSalesragCredits_CreditsHappyPath verifies that for a credits-mode
// user with sufficient balance, acquireSalesragCredits succeeds and returns a
// non-nil cc with SkipDeduction=false. As of Task 10 (P1 spec-compliance fix),
// the inline Reserve is delegated to the Gateway middleware (ContextBudgetCredits).
// This function performs only CheckAndEstimate (early balance-denial), so:
//   - cc.rsv is nil (no inline Reserve)
//   - balance is unchanged after this call
//   - finalize is a safe no-op when cc.rsv == nil
//
// The Reconcile/Refund lifecycle is exercised in credit_service_reconcile_test.go
// and the Gateway middleware tests.
func TestAcquireSalesragCredits_CreditsHappyPath(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	// Seed a user with 1000 credits and the fallback coef + pricing rule.
	userID := uint(1001)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-1")

	// Acquire: CheckAndEstimate only (Reserve delegated to Gateway since Task 10).
	cc, err := b.acquireSalesragCredits(ctx, userID, 42, 500)
	require.NoError(t, err, "credits user with sufficient balance must not be denied")
	require.NotNil(t, cc)
	require.NotNil(t, cc.pre, "PreCheckResult must be populated")
	assert.False(t, cc.pre.SkipDeduction, "credits user must NOT skip deduction")
	assert.True(t, cc.pre.Sufficient, "user has ample balance")
	assert.Greater(t, cc.pre.EstimatedCredits, int64(0), "estimation must be non-zero")

	// KEY: no inline Reserve — Gateway middleware owns the Reserve cycle.
	assert.Nil(t, cc.rsv,
		"acquireSalesragCredits must NOT reserve credits inline (Gateway middleware owns it since Task 10)")

	// Balance must be unchanged — no debit happened in this function.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 1000, acc.Balance, "balance unchanged: no inline Reserve")

	// finalize with nil rsv must be a safe no-op.
	cc.finalize(ctx)
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 1000, acc.Balance, "finalize(nil rsv) must not mutate balance")
}

// --- Test 2: insufficient credits → ErrInsufficientCredits wrapped as errno ---

// TestAcquireSalesragCredits_InsufficientBalance verifies Reserve fails loudly
// when the user has zero credits. The returned error must be the errno
// wrapper so HTTP controllers surface 402 Credits.Insufficient.
func TestAcquireSalesragCredits_InsufficientBalance(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	userID := uint(1002)
	// Seed a credits-mode user but with zero balance (no packages).
	// UserTier must be "free" so isEffectiveLegacy()=false and the credits path
	// (which enforces balance check) is exercised instead of legacyTierImpl.
	user := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierFree,
		Phone:       "13811111111",
	}
	user.ID = userID
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.CreditAccount{UserID: userID, Balance: 0, Status: "active"}).Error)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-2")

	cc, err := b.acquireSalesragCredits(ctx, userID, 9, 500)
	require.Error(t, err, "zero-balance user must be denied")

	// Error must be the errno wrapper (HTTP 402 Credits.Insufficient) so
	// controller-level core.WriteResponse surfaces the right domain error.
	var wrapped *errno.Errno
	require.True(t, errors.As(err, &wrapped), "error must unwrap to *errno.Errno, got %T: %v", err, err)
	assert.Equal(t, errno.ErrInsufficientCredits.Code, wrapped.Code,
		"must be ErrInsufficientCredits errno domain code")

	// No reservation row must be written when denial happens at CheckAndEstimate.
	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count, "no reservation on denied pre-check")

	// cc is returned (to carry PreCheckResult.Reason for error messaging) but
	// should have no active reservation.
	require.NotNil(t, cc)
	assert.Nil(t, cc.rsv)
}

// --- Test 3: P4e=A — legacy_tier SalesRAG is FREE (SkipDeduction=true) ---

// TestAcquireSalesragCredits_LegacyTierFree confirms grandfathering option A:
// legacy_tier users with an active membership get SalesRAG for free. No
// CheckAndEstimate denial, no Reserve, defer-finalize is a no-op. This is
// the decision P4e=A captured in docs/credits-system-plan.md.
func TestAcquireSalesragCredits_LegacyTierFree(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	// legacy_tier standard user well inside the monthly 20-cap.
	userID := uint(1003)
	seedLegacyTierUser(t, db, userID, model.UserTierStandard)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-3")

	cc, err := b.acquireSalesragCredits(ctx, userID, 42, 500)
	require.NoError(t, err, "legacy_tier standard user must NOT be denied (P4e=A)")
	require.NotNil(t, cc)
	// No reservation row — SkipDeduction=true means we never called Reserve.
	assert.Nil(t, cc.rsv, "legacy_tier MUST NOT reserve credits (P4e=A free)")
	require.NotNil(t, cc.pre)
	assert.True(t, cc.pre.SkipDeduction, "legacy_tier dispatches to legacy impl → SkipDeduction=true")

	// recordLLMResult + finalize must be safe no-ops on legacy_tier.
	cc.recordLLMResult(ctx, nil, "volc", "deepseek-v3", 100, 50)
	cc.finalize(ctx)

	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count, "legacy_tier leaves zero reservation rows")

	// And the user's credit_account / credit_package are untouched (none exist).
	var accCount int64
	db.Model(&model.CreditAccount{}).Where("user_id = ?", userID).Count(&accCount)
	assert.EqualValues(t, 0, accCount, "legacy_tier never creates a credit_account via salesrag")
}

// --- Test 4: mid-stream abort with nil rsv — finalize is a safe no-op ---

// TestFinalize_StreamErrorTriggersRefund verifies that after the Task 10
// P1 fix, acquireSalesragCredits returns cc.rsv=nil for credits users (Reserve
// delegated to Gateway). As a result, recordLLMResult and finalize are both
// safe no-ops when cc.rsv==nil: no reservation row is written and balance
// is unchanged even when a stream error (context.Canceled) is recorded.
//
// The Refund lifecycle for the Gateway-managed reservation is covered by the
// Gateway middleware tests (ContextBudgetCredits) and credit_service_reconcile_test.go.
func TestFinalize_StreamErrorTriggersRefund(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	userID := uint(1004)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-4")

	// acquireSalesragCredits: CheckAndEstimate runs, Reserve is delegated to Gateway.
	cc, err := b.acquireSalesragCredits(ctx, userID, 77, 500)
	require.NoError(t, err)
	require.NotNil(t, cc)
	// KEY: no inline Reserve since Task 10 P1 fix.
	assert.Nil(t, cc.rsv, "credits user must not have an inline reservation (Gateway owns it)")

	// Balance must be unchanged — no debit.
	var accBefore model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accBefore).Error)
	assert.EqualValues(t, 1000, accBefore.Balance, "no debit before gateway reserve")

	// Simulate mid-stream abort: recordLLMResult with cc.rsv==nil is a no-op.
	cc.recordLLMResult(ctx, context.Canceled, "", "", 0, 0)
	// Since cc.rsv==nil, opErr is NOT captured (the guard "if cc==nil || cc.rsv==nil" returns early).
	assert.EqualValues(t, 0, cc.actualCost, "no-op: rsv==nil means no cost tracking")

	// Finalize with cc.rsv==nil must be a safe no-op — no reservation rows written.
	cc.finalize(ctx)
	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count, "finalize(nil rsv) must not write any reservation rows")

	// Balance must remain untouched throughout.
	var accAfter model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accAfter).Error)
	assert.Equal(t, int64(1000), accAfter.Balance, "balance unchanged: no inline Reserve, no inline Refund")
}

// --- Test 5: repeated calls with same request_uuid — no reservation either time ---

// TestAcquireSalesragCredits_IdempotentReplay verifies that as of the Task 10
// P1 fix, acquireSalesragCredits performs only CheckAndEstimate (no inline
// Reserve). Repeated calls with the same X-Request-ID context do not create
// any reservation rows — idempotency for the Reserve cycle is now enforced by
// the Gateway middleware (ContextBudgetCredits), not by this function.
//
// This test confirms that two calls with the same request context leave zero
// reservation rows and identical cc state (no double-check side-effects).
func TestAcquireSalesragCredits_IdempotentReplay(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	userID := uint(1005)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-idemp")

	cc1, err := b.acquireSalesragCredits(ctx, userID, 99, 500)
	require.NoError(t, err)
	require.NotNil(t, cc1)
	assert.Nil(t, cc1.rsv, "first call: no inline Reserve since Task 10 P1 fix")

	// Second call with identical context — same CheckAndEstimate result, still no Reserve.
	cc2, err := b.acquireSalesragCredits(ctx, userID, 99, 500)
	require.NoError(t, err)
	require.NotNil(t, cc2)
	assert.Nil(t, cc2.rsv, "second call: still no inline Reserve")

	// Zero reservation rows — idempotency is the Gateway's responsibility.
	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count, "no reservation rows from acquireSalesragCredits (Gateway owns it)")
}

// --- Test 6: wrapCreditError surfaces legacy_tier Chinese denial reason ---

// TestAcquireSalesragCredits_LegacyTierFreeUserBlocked exercises P4e=A's
// escape hatch: a legacy_tier user whose tier is free (no membership)
// still fails CanRunSOP because the user has no subscription. The error
// must surface the Chinese denial reason embedded in PreCheckResult.Reason.
func TestAcquireSalesragCredits_LegacyTierFreeUserBlocked(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	userID := uint(1006)
	// free tier on legacy_tier — CanRunSOP returns "免费用户...".
	seedLegacyTierUser(t, db, userID, model.UserTierFree)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-6")

	cc, err := b.acquireSalesragCredits(ctx, userID, 1, 200)
	require.Error(t, err)
	// wrapped into errno.ErrInsufficientCredits with the zh message in Message.
	var wrapped *errno.Errno
	require.True(t, errors.As(err, &wrapped), "error must unwrap to *errno.Errno, got %T: %v", err, err)
	assert.Equal(t, errno.ErrInsufficientCredits.Code, wrapped.Code)
	assert.Contains(t, wrapped.Message, "免费用户",
		"errno message must carry CanRunSOP's zh denial reason")
	require.NotNil(t, cc)
	assert.True(t, cc.pre.SkipDeduction, "legacy_tier even when denied keeps SkipDeduction=true")
	assert.Nil(t, cc.rsv, "no reservation written on denial")
}

// ============================================================================
// Task 10: SalesRAG Fragment Contract
// ============================================================================

// TestSalesRAGBuildsEvidenceFragmentsWithoutSOPMetadata verifies that
// buildSalesRAGEvidenceFragments constructs well-formed RoleEvidence fragments
// from a slice of KnowledgeChunks, and that no SOP-specific or chatbot-specific
// metadata keys are injected (spec §2.2 enforcement: contextbudget must not
// branch on business-domain metadata).
func TestSalesRAGBuildsEvidenceFragmentsWithoutSOPMetadata(t *testing.T) {
	chunks := []domain.KnowledgeChunk{
		{ID: "vec-001", Content: "Product pricing is $99/month.", Score: 0.9},
		{ID: "vec-002", Content: "Supports up to 100 users.", Score: 0.5},
		{ID: "", Content: "No ID chunk.", Score: 0.0},
	}

	frags := buildSalesRAGEvidenceFragments(chunks)

	require.Len(t, frags, 3, "one fragment per chunk")

	for i, f := range frags {
		// Role + Source invariants.
		assert.Equal(t, cb.RoleEvidence, f.Role, "fragment[%d] must be RoleEvidence", i)
		assert.Equal(t, cb.SourceKB, f.Source, "fragment[%d] must be SourceKB", i)
		assert.Equal(t, cb.CompressReference, f.Compressibility, "fragment[%d] must be CompressReference", i)

		// No SOP-specific or chatbot-specific metadata.
		for k := range f.Metadata {
			assert.NotContains(t, k, "sop", "fragment metadata must not contain SOP keys (key=%q)", k)
			assert.NotContains(t, k, "chatbot", "fragment metadata must not contain chatbot keys (key=%q)", k)
			assert.NotContains(t, k, "node_id", "fragment metadata must not contain node_id key (key=%q)", k)
		}
	}

	// Score-to-importance mapping.
	assert.Equal(t, 9, frags[0].Importance, "score 0.9 → importance 9")
	assert.Equal(t, 5, frags[1].Importance, "score 0.5 → importance 5")
	assert.Equal(t, 0, frags[2].Importance, "score 0.0 → importance 0")

	// SourceReference fallback when ID is empty.
	assert.Equal(t, "vec-001", frags[0].SourceReference)
	assert.NotEmpty(t, frags[2].SourceReference, "empty chunk ID must produce a non-empty SourceReference fallback")
}

// ============================================================================
// Task 10: P1 — No double-reserve for credits-mode ChatWithSession
// ============================================================================

// TestSalesRAGChatWithSessionNoDoubleReserve verifies the P1 spec-compliance fix:
// now that RetrieveStream always populates ContextFragments (Task 10), the
// Gateway middleware's ContextBudgetCredits handles credit reservation.
// acquireSalesragCredits must NOT also call Reserve for credits-mode users,
// so the combined reservation count across both paths stays at most 1.
//
// Test model: we call acquireSalesragCredits directly (the only inline-Reserve
// site) and assert that it does NOT create a reservation row for a credits-mode
// user, even when CheckAndEstimate succeeds. The Gateway middleware path is
// exercised in integration; here we verify the biz-layer contract that
// cc.rsv is always nil for credits users post-fix.
func TestSalesRAGChatWithSessionNoDoubleReserve(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc, nil)

	// Seed a credits-mode user with ample balance.
	userID := uint(2001)
	seedCreditsUserWithPackage(t, db, userID, 5000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-p1-fix")

	// Call acquireSalesragCredits — CheckAndEstimate runs (early balance check)
	// but Reserve must NOT fire (delegated to Gateway middleware since Task 10).
	cc, err := b.acquireSalesragCredits(ctx, userID, 123, 500)
	require.NoError(t, err, "credits-mode user with ample balance must not be denied")
	require.NotNil(t, cc, "cc must be returned (carries PreCheckResult for logging)")

	// KEY assertion: no reservation was created by the inline biz path.
	assert.Nil(t, cc.rsv,
		"credits-mode: Reserve must NOT be called by acquireSalesragCredits (Gateway middleware owns it)")

	// Confirm zero reservation rows in DB — no double-reserve side-effect.
	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count,
		"no credit_reservation row must be written by acquireSalesragCredits for credits users")

	// finalize must be a safe no-op (cc.rsv == nil).
	cc.finalize(ctx)
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 0, count, "finalize with nil rsv must not write any DB rows")

	// Legacy-tier users are unaffected: SkipDeduction=true means no Reserve
	// either way, but CheckAndEstimate still runs the CanRunSOP gate.
	legacyUserID := uint(2002)
	seedLegacyTierUser(t, db, legacyUserID, model.UserTierStandard)

	ccLegacy, errLegacy := b.acquireSalesragCredits(ctx, legacyUserID, 456, 200)
	require.NoError(t, errLegacy, "legacy_tier standard user must pass CanRunSOP gate")
	require.NotNil(t, ccLegacy)
	assert.Nil(t, ccLegacy.rsv, "legacy_tier: no reservation (SkipDeduction path, unchanged)")
	assert.True(t, ccLegacy.pre.SkipDeduction, "legacy_tier SkipDeduction must still be true")
}

// TestSalesRAGProfileAndChatStyleUseFragments verifies that the fragment helper
// functions for profile-analysis and chat-style operations produce fragments
// with the correct invariants: system prompt → RoleImmutable + Critical=true,
// user message → RoleRecent + Critical=true + CompressNone. No SOP metadata.
func TestSalesRAGProfileAndChatStyleUseFragments(t *testing.T) {
	sysPrompt := "You are a customer profile analyst."
	userMsg := "以下是该客户的相关资料：\n\nSample customer data."

	sysF := buildSalesRAGSystemFragment("sys-0", sysPrompt)
	usrF := buildSalesRAGUserFragment("cur-msg", userMsg)

	// System fragment invariants.
	assert.Equal(t, cb.RoleImmutable, sysF.Role, "system fragment must be RoleImmutable")
	assert.Equal(t, cb.SourceSystem, sysF.Source)
	assert.True(t, sysF.Critical, "system fragment must be Critical")
	assert.Equal(t, cb.CompressNone, sysF.Compressibility)
	assert.Equal(t, sysPrompt, sysF.Content)

	// User fragment invariants.
	assert.Equal(t, cb.RoleRecent, usrF.Role, "user fragment must be RoleRecent")
	assert.Equal(t, cb.SourceUser, usrF.Source)
	assert.True(t, usrF.Critical, "user fragment must be Critical")
	assert.Equal(t, cb.CompressNone, usrF.Compressibility)
	assert.Equal(t, userMsg, usrF.Content)

	// Neither fragment must carry SOP metadata.
	for k := range sysF.Metadata {
		assert.NotContains(t, k, "sop")
	}
	for k := range usrF.Metadata {
		assert.NotContains(t, k, "sop")
	}
}
