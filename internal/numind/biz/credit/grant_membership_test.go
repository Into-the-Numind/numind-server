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

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newGrantTestDB creates an isolated SQLite DB with the schema needed for
// GrantMembership tests (T4 rewrite):
//   - user, action_log, credit_account (legacy tables, still used by GrantMembership)
//   - subscription, trial_grant, membership_event (new tables written by MembershipService)
//   - credit_package retained for HasActiveSubscription/HasTrialPackage guard tests that
//     seed pre-existing state in the NEW tables (credit_package is no longer written, but
//     the store uses subscription/trial_grant tables now)
//
// Raw DDL is used because GORM `type:enum(...)` tags are MySQL-specific and
// cause AutoMigrate to fail on SQLite.
func newGrantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/grant_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite file DB")

	// Legacy tables (still referenced by GrantMembership side-effects).
	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            phone           TEXT,
            nickname        TEXT,
            avatar_url      TEXT,
            parent_user_id  INTEGER,
            total_sop_runs  INTEGER DEFAULT 0,
            monthly_sop_runs INTEGER DEFAULT 0,
            monthly_reset_at DATETIME,
            user_tier       TEXT DEFAULT 'free',
            tier_expires    DATETIME,
            billing_mode    TEXT NOT NULL DEFAULT 'credits',
            username        TEXT,
            password        TEXT,
            is_admin        INTEGER DEFAULT 0,
            status          INTEGER DEFAULT 0,
            last_login      DATETIME
        )`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditTransaction{},
		&model.TierChangeLog{},
		&model.ActionLogM{},
	), "auto-migrate legacy tables")

	// New membership tables (written by MembershipService, read by guard readers).
	ddl := []string{
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
		`CREATE INDEX IF NOT EXISTS idx_sub_expires_at ON subscription (expires_at)`,
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
		`CREATE INDEX IF NOT EXISTS idx_trial_expires_at ON trial_grant (expires_at)`,
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
		`CREATE INDEX IF NOT EXISTS idx_event_user_occurred ON membership_event (user_id, occurred_at)`,
	}
	for _, stmt := range ddl {
		require.NoError(t, db.Exec(stmt).Error, "DDL: %.60s", stmt)
	}

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newGrantTestBiz creates a creditBiz + MembershipService wired together,
// backed by the given SQLite DB.
func newGrantTestBiz(t *testing.T, db *gorm.DB) ICreditBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds)
	svc := membership.NewMembershipService(db)
	InjectCreditBizMembershipSvc(b, svc)
	return b
}

// insertGrantTestUser inserts a user with the given tier/parent/billing_mode and returns the ID.
func insertGrantTestUser(t *testing.T, db *gorm.DB, tier string, parentID *uint, billingMode string, tierExpires *time.Time) uint {
	t.Helper()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, user_tier, tier_expires, billing_mode, parent_user_id, monthly_sop_runs)
         VALUES (?, ?, ?, ?, ?, ?, 0)`,
		time.Now(), time.Now(), tier, tierExpires, billingMode, parentVal,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// ---------- GrantMembership: 父子关系鉴权 ----------

func TestGrantMembership_ChildNotBelongingToParent_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parentA := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	parentB := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	// childOfB belongs to parentB, not parentA
	childOfB := insertGrantTestUser(t, db, model.UserTierFree, &parentB, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parentA,
		ChildUserID:  childOfB,
		ProductType:  model.ProductTypeTrial,
		Reason:       "cross-tenant attempt",
	})
	require.Error(t, err, "parent A must not grant to child of parent B")
}

func TestGrantMembership_ChildNotExists_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  99999,
		ProductType:  model.ProductTypeTrial,
		Reason:       "nonexistent child",
	})
	require.Error(t, err, "nonexistent child must be rejected")
}

// ---------- GrantMembership: Trial path (writes trial_grant new table) ----------

