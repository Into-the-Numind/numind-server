package credit_test

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
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newCreditReserveTestDB extends newCreditTestDB with the reservation tables.
// Because CreditReservation.Status / FinalizeReason are MySQL ENUMs, we
// bypass AutoMigrate for those fields and hand-roll the CREATE TABLE in
// SQLite-compatible SQL (plain TEXT columns — SQLite has no native ENUM).
func newCreditReserveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// AutoMigrate the tables that have no MySQL-specific types.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.UsageRecord{},
		&model.CreditUserTypeConfig{},
	))

	// Hand-roll the reservation tables so SQLite accepts the ENUM columns as TEXT.
	// Includes context-budget extension columns (estimation_source, token_profile_id, etc.)
	// and user_type_multiplier (feature: credit-user-type-multiplier).
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

	// T4: add subscription table so HasActiveSubscription (now reading new table) doesn't
	// return "no such table: subscription" when called by GetUserTypeCreditMultiplier.
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS subscription (
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
);`).Error)

	return db
}

// newCreditsUser returns a credits-mode user with no membership constraints
// (the default for Reserve path tests).
//
// UserTier must be "free" (not standard/trial/premium) so that
// HasActiveMembership() returns false and isEffectiveLegacy() routes the user
// to the creditsImpl path instead of the legacy path. Setting UserTier=standard
// with TierExpires=nil causes GetActualUserTier() to return "standard" (nil
// expiry is treated as unexpired), which makes HasActiveMembership()=true and
// isEffectiveLegacy()=true — routing credits-mode inputs into legacyTierImpl
// which panics by design.
func newCreditsUser(id uint) *model.User {
	u := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierFree,
	}
	u.ID = id
	return u
}

// --- Task C.3: creditsImpl.Reserve ---

// TestReserve_HappyPath_SinglePackage verifies Reserve deducts credits FIFO
// and persists one credit_reservation + one credit_reservation_item.
func TestReserve_HappyPath_SinglePackage(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(200)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	idemp := "sop_run:1:1"
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 180, 1, &idemp)
	require.NoError(t, err)
	require.NotNil(t, rsv)
	assert.Equal(t, userID, rsv.UserID)
	assert.Equal(t, credit.OpSopRun, rsv.Operation)
	assert.EqualValues(t, 180, rsv.ReservedCredits)
	assert.EqualValues(t, 1, rsv.CoefficientID)
	assert.Equal(t, credit.StatusReserved, rsv.Status)
	require.NotNil(t, rsv.IdempotencyKey)
	assert.Equal(t, idemp, *rsv.IdempotencyKey)
	require.Len(t, rsv.Items, 1)
	assert.Equal(t, 1, rsv.Items[0].Seq)
	assert.EqualValues(t, 180, rsv.Items[0].Credits)
	assert.Equal(t, model.CreditTypeSubscription, rsv.Items[0].PackageType)

	// DB row written
	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)
	assert.Equal(t, "reserved", dbRsv.Status)
	assert.EqualValues(t, 180, dbRsv.ReservedCredits)

	// Item written with seq=1
	var items []model.CreditReservationItem
	require.NoError(t, db.Where("reservation_id = ?", rsv.ID).Order("seq ASC").Find(&items).Error)
	require.Len(t, items, 1)
	assert.Equal(t, 1, items[0].Seq)

	// Balance decremented
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 820, acc.Balance) // 1000-180
}

// TestReserve_FIFOCrossPackage verifies Reserve spans FIFO across two
// packages (sub → booster) and persists seq=1, seq=2 items in order.
func TestReserve_FIFOCrossPackage(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(201)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 600,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})

	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 150, 2, nil)
	require.NoError(t, err)
	require.Len(t, rsv.Items, 2)

	assert.Equal(t, 1, rsv.Items[0].Seq)
	assert.EqualValues(t, 50, rsv.Items[0].Credits)
	assert.Equal(t, model.CreditTypeSubscription, rsv.Items[0].PackageType)

	assert.Equal(t, 2, rsv.Items[1].Seq)
	assert.EqualValues(t, 100, rsv.Items[1].Credits)
	assert.Equal(t, model.CreditTypeBooster, rsv.Items[1].PackageType)

	// No idempotency_key on this call
	assert.Nil(t, rsv.IdempotencyKey)
}

// TestReserve_InsufficientRollsBack verifies that insufficient balance
// returns ErrInsufficientCredits and leaves zero reservation rows.
func TestReserve_InsufficientRollsBack(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(202)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	_, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 200, 1, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits))

	// Transaction rolled back: zero reservation rows, balance unchanged.
	var rsvCount int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Count(&rsvCount).Error)
	assert.EqualValues(t, 0, rsvCount)

	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 50, acc.Balance)
}

// TestReserve_IdempotencyReturnsExisting verifies that a duplicate Reserve
// with the same idempotency_key returns the pre-existing reservation WITHOUT
// double-deducting credits.
func TestReserve_IdempotencyReturnsExisting(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(203)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	idemp := "sop_run:999:1"
	rsv1, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 120, 1, &idemp)
	require.NoError(t, err)

	// Second call with same idempotency_key — must return same reservation.
	rsv2, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 120, 1, &idemp)
	require.NoError(t, err)
	assert.Equal(t, rsv1.ID, rsv2.ID, "idempotency key must dedupe")

	// Balance decremented once only.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 880, acc.Balance, "idempotent retry must not double-deduct")

	// Only one reservation row persisted.
	var rsvCount int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&rsvCount).Error)
	assert.EqualValues(t, 1, rsvCount)
}

// TestReserve_GetBalanceCredits verifies that credits-mode GetBalance returns
// the package-based breakdown (sub + booster, no RemainingRuns).
func TestReserve_GetBalanceCredits(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(204)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 1800,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 300,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})

	bal, err := svc.GetBalance(context.Background(), user)
	require.NoError(t, err)
	assert.Equal(t, model.BillingModeCredits, bal.BillingMode)
	assert.EqualValues(t, 2000, bal.SubTotal)
	assert.EqualValues(t, 1800, bal.SubRemain)
	assert.EqualValues(t, 600, bal.BoosterTotal)
	assert.EqualValues(t, 300, bal.BoosterRemain)
	assert.Nil(t, bal.RemainingRuns, "credits mode must not set RemainingRuns")
}

// --- Task 4: Budget-aware credit reservation API ---

// newPureCreditsUser returns a user whose billing_mode=credits and UserTier=free,
// so HasActiveMembership()=false and isEffectiveLegacy()=false. This ensures the
// credits path (not the legacy path) is taken by CheckAndEstimateBudget /
// ReserveBudget tests.
func newPureCreditsUser(id uint) *model.User {
	u := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierFree, // no active membership → not effective-legacy
	}
	u.ID = id
	return u
}

// TestCheckAndEstimateBudget_NormalizesSopNodeExecuteToSopRun verifies that
// CheckAndEstimateBudget normalizes the raw operation "sop_node_execute" to
// OpSopRun and returns a successful precheck result for a user with sufficient
// balance. Spec §6.1.1.
func TestCheckAndEstimateBudget_NormalizesSopNodeExecuteToSopRun(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(700)
	user := newPureCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 2000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	result, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		UserID:                    userID,
		Operation:                 "sop_node_execute",
		EstimatedPromptTokens:     1000,
		EstimatedCompletionTokens: 200,
		Provider:                  "volc",
		Model:                     "glm-4-7-251222",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	// The result carries estimated credits > 0 because the operation normalized to OpSopRun.
	// Legacy-only credits path: EstimatedCredits should reflect some positive estimate.
	assert.False(t, result.SkipDeduction, "credits-mode user must NOT skip deduction")
	assert.True(t, result.Sufficient, "user with 2000 credits must be sufficient for sop_node_execute")
	assert.Greater(t, result.EstimatedCredits, int64(0), "normalized to OpSopRun → credits > 0")
}

// TestReserveBudget_WritesContextBudgetMetadata verifies that ReserveBudget
// writes a credit_reservation row with estimation_source='context_budget',
// coefficient_id=NULL, and the supplied token profile / event id fields.
// Spec §6.1.2.
func TestReserveBudget_WritesContextBudgetMetadata(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(701)
	user := newPureCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 2000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	rsv, err := svc.ReserveBudget(context.Background(), user, credit.BudgetReservationInput{
		BudgetPrecheckInput: credit.BudgetPrecheckInput{
			UserID:                    userID,
			Operation:                 "sop_node_execute",
			EstimatedPromptTokens:     1000,
			EstimatedCompletionTokens: 200,
			Provider:                  "volc",
			Model:                     "glm-4-7-251222",
			TokenProfileID:            7,
			ContextBudgetEventID:      42,
		},
		EstimatedCredits: 50,
		IdempotencyKey:   "budget:test:701:1",
	})
	require.NoError(t, err)
	require.NotNil(t, rsv, "ReserveBudget must return a non-nil reservation for credits-mode user")

	// Read back the DB row and verify context_budget metadata.
	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)

	assert.Equal(t, "context_budget", dbRsv.EstimationSource, "estimation_source must be 'context_budget'")
	assert.Nil(t, dbRsv.CoefficientID, "coefficient_id must be NULL for context_budget reservations")
	require.NotNil(t, dbRsv.TokenProfileID)
	assert.EqualValues(t, 7, *dbRsv.TokenProfileID, "token_profile_id must match input")
	assert.Greater(t, dbRsv.EstimatedPromptTokens, 0, "estimated_prompt_tokens must be persisted")
	assert.Greater(t, dbRsv.EstimatedCompletionTokens, 0, "estimated_completion_tokens must be persisted")
	assert.NotEmpty(t, dbRsv.Provider, "provider must be persisted")
	assert.NotEmpty(t, dbRsv.Model, "model must be persisted")
	require.NotNil(t, dbRsv.ContextBudgetEventID)
	assert.EqualValues(t, 42, *dbRsv.ContextBudgetEventID, "context_budget_event_id must match input")
	// user_type_multiplier must default to 1.0 (user has subscription, no discount).
	assert.InDelta(t, 1.0, dbRsv.UserTypeMultiplier, 0.001, "user_type_multiplier must be 1.0 for subscription user")
}

// TestCheckAndEstimateBudget_LegacyTierSkipsReserve verifies that a user with
// BillingMode=legacy_tier and an active membership receives SkipDeduction=true
// and no credit_reservation row is created. Spec §6.1.1 + §1.3 contract.
func TestCheckAndEstimateBudget_LegacyTierSkipsReserve(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	future := time.Now().Add(30 * 24 * time.Hour)
	user := &model.User{
		BillingMode: model.BillingModeLegacyTier,
		UserTier:    model.UserTierStandard,
		TierExpires: &future,
	}
	user.ID = 702

	result, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		UserID:                    702,
		Operation:                 "sop_node_execute",
		EstimatedPromptTokens:     500,
		EstimatedCompletionTokens: 100,
		Provider:                  "volc",
		Model:                     "glm-4-7-251222",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SkipDeduction, "legacy_tier user must have SkipDeduction=true")

	// No reservation row should have been created.
	var count int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Where("user_id = ?", uint(702)).Count(&count).Error)
	assert.EqualValues(t, 0, count, "CheckAndEstimateBudget must NOT create a reservation for legacy_tier")
}

// TestCheckAndEstimateBudget_UnknownChargedOperationFailsClosed verifies that
// when a credits-mode user submits an unknown operation that cannot be
// normalized, CheckAndEstimateBudget returns ErrUnknownBudgetOperation (typed)
// rather than silently billing via a default operation. Spec §6.1.1.
func TestCheckAndEstimateBudget_UnknownChargedOperationFailsClosed(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(703)
	user := newPureCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 2000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	initialBalance := int64(2000)

	_, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		UserID:                    userID,
		Operation:                 "some_unknown_op",
		EstimatedPromptTokens:     500,
		EstimatedCompletionTokens: 100,
		Provider:                  "volc",
		Model:                     "glm-4-7-251222",
	})
	require.Error(t, err, "unknown operation must return an error")
	require.True(t, errors.Is(err, credit.ErrUnknownBudgetOperation),
		"expected ErrUnknownBudgetOperation, got %v", err)

	// User balance must not have changed.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, initialBalance, acc.Balance, "unknown operation must not modify user balance")
}

// --- User-type credit multiplier tests ---

// TestReserve_TrialUserMultiplierApplied verifies that a user with an active trial
// package (no subscription) has credits reserved at the configured 0.5× rate.
// The snapshot on credit_reservation.user_type_multiplier must equal 0.5, and
// reserved_credits must equal round(estimated * 0.5).
func TestReserve_TrialUserMultiplierApplied(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(800)
	user := newCreditsUser(userID)
	now := time.Now()

	// Seed a trial package only (no subscription).
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeTrial, TotalCredits: 200, RemainCredits: 200,
			ActivatedAt: now, ExpiresAt: now.Add(72 * time.Hour)},
	})

	// Seed the trial multiplier config (0.5×).
	require.NoError(t, db.Create(&model.CreditUserTypeConfig{
		UserType:         "trial",
		CreditMultiplier: 0.5,
		Description:      "test: trial users burn at half rate",
		IsActive:         true,
	}).Error)

	estimated := int64(100)
	idemp := "test:trial_multiplier:800"
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, estimated, 0, &idemp)
	require.NoError(t, err)
	require.NotNil(t, rsv)

	// Core invariant: reserved_credits = round(100 * 0.5) = 50.
	assert.EqualValues(t, 50, rsv.ReservedCredits,
		"trial user should have reserved_credits = round(estimated * 0.5)")

	// DB snapshot must carry the multiplier so Reconcile is consistent.
	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)
	assert.EqualValues(t, 50, dbRsv.ReservedCredits)
	assert.InDelta(t, 0.5, dbRsv.UserTypeMultiplier, 0.001, "snapshot multiplier must be 0.5")
}

// TestReserve_SubscriptionUserBypassesTrialMultiplier verifies that a user with
// both an active subscription and a trial package gets multiplier 1.0 (subscription
// takes precedence and the trial discount is suppressed).
func TestReserve_SubscriptionUserBypassesTrialMultiplier(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(801)
	user := newCreditsUser(userID)
	now := time.Now()

	// User has both subscription and trial in credit_package (legacy read path).
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 2000,
			ActivatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)},
		{Type: model.CreditTypeTrial, TotalCredits: 200, RemainCredits: 100,
			ActivatedAt: now, ExpiresAt: now.Add(72 * time.Hour)},
	})
	// T4: GetUserTypeCreditMultiplier now reads the new subscription table to detect
	// active subscriptions. Seed the new table so the "subscription bypasses trial
	// discount" invariant is correctly detected.
	require.NoError(t, db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at, total_months_purchased, source, created_at, updated_at)
         VALUES (?, ?, ?, ?, 1, 'b2b_grant', ?, ?)`,
		userID, now, now, now.Add(30*24*time.Hour), now, now,
	).Error)
	require.NoError(t, db.Create(&model.CreditUserTypeConfig{
		UserType: "trial", CreditMultiplier: 0.5, IsActive: true,
	}).Error)

	idemp := "test:sub_bypasses_trial:801"
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 100, 0, &idemp)
	require.NoError(t, err)

	// Subscription takes precedence: multiplier = 1.0, reserved = 100.
	assert.EqualValues(t, 100, rsv.ReservedCredits,
		"subscription user must not receive trial discount")

	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)
	assert.InDelta(t, 1.0, dbRsv.UserTypeMultiplier, 0.001)
}

