package credit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newCronBillingTestDB creates an isolated SQLite DB with the minimal schema
// for reconcileBillingMode tests (user + credit_package). The user table is
// hand-rolled because its GORM `type:enum(...)` tag is MySQL-specific.
func newCronBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            user_tier       TEXT DEFAULT 'free',
            tier_expires    DATETIME,
            billing_mode    TEXT NOT NULL DEFAULT 'credits'
        )`).Error)

	require.NoError(t, db.AutoMigrate(&model.CreditPackage{}), "auto-migrate credit_package")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func insertUser(t *testing.T, db *gorm.DB, billingMode string) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, user_tier, billing_mode) VALUES (?, ?, 'standard', ?)`,
		time.Now(), time.Now(), billingMode,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertCreditPackage(t *testing.T, db *gorm.DB, userID uint, pkgType, status string) {
	t.Helper()
	now := time.Now()
	pkg := &model.CreditPackage{
		UserID:        userID,
		Type:          pkgType,
		TotalCredits:  2000,
		RemainCredits: 2000,
		ActivatedAt:   now,
		ExpiresAt:     now.AddDate(0, 1, 0),
		Status:        status,
	}
	require.NoError(t, db.Create(pkg).Error)
}

func readUserBillingMode(t *testing.T, db *gorm.DB, userID uint) string {
	t.Helper()
	var mode string
	require.NoError(t, db.Raw("SELECT billing_mode FROM user WHERE id = ?", userID).Scan(&mode).Error)
	return mode
}

// TestReconcileBillingMode_LegacyTierWithActiveSubscription_Switched exercises
// the primary fallback path: a legacy_tier user who has an active subscription
// package (e.g., the fulfillOrder switch dropped a warn-log during the original
// order) must be flipped to credits on the next cron run.
func TestReconcileBillingMode_LegacyTierWithActiveSubscription_Switched(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uid, model.CreditTypeSubscription, model.CreditPackageActive)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uid),
		"legacy_tier user with active subscription should be switched to credits")
}

// A pending subscription (activated_at in the future) also qualifies —
// the user has already paid, they just haven't reached their first cycle yet.
func TestReconcileBillingMode_LegacyTierWithPendingSubscription_Switched(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uid, model.CreditTypeSubscription, model.CreditPackagePending)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uid))
}

// A legacy_tier user with no credit package at all must remain legacy_tier.
// (They are genuine legacy users who never upgraded.)
func TestReconcileBillingMode_LegacyTierWithoutSubscription_Unchanged(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeLegacyTier)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeLegacyTier, readUserBillingMode(t, db, uid),
		"legacy_tier user with no subscription package must remain legacy_tier")
}

// A legacy_tier user with an exhausted/expired subscription should NOT be
// switched — they previously had membership but it's no longer active.
func TestReconcileBillingMode_LegacyTierWithExhaustedSubscription_Unchanged(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uid, model.CreditTypeSubscription, model.CreditPackageExhausted)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeLegacyTier, readUserBillingMode(t, db, uid),
		"legacy_tier user with exhausted subscription must remain legacy_tier")
}

// A legacy_tier user with only a trial or booster package (not subscription)
// should not be switched — trial doesn't convey membership status, and booster
// is gated by membership in the first place.
func TestReconcileBillingMode_LegacyTierWithOnlyTrial_Unchanged(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uid, model.CreditTypeTrial, model.CreditPackageActive)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeLegacyTier, readUserBillingMode(t, db, uid),
		"legacy_tier user with only trial package must remain legacy_tier")
}

// Users already on credits must be left alone (idempotence).
func TestReconcileBillingMode_AlreadyCredits_Idempotent(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	uid := insertUser(t, db, model.BillingModeCredits)
	insertCreditPackage(t, db, uid, model.CreditTypeSubscription, model.CreditPackageActive)

	// Run twice: second run is a no-op but must not error.
	require.NoError(t, b.reconcileBillingMode(context.Background()))
	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uid))
}

// Multiple users: only the legacy_tier + subscription ones should move.
func TestReconcileBillingMode_MixedCohort_OnlyTargetedUsersSwitched(t *testing.T) {
	db := newCronBillingTestDB(t)
	ds := store.NewTestStore(db)
	b := &creditBiz{ds: ds}

	// user A: legacy_tier + active subscription → should switch
	uidA := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uidA, model.CreditTypeSubscription, model.CreditPackageActive)

	// user B: legacy_tier + no subscription → unchanged
	uidB := insertUser(t, db, model.BillingModeLegacyTier)

	// user C: credits + active subscription → unchanged
	uidC := insertUser(t, db, model.BillingModeCredits)
	insertCreditPackage(t, db, uidC, model.CreditTypeSubscription, model.CreditPackageActive)

	// user D: legacy_tier + pending subscription → should switch
	uidD := insertUser(t, db, model.BillingModeLegacyTier)
	insertCreditPackage(t, db, uidD, model.CreditTypeSubscription, model.CreditPackagePending)

	require.NoError(t, b.reconcileBillingMode(context.Background()))

	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uidA))
	assert.Equal(t, model.BillingModeLegacyTier, readUserBillingMode(t, db, uidB))
	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uidC))
	assert.Equal(t, model.BillingModeCredits, readUserBillingMode(t, db, uidD))
}