func TestGrantMembership_Trial_Success(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "trial granted",
	})
	require.NoError(t, err)

	// T4: verify trial_grant row created (not credit_package)
	var trialRows []struct {
		ID               int64
		UserID           int64
		CreditsRemaining int
		Source           string
		GranterUserID    *int64
	}
	require.NoError(t, db.Raw(
		`SELECT id, user_id, credits_remaining, source, granter_user_id FROM trial_grant WHERE user_id = ?`, child,
	).Scan(&trialRows).Error)
	require.Len(t, trialRows, 1, "trial_grant must have exactly 1 row")
	tg := trialRows[0]
	assert.EqualValues(t, 200, tg.CreditsRemaining)
	assert.Equal(t, "b2b_grant", tg.Source)
	require.NotNil(t, tg.GranterUserID)
	assert.EqualValues(t, parent, *tg.GranterUserID)

	// membership_event written
	var evtCount int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM membership_event WHERE user_id = ? AND event_type = 'trial_granted'`, child,
	).Scan(&evtCount).Error)
	assert.EqualValues(t, 1, evtCount, "trial_granted event must be written")

	// Action log written
	var logs []model.ActionLogM
	require.NoError(t, db.Where("user_id = ? AND action = ?", parent, "grant_membership").Find(&logs).Error)
	require.Len(t, logs, 1, "grant_membership action log must be written")
	require.NotNil(t, logs[0].TargetID)
	assert.Equal(t, child, *logs[0].TargetID)

	// credit_package must NOT be written (T4: old path removed)
	var cpCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='credit_package'`).Scan(&cpCount).Error)
	// credit_package table may not exist at all in the new schema, which is fine.
	// If it does exist (e.g. during transition), it must have 0 rows.
	if cpCount > 0 {
		var rows int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM credit_package WHERE user_id = ?", child).Scan(&rows).Error)
		assert.Zero(t, rows, "credit_package must not be written by T4 grant path")
	}
}

// ---------- Trial UNIQUE protection (second grant rejected) ----------

func TestGrantMembership_TrialLifetimeUnique_SecondGrantRejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// First grant: must succeed
	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "first trial",
	})
	require.NoError(t, err, "first trial grant must succeed")

	// Second grant: must be rejected (trial is lifetime-unique)
	err = b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "duplicate trial attempt",
	})
	require.Error(t, err, "second trial grant must be rejected")
	assert.ErrorIs(t, err, ErrGrantTrialAlreadyPurchased, "must return ErrGrantTrialAlreadyPurchased")

	// trial_grant still has exactly 1 row (no duplicate write)
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM trial_grant WHERE user_id = ?`, child).Scan(&count).Error)
	assert.EqualValues(t, 1, count, "trial_grant must still have exactly 1 row")
}

func TestGrantMembership_ChildAlreadyHasTrial_TrialRejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed trial_grant row directly (simulates exhausted trial from previous session)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO trial_grant (user_id, granted_at, expires_at, credits_remaining, source, created_at)
         VALUES (?, ?, ?, 0, 'b2b_grant', ?)`,
		child, now.Add(-10*24*time.Hour), now.Add(-7*24*time.Hour), now,
	).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
	})
	require.Error(t, err, "trial can only be granted once per child (lifetime)")
	assert.ErrorIs(t, err, ErrGrantTrialAlreadyPurchased)
}

// ---------- GrantMembership: Subscription path (writes subscription new table) ----------

