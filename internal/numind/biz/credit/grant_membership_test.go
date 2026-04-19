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

// newGrantTestDB creates an isolated SQLite DB with the minimal schema needed
// for GrantMembership tests: user + action_log + credit_account + credit_package
// + credit_transaction + tier_change_log.
//
// The user table is hand-rolled because its GORM `type:enum(...)` tag for
// billing_mode is MySQL-specific.
func newGrantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/grant_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite file DB")

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
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.TierChangeLog{},
		&model.ActionLogM{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
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

// ---------- Q1.2 GrantMembership: 父子关系鉴权 ----------

func TestGrantMembership_ChildNotBelongingToParent_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

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
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  99999,
		ProductType:  model.ProductTypeTrial,
		Reason:       "nonexistent child",
	})
	require.Error(t, err, "nonexistent child must be rejected")
}

// ---------- Q1.2 GrantMembership: Trial path ----------

func TestGrantMembership_Trial_Success(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
		Reason:       "trial granted",
	})
	require.NoError(t, err)

	// Verify credit_package: trial, 200 credits, 3 days, grant_source=b2b_grant, granter=parent
	var pkgs []model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", child).Find(&pkgs).Error)
	require.Len(t, pkgs, 1, "trial creates exactly 1 package")
	p := pkgs[0]
	assert.Equal(t, model.CreditTypeTrial, p.Type)
	assert.EqualValues(t, 200, p.TotalCredits)
	assert.EqualValues(t, 200, p.RemainCredits)
	assert.Equal(t, model.CreditPackageActive, p.Status)
	assert.Equal(t, model.GrantSourceB2BGrant, p.GrantSource)
	require.NotNil(t, p.GranterUserID, "b2b_grant must fill granter_user_id")
	assert.Equal(t, parent, *p.GranterUserID)
	// ExpiresAt ≈ activated + 3 days
	approxExpires := p.ActivatedAt.Add(3 * 24 * time.Hour)
	assert.WithinDuration(t, approxExpires, p.ExpiresAt, 2*time.Second)

	// Balance updated
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", child).First(&acc).Error)
	assert.EqualValues(t, 200, acc.Balance)

	// Action log written
	var logs []model.ActionLogM
	require.NoError(t, db.Where("user_id = ? AND action = ?", parent, "grant_membership").Find(&logs).Error)
	require.Len(t, logs, 1, "grant_membership action log must be written")
	require.NotNil(t, logs[0].TargetID)
	assert.Equal(t, child, *logs[0].TargetID)
}

// ---------- Q1.2 GrantMembership: Monthly path ----------

func TestGrantMembership_Monthly_ThreeMonths_CreatesThreePackages(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       3,
		Reason:       "3-month grant",
	})
	require.NoError(t, err)

	var pkgs []model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", child).Order("activated_at ASC").Find(&pkgs).Error)
	require.Len(t, pkgs, 3, "monthly with months=3 creates 3 packages")

	// First is active, rest pending
	assert.Equal(t, model.CreditPackageActive, pkgs[0].Status)
	assert.Equal(t, model.CreditPackagePending, pkgs[1].Status)
	assert.Equal(t, model.CreditPackagePending, pkgs[2].Status)

	// All are subscription type, 2000 credits each, grant_source=b2b_grant, granter=parent
	for i, p := range pkgs {
		assert.Equal(t, model.CreditTypeSubscription, p.Type, "pkg %d", i)
		assert.EqualValues(t, 2000, p.TotalCredits, "pkg %d", i)
		assert.Equal(t, model.GrantSourceB2BGrant, p.GrantSource, "pkg %d", i)
		require.NotNil(t, p.GranterUserID, "pkg %d missing granter", i)
		assert.Equal(t, parent, *p.GranterUserID, "pkg %d", i)
		// Q1 grant does NOT go through Order (no payment flow) → OrderID must be nil
		assert.Nil(t, p.OrderID, "b2b_grant package %d must not reference an Order", i)
	}

	// Balance = 2000 (only first package active)
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", child).First(&acc).Error)
	assert.EqualValues(t, 2000, acc.Balance)
}

func TestGrantMembership_Monthly_InvalidMonths_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

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
	}
}

// ---------- Q1.2 GrantMembership: 防重复开通同级/更高在期会员 ----------

func TestGrantMembership_ChildAlreadyHasActiveSubscription_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed an existing active subscription package on child
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{
		UserID:        child,
		Type:          model.CreditTypeSubscription,
		TotalCredits:  2000,
		RemainCredits: 2000,
		ActivatedAt:   now,
		ExpiresAt:     now.AddDate(0, 1, 0),
		Status:        model.CreditPackageActive,
		GrantSource:   model.GrantSourceB2BGrant,
	}).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
	})
	require.Error(t, err, "child already has active subscription → reject repeat grant")
}

func TestGrantMembership_ChildAlreadyHasTrial_TrialRejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	// Seed trial package (even exhausted — HasTrialPackage is lifetime-check)
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{
		UserID:        child,
		Type:          model.CreditTypeTrial,
		TotalCredits:  200,
		RemainCredits: 0,
		ActivatedAt:   now,
		ExpiresAt:     now.Add(3 * 24 * time.Hour),
		Status:        model.CreditPackageExhausted,
		GrantSource:   model.GrantSourceB2BGrant,
	}).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  child,
		ProductType:  model.ProductTypeTrial,
	})
	require.Error(t, err, "trial can only be granted once per child")
}

// ---------- Q1.2 GrantMembership: billing_mode switch legacy→credits ----------

func TestGrantMembership_SwitchesBillingModeLegacyToCredits(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

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

// ---------- Q1.2 GrantMembership: ProductType validation ----------

func TestGrantMembership_UnsupportedProductType_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

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
	}
}
