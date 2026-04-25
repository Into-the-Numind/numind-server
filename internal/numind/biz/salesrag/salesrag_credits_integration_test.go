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
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL,
    coefficient_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uk_idempotency_key ON credit_reservation(idempotency_key);`).Error)

	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_id INTEGER NOT NULL,
    package_id INTEGER NOT NULL,
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
	user := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierStandard,
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
	return context.WithValue(ctx, known.XRequestIDKey, id)
}

// --- Test 1: happy path — credits user Chat debits balance + reconciles ---

// TestAcquireSalesragCredits_CreditsHappyPath verifies a full credits flow:
// Reserve deducts EstimatedCredits from the user's package; recordLLMResult
// captures actualCost; finalize triggers Reconcile with the correct delta
// (actual < reserved → refund surplus back to the package).
func TestAcquireSalesragCredits_CreditsHappyPath(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

	// Seed a user with 1000 credits and the fallback coef + pricing rule.
	userID := uint(1001)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-1")

	// Acquire: CheckAndEstimate + Reserve.
	cc, err := b.acquireSalesragCredits(ctx, userID, 42, 500)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cc.rsv, "credits user must reserve")
	assert.Greater(t, cc.rsv.ReservedCredits, int64(0))

	// Snapshot balance after Reserve — it must have been debited FIFO.
	var accAfterReserve model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accAfterReserve).Error)
	assert.Equal(t, 1000-cc.rsv.ReservedCredits, accAfterReserve.Balance,
		"balance should decrement by ReservedCredits after Reserve")

	// Simulate successful LLM: pricing (200 / 800 per Mtok flat) for 100 prompt +
	// 50 completion tokens = ceil(100*200/1e6 * 100 + 50*800/1e6 * 100) = ceil(2+4) = 6 cents.
	cc.recordLLMResult(ctx, nil, "", "", 100, 50)
	assert.EqualValues(t, 6, cc.actualCost, "actualCost = 100*200/1e6 + 50*800/1e6 = 0.06 yuan = 6 cents")

	// Finalize → Reconcile (delta = 6 - reserved). Surplus refunded to package.
	cc.finalize(ctx)

	// The reservation row must be reconciled.
	var rsvRow model.CreditReservation
	require.NoError(t, db.First(&rsvRow, cc.rsv.ID).Error)
	assert.Equal(t, "reconciled", rsvRow.Status, "finalize should reconcile reservation")
	require.NotNil(t, rsvRow.ActualCostCents)
	assert.EqualValues(t, 6, *rsvRow.ActualCostCents)

	// Balance should rebound by the delta (refund of over-reservation).
	var accAfterFinalize model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accAfterFinalize).Error)
	expectedBalance := int64(1000 - 6) // net-net the user only paid the actual cost.
	assert.Equal(t, expectedBalance, accAfterFinalize.Balance,
		"after Reconcile the balance equals 1000 - actualCost; surplus is refunded to package")
}

// --- Test 2: insufficient credits → ErrInsufficientCredits wrapped as errno ---

// TestAcquireSalesragCredits_InsufficientBalance verifies Reserve fails loudly
// when the user has zero credits. The returned error must be the errno
// wrapper so HTTP controllers surface 402 Credits.Insufficient.
func TestAcquireSalesragCredits_InsufficientBalance(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

	userID := uint(1002)
	// Seed a credits-mode user but with zero balance (no packages).
	user := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierStandard,
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
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

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

// --- Test 4: mid-stream abort (context.Canceled) triggers Refund ---

// TestFinalize_StreamErrorTriggersRefund simulates the ChatStream-drain
// failure path: client disconnects or LLM provider times out mid-stream.
// recordLLMResult is called with a non-nil streamErr; finalize should
// invoke Refund (not Reconcile), restoring the reserved credits to their
// origin package.
func TestFinalize_StreamErrorTriggersRefund(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

	userID := uint(1004)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-4")

	cc, err := b.acquireSalesragCredits(ctx, userID, 77, 500)
	require.NoError(t, err)
	require.NotNil(t, cc.rsv)
	reservedAmount := cc.rsv.ReservedCredits

	// Snapshot balance after Reserve (before the abort).
	var accAfterReserve model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accAfterReserve).Error)
	assert.Equal(t, 1000-reservedAmount, accAfterReserve.Balance)

	// Simulate mid-stream abort: caller passes context.Canceled.
	cc.recordLLMResult(ctx, context.Canceled, "", "", 0, 0)
	assert.EqualValues(t, 0, cc.actualCost, "abort path never records actualCost")
	assert.ErrorIs(t, cc.opErr, context.Canceled)

	// Finalize → Refund (classifyReason maps context.Canceled → "user_cancelled").
	cc.finalize(ctx)

	// Reservation row must be refunded.
	var rsvRow model.CreditReservation
	require.NoError(t, db.First(&rsvRow, cc.rsv.ID).Error)
	assert.Equal(t, "refunded", rsvRow.Status, "abort path must refund")
	require.NotNil(t, rsvRow.FinalizeReason)
	assert.Equal(t, "user_cancelled", *rsvRow.FinalizeReason,
		"context.Canceled maps to user_cancelled refund reason")

	// Balance must have rebounded fully.
	var accAfter model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&accAfter).Error)
	assert.Equal(t, int64(1000), accAfter.Balance, "full refund restores original balance")
}

// --- Test 5: idempotency — duplicate request with same request_uuid reuses reservation ---

// TestAcquireSalesragCredits_IdempotentReplay verifies that a retried call
// with the same X-Request-ID (same session_id + request_uuid → same
// idempotency_key) returns the existing reservation instead of double-
// debiting. Protects against network-retry double-charge.
func TestAcquireSalesragCredits_IdempotentReplay(t *testing.T) {
	db := newSalesragCreditsTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

	userID := uint(1005)
	seedCreditsUserWithPackage(t, db, userID, 1000)
	seedSalesragCoefficient(t, db)

	b := newTestSalesragBiz(ds, svc, calc)
	ctx := ctxWithRequestID(context.Background(), "req-ctx-idemp")

	cc1, err := b.acquireSalesragCredits(ctx, userID, 99, 500)
	require.NoError(t, err)
	require.NotNil(t, cc1.rsv)

	// Second call with identical request_uuid → same idempotency_key →
	// Reserve returns existing row (no second debit).
	cc2, err := b.acquireSalesragCredits(ctx, userID, 99, 500)
	require.NoError(t, err)
	require.NotNil(t, cc2.rsv)
	assert.Equal(t, cc1.rsv.ID, cc2.rsv.ID, "same idempotency_key must return the same reservation")

	// Only ONE reservation row should exist for this user+session.
	var count int64
	db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&count)
	assert.EqualValues(t, 1, count, "idempotent replay must not create a second reservation")
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
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), calc)

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