func TestGrantMembership_Monthly_OneMonth_CreatesSubscription(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "1-month grant",
	})
	require.NoError(t, err)

	// T4: subscription row created
	var subs []struct {
		ID                   int64
		UserID               int64
		TotalMonthsPurchased int
		Source               string
		GranterUserID        *int64
	}
	require.NoError(t, db.Raw(
		`SELECT id, user_id, total_months_purchased, source, granter_user_id FROM subscription WHERE user_id = ?`, child,
	).Scan(&subs).Error)
	require.Len(t, subs, 1, "subscription must have exactly 1 row for 1-month grant")
	sub := subs[0]
	assert.Equal(t, 1, sub.TotalMonthsPurchased)
	assert.Equal(t, "b2b_grant", sub.Source)
	require.NotNil(t, sub.GranterUserID)
	assert.EqualValues(t, parent, *sub.GranterUserID)

	// membership_event written
	var evtCount int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM membership_event WHERE user_id = ? AND event_type = 'sub_granted'`, child,
	).Scan(&evtCount).Error)
	assert.EqualValues(t, 1, evtCount)

	// Action log written
	var logs []model.ActionLogM
	require.NoError(t, db.Where("user_id = ? AND action = ?", parent, "grant_membership").Find(&logs).Error)
	require.Len(t, logs, 1)
}

func TestGrantMembership_Monthly_TwelveMonths_CreatesSubscription(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       12,
		Reason:       "12-month grant",
	})
	require.NoError(t, err)

	// T4: single subscription row with total_months_purchased=12
	var totalMonths int
	require.NoError(t, db.Raw(
		`SELECT total_months_purchased FROM subscription WHERE user_id = ?`, child,
	).Scan(&totalMonths).Error)
	assert.Equal(t, 12, totalMonths, "12-month grant: total_months_purchased must be 12")
}

func TestGrantMembership_Monthly_InvalidMonths_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	for _, months := range []int{0, -1, 13, 999} {
		err := b.GrantMembership(context.Background(), GrantMembershipReq{
			ParentUserID: parent,
			ChildUserID:  child,
			ProductType:  model.ProductTypeMonthly,
			Months:       months,
		})
		require.Error(t, err, "months=%d should reject", months)
		assert.ErrorIs(t, err, ErrGrantInvalidMonths, "months=%d", months)
	}
}

// ---------- Guard reader: HasActiveSubscription reads subscription table ----------

func TestGrantMembership_ChildAlreadyHasActiveSubscription_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed subscription in the NEW table (not credit_package)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at, total_months_purchased, source, created_at, updated_at)
         VALUES (?, ?, ?, ?, 1, 'b2b_grant', ?, ?)`,
		child, now, now, now.AddDate(0, 1, 0), now, now,
	).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
	})
	require.Error(t, err, "child already has active subscription → reject repeat grant")
	assert.ErrorIs(t, err, ErrGrantActiveSubscription)
}

func TestGrantMembership_ExpiredSubscription_AllowsNewGrant(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed an EXPIRED subscription in the NEW table
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at, total_months_purchased, source, created_at, updated_at)
         VALUES (?, ?, ?, ?, 1, 'b2b_grant', ?, ?)`,
		child, now.AddDate(0, -2, 0), now.AddDate(0, -2, 0), now.AddDate(0, -1, 0), now, now,
	).Error)

	// HasActiveSubscription should return false (expired) → grant must succeed (reopen scenario)
	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
	})
	require.NoError(t, err, "expired subscription must allow new grant (reopen scenario)")
}

func TestGrantMembership_ActiveSubscriptionBlocksTrial(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed active subscription (blocks trial per spec §3.9)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at, total_months_purchased, source, created_at, updated_at)
         VALUES (?, ?, ?, ?, 1, 'b2b_grant', ?, ?)`,
		child, now, now, now.AddDate(0, 1, 0), now, now,
	).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
	})
	require.Error(t, err, "active subscription must block trial grant")
	assert.ErrorIs(t, err, ErrGrantActiveSubscription)
}

// ---------- GrantMembership: billing_mode switch legacy→credits ----------

func TestGrantMembership_SwitchesBillingModeLegacyToCredits(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	// Child starts in legacy_tier mode
	pastExpires := time.Now().Add(-24 * time.Hour)
	child := insertGrantTestUser(t, db, model.UserTierStandard, &parent, model.BillingModeLegacyTier, &pastExpires)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
	})
	require.NoError(t, err)

	// billing_mode must be 'credits' now
	var bm string
	require.NoError(t, db.Raw(`SELECT billing_mode FROM user WHERE id = ?`, child).Scan(&bm).Error)
	assert.Equal(t, model.BillingModeCredits, bm, "grant should switch legacy_tier → credits")
}

// ---------- GrantMembership: ProductType validation ----------

