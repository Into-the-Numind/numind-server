package payment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// newPaymentTestDB opens an in-memory SQLite DB and creates the minimal set of
// tables required by the credits-system payment tests (User, CreditAccount,
// CreditPackage, CreditTransaction, Order).
//
// The User.BillingMode GORM tag declares a MySQL ENUM type which SQLite does
// not understand, so we create the user table manually via raw DDL and rely on
// AutoMigrate for the simpler tables.
func newPaymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	// Hand-rolled user table (sqlite can't parse MySQL enum syntax).
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
		&model.Order{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// mustCreateUser inserts a user with given tier / billing_mode and returns the ID.
func mustCreateUser(t *testing.T, db *gorm.DB, tier, billingMode string, tierExpires *time.Time) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, user_tier, tier_expires, billing_mode, monthly_sop_runs) VALUES (?, ?, ?, ?, ?, 0)`,
		time.Now(), time.Now(), tier, tierExpires, billingMode,
	)
	require.NoError(t, res.Error)
	require.Equal(t, int64(1), res.RowsAffected)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// mustCreateActiveSubscriptionPackage creates an active subscription credit package for user.
func mustCreateActiveSubscriptionPackage(t *testing.T, db *gorm.DB, userID uint) {
	t.Helper()
	now := time.Now()
	pkg := &model.CreditPackage{
		UserID:        userID,
		Type:          model.CreditTypeSubscription,
		TotalCredits:  2000,
		RemainCredits: 2000,
		ActivatedAt:   now,
		ExpiresAt:     now.AddDate(0, 1, 0),
		Status:        model.CreditPackageActive,
	}
	require.NoError(t, db.Create(pkg).Error)
}

// newPaymentBizForTest wires up a paymentBiz (with nil wechat/alipay clients so
// that reaching the channel code path yields a deterministic "not configured"
// error — useful for asserting that validation passed).
func newPaymentBizForTest(ds store.IStore) *paymentBiz {
	return &paymentBiz{
		ds:        ds,
		creditBiz: credit.NewCreditBiz(ds),
	}
}

// ---------- D.1: Booster 会员门槛 ----------

func TestCreateOrder_Booster_NoActiveSubscription_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	// free user, no active subscription
	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrMembershipRequired, "booster without active subscription must return ErrMembershipRequired")
}

func TestCreateOrder_Booster_LegacyTierWithSubscription_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	future := time.Now().Add(30 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeLegacyTier, &future)
	mustCreateActiveSubscriptionPackage(t, db, uid)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBoosterNotAvailableForLegacy, "legacy_tier user must be rejected even with active subscription")
}

func TestCreateOrder_Booster_CreditsWithSubscription_PassesValidation(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	future := time.Now().Add(30 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeCredits, &future)
	mustCreateActiveSubscriptionPackage(t, db, uid)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error (validation passed)")
	// wechat client is nil → "微信支付未配置"; assert we did NOT hit one of the gate errors
	assert.NotErrorIs(t, err, errno.ErrMembershipRequired)
	assert.NotErrorIs(t, err, errno.ErrBoosterNotAvailableForLegacy)
}

func TestCreateOrder_Booster_TrialPackageOnly_NotSubscription_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	// User has only a trial credit package (type=trial), not subscription.
	future := time.Now().Add(3 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierTrial, model.BillingModeCredits, &future)
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{
		UserID:        uid,
		Type:          model.CreditTypeTrial,
		TotalCredits:  200,
		RemainCredits: 200,
		ActivatedAt:   now,
		ExpiresAt:     future,
		Status:        model.CreditPackageActive,
	}).Error)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrMembershipRequired, "trial user without subscription must be rejected")
}