// TestReserve_ExpiredTrialPackageGetsNoDiscount verifies that a user whose trial
// package has expired is treated as a normal user (multiplier = 1.0).
func TestReserve_ExpiredTrialPackageGetsNoDiscount(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	userID := uint(802)
	user := newCreditsUser(userID)
	now := time.Now()

	// Expired trial package (status is 'expired').
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 2000,
			ActivatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)},
	})
	// Also add an expired trial manually (status='expired', ExpiresAt in the past).
	expiredPkg := model.CreditPackage{
		UserID: userID, Type: model.CreditTypeTrial, TotalCredits: 200,
		RemainCredits: 0, Status: model.CreditPackageExpired,
		ActivatedAt: now.Add(-5 * 24 * time.Hour), ExpiresAt: now.Add(-2 * 24 * time.Hour),
	}
	require.NoError(t, db.Create(&expiredPkg).Error)
	require.NoError(t, db.Create(&model.CreditUserTypeConfig{
		UserType: "trial", CreditMultiplier: 0.5, IsActive: true,
	}).Error)

	idemp := "test:expired_trial:802"
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 100, 0, &idemp)
	require.NoError(t, err)

	// Expired trial: no discount, multiplier = 1.0.
	assert.EqualValues(t, 100, rsv.ReservedCredits)
	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)
	assert.InDelta(t, 1.0, dbRsv.UserTypeMultiplier, 0.001)
}