func TestGrantMembership_UnsupportedProductType_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// yearly is not supported by grant (reserved for future); booster is self_purchase only
	for _, pt := range []string{model.ProductTypeYearly, model.ProductTypeBooster, "garbage"} {
		err := b.GrantMembership(context.Background(), GrantMembershipReq{
			ParentUserID: parent,
			ChildUserID:  child,
			ProductType:  pt,
			Months:       1,
		})
		require.Error(t, err, "productType=%s must be rejected", pt)
		assert.ErrorIs(t, err, ErrGrantInvalidProductType, "productType=%s", pt)
	}
}

// ---------- Self-Grant: Trial path ----------

func TestGrantMembership_SelfGrant_Trial_Success(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeTrial,
		Reason:       "self trial",
	})
	require.NoError(t, err, "parent self-granting trial must succeed")

	// T4: verify trial_grant row (not credit_package)
	var tgCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM trial_grant WHERE user_id = ?`, parent).Scan(&tgCount).Error)
	assert.EqualValues(t, 1, tgCount, "trial_grant row must be created for self-grant")

	// Action log target_id == parent (self-grant)
	var logs []model.ActionLogM
	require.NoError(t, db.Where("user_id = ? AND action = ?", parent, "grant_membership").Find(&logs).Error)
	require.Len(t, logs, 1)
	require.NotNil(t, logs[0].TargetID)
	assert.Equal(t, parent, *logs[0].TargetID, "self-grant: target_id == user_id")
}

func TestGrantMembership_SelfGrant_Monthly_Success(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeMonthly,
		Months:       3,
		Reason:       "self monthly 3m",
	})
	require.NoError(t, err)

	// T4: single subscription row with total_months_purchased=3
	var totalMonths int
	require.NoError(t, db.Raw(
		`SELECT total_months_purchased FROM subscription WHERE user_id = ?`, parent,
	).Scan(&totalMonths).Error)
	assert.Equal(t, 3, totalMonths, "self-grant 3-month: total_months_purchased must be 3")
}

// ---------- Self-Grant: 越权防线 ----------

func TestGrantMembership_SubUserSelfGrant_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: child,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "sub self-grant attempt",
	})
	require.Error(t, err, "sub-user self-grant must be rejected")
	assert.ErrorIs(t, err, ErrGrantForbidden, "must return ErrGrantForbidden")

	// No trial_grant row written
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM trial_grant WHERE user_id = ?`, child).Scan(&count).Error)
	assert.EqualValues(t, 0, count, "no trial_grant row written for rejected sub-user self-grant")
}

// TestGrantMembership_SubUserSelfGrant_Monthly_Rejected verifies the C-end
// self-purchase guard at the **biz layer**: a sub-user (parent_user_id != nil)
// on billing_mode=credits cannot self-grant a monthly subscription.
//
// The biz-layer equivalent of errno.ErrMembershipSelfPurchaseDisabled is
// ErrGrantForbidden — see GrantMembership.Step 2 (req.ChildUserID == req.ParentUserID
// path). The MembershipService's deeper ErrMembershipSelfPurchaseDisabled guard is
// also present but the biz layer rejects earlier with ErrGrantForbidden.
// This test pins down the boundary.
func TestGrantMembership_SubUserSelfGrant_Monthly_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	// Sub-user: parent_user_id = parent, billing_mode=credits
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: child, // caller == target, but caller is a sub-user
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "C-end self-purchase attempt (monthly)",
	})
	require.Error(t, err, "C-end sub-user must not self-grant monthly subscription")
	assert.ErrorIs(t, err, ErrGrantForbidden,
		"biz-layer rejection is ErrGrantForbidden (equivalent of errno.ErrMembershipSelfPurchaseDisabled)")

	// No subscription written
	var subCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM subscription WHERE user_id = ?`, child).Scan(&subCount).Error)
	assert.EqualValues(t, 0, subCount, "no subscription row may be written for rejected C-end self-grant")

	// No action_log written either
	var logCount int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM action_log WHERE user_id = ? AND action = 'grant_membership'`, child,
	).Scan(&logCount).Error)
	assert.EqualValues(t, 0, logCount, "no action_log row may be written for rejected C-end self-grant")
}

func TestGrantMembership_CrossParentGrant_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parentA := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	parentB := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parentA,
		ChildUserID:  parentB,
		ProductType:  model.ProductTypeTrial,
		Reason:       "cross-parent attempt",
	})
	require.Error(t, err, "parent A must not grant to parent B (both are parent accounts)")
	assert.ErrorIs(t, err, ErrGrantForbidden)

	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM trial_grant WHERE user_id = ?`, parentB).Scan(&count).Error)
	assert.EqualValues(t, 0, count)
}

// ---------- Self-Grant: billing_mode switch ----------

func TestGrantMembership_SelfGrant_BillingModeSwitch(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeLegacyTier, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeTrial,
		Reason:       "legacy→credits switch on self-grant",
	})
	require.NoError(t, err)

	var bm string
	require.NoError(t, db.Raw("SELECT billing_mode FROM user WHERE id = ?", parent).Scan(&bm).Error)
	assert.Equal(t, model.BillingModeCredits, bm, "billing_mode must switch legacy_tier → credits")
}

// ---------- Self-Grant: 防重复 ----------

func TestGrantMembership_SelfGrant_TrialAlreadyPurchased_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	// Seed trial_grant row (expired, but lifetime check still applies)
	require.NoError(t, db.Exec(
		`INSERT INTO trial_grant (user_id, granted_at, expires_at, credits_remaining, source, created_at)
         VALUES (?, ?, ?, 0, 'b2b_grant', ?)`,
		parent,
		time.Now().Add(-10*24*time.Hour),
		time.Now().Add(-7*24*time.Hour),
		time.Now(),
	).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeTrial,
		Reason:       "duplicate trial attempt",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantTrialAlreadyPurchased)
}

func TestGrantMembership_SelfGrant_ActiveSubscription_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	// Seed active subscription in the new table
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at, total_months_purchased, source, created_at, updated_at)
         VALUES (?, ?, ?, ?, 1, 'b2b_grant', ?, ?)`,
		parent, now.Add(-5*24*time.Hour), now.Add(-5*24*time.Hour), now.Add(25*24*time.Hour), now, now,
	).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "duplicate monthly attempt",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantActiveSubscription)
}

// ---------- Idempotency replay: guard detects existing rows on retry ----------
// Note: GrantMembership is NOT idempotent at the biz level — it always rejects
// duplicates (via guard readers reading subscription/trial_grant). True idempotency
// (same network retry within window) is handled at HTTP/controller layer through
// the Idempotency-Key middleware. These tests verify the biz-layer guard semantics.

// TestGrantMembership_TrialGuardDetectsExistingRow_AfterRetry verifies that a
// second GrantMembership call for the same child is **rejected** by the trial
// lifetime guard (HasTrialPackage reads trial_grant new table). The name reflects
// the actual assertion: it's a guard-detection test, NOT an idempotent-success
// test. trial_grant table maintains exactly 1 row across both calls (UNIQUE
// constraint on user_id also acts as a backstop).
func TestGrantMembership_TrialGuardDetectsExistingRow_AfterRetry(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// First call: trial granted
	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "first",
	})
	require.NoError(t, err)

	// At this point trial_grant has exactly one row. The second call (retry
	// simulation) must be rejected — at the biz level a second grant is a data
	// integrity error, not silent success. Controller layer must not retry after 409.
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM trial_grant WHERE user_id = ?`, child).Scan(&count).Error)
	assert.EqualValues(t, 1, count, "exactly one trial_grant row must exist after first call")

	err = b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "retry",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantTrialAlreadyPurchased)
}

func TestGrantMembership_IdempotencyReplay_SubscriptionRenewal(t *testing.T) {
	db := newGrantTestDB(t)
	b := newGrantTestBiz(t, db)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// First call: subscription granted
	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "first",
	})
	require.NoError(t, err)

	// subscription row exists now; second call is rejected (active subscription guard)
	err = b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "retry",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantActiveSubscription,
		"second subscription grant must be rejected by active subscription guard")

	// subscription table still has exactly 1 row (no duplicate INSERT)
	var subCount int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM subscription WHERE user_id = ?`, child).Scan(&subCount).Error)
	assert.EqualValues(t, 1, subCount, "subscription table must have exactly 1 row")
}
