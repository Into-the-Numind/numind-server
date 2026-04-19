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
	// File-backed sqlite in a tmpdir so every connection sees the same DB.
	// All fulfillOrder-flavored tests use fakeCreditBiz to avoid the nested
	// non-tx write deadlock in RechargeWithOrderTx's GetOrCreateAccount (which
	// cannot be fixed without touching biz/credit/credit.go — Track C territory).
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/payment_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite file DB")

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

// fakeCreditBiz is a test stub for credit.ICreditBiz that bypasses the nested
// non-tx write in RechargeWithOrderTx (which would deadlock SQLite in tests).
// RechargeWithOrderTx here is a no-op, letting fulfillOrder focus on its own
// responsibilities (updating order status + calling switchBillingModeIfLegacy).
type fakeCreditBiz struct {
	rechargeCalls int
}

func (f *fakeCreditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
	return true, ""
}
func (f *fakeCreditBiz) DeductCredits(ctx context.Context, userID uint, costCents int64, operation, bizRefType, bizRefID string, usageRecordID *uint64) error {
	return nil
}
func (f *fakeCreditBiz) DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint, credits int64, reason string) ([]credit.PackageDeduction, error) {
	return nil, nil
}
func (f *fakeCreditBiz) GetBalance(ctx context.Context, userID uint) (int64, error) {
	return 0, nil
}
func (f *fakeCreditBiz) RechargeCredits(ctx context.Context, userID uint, packageType string, totalCredits int64, expiresAt time.Time) error {
	return nil
}
func (f *fakeCreditBiz) RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error {
	f.rechargeCalls++
	return nil
}
func (f *fakeCreditBiz) GrantMembership(ctx context.Context, req credit.GrantMembershipReq) error {
	return nil
}
func (f *fakeCreditBiz) RunCronTasks(ctx context.Context) error { return nil }
func (f *fakeCreditBiz) GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error) {
	return 0, 0, 0, 0, nil
}

// newPaymentBizWithFakeCredit builds a paymentBiz whose creditBiz is a stub.
// Use this for fulfillOrder-shaped tests that need to exercise the transaction
// callback without tripping over the SQLite nested-write deadlock.
func newPaymentBizWithFakeCredit(ds store.IStore) (*paymentBiz, *fakeCreditBiz) {
	fake := &fakeCreditBiz{}
	return &paymentBiz{ds: ds, creditBiz: fake}, fake
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

// ---------- D.2: 防提前续费 ----------

func TestCreateOrder_AntiEarlyRenewal(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(30 * 24 * time.Hour)

	type scenario struct {
		name        string
		tier        string
		tierExpires *time.Time
		productType string
		expectErr   error // if nil → validation should pass (channel error expected)
	}

	cases := []scenario{
		// Monthly purchases
		{"free_user_buys_monthly_passes", model.UserTierFree, nil, model.ProductTypeMonthly, nil},
		{"expired_standard_buys_monthly_passes", model.UserTierStandard, &past, model.ProductTypeMonthly, nil},
		{"in_period_standard_buys_monthly_blocked", model.UserTierStandard, &future, model.ProductTypeMonthly, errno.ErrTierInPeriod},
		{"in_period_premium_buys_monthly_blocked", model.UserTierPremium, &future, model.ProductTypeMonthly, errno.ErrTierInPeriod},
		{"in_period_trial_buys_monthly_upgrade_passes", model.UserTierTrial, &future, model.ProductTypeMonthly, nil},

		// Yearly purchases (yearly currently maps to standard tier rank=2)
		{"free_user_buys_yearly_passes", model.UserTierFree, nil, model.ProductTypeYearly, nil},
		{"in_period_standard_buys_yearly_blocked", model.UserTierStandard, &future, model.ProductTypeYearly, errno.ErrTierInPeriod},
		{"in_period_premium_buys_yearly_blocked", model.UserTierPremium, &future, model.ProductTypeYearly, errno.ErrTierInPeriod},
		{"in_period_trial_buys_yearly_upgrade_passes", model.UserTierTrial, &future, model.ProductTypeYearly, nil},

		// Trial purchases
		{"free_user_buys_trial_passes", model.UserTierFree, nil, model.ProductTypeTrial, nil},
		{"in_period_trial_buys_trial_blocked", model.UserTierTrial, &future, model.ProductTypeTrial, errno.ErrTrialNotAvailableInPeriod},
		{"in_period_standard_buys_trial_blocked", model.UserTierStandard, &future, model.ProductTypeTrial, errno.ErrTrialNotAvailableInPeriod},
		{"in_period_premium_buys_trial_blocked", model.UserTierPremium, &future, model.ProductTypeTrial, errno.ErrTrialNotAvailableInPeriod},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newPaymentTestDB(t)
			ds := store.NewTestStore(db)
			b := newPaymentBizForTest(ds)

			uid := mustCreateUser(t, db, tc.tier, model.BillingModeCredits, tc.tierExpires)

			// Q1: trial/monthly/yearly self-purchase is blocked for external callers.
			// These tests pre-date Q1 and exercise the anti-early-renewal validation
			// inside CreateOrder, so we mark the context as internal to bypass the
			// Q1 gate and reach the legacy validation branches.
			_, err := b.CreateOrder(WithInternalCaller(ctx), uid, uid, tc.productType, 1, model.PayChannelWechat)
			require.Error(t, err, "CreateOrder should always error in tests (channel not configured when validation passes)")

			if tc.expectErr != nil {
				assert.ErrorIs(t, err, tc.expectErr, "expected sentinel error")
			} else {
				assert.NotErrorIs(t, err, errno.ErrTierInPeriod)
				assert.NotErrorIs(t, err, errno.ErrTrialNotAvailableInPeriod)
				assert.NotErrorIs(t, err, errno.ErrTrialAlreadyPurchased)
			}
		})
	}
}

// HasTrialPackage existing check still fires even for a free user when a trial
// credit package already exists on the account. Regression guard.
func TestCreateOrder_Trial_AlreadyPurchased_FreeUser(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{
		UserID:        uid,
		Type:          model.CreditTypeTrial,
		TotalCredits:  200,
		RemainCredits: 100,
		ActivatedAt:   now,
		ExpiresAt:     now.Add(3 * 24 * time.Hour),
		Status:        model.CreditPackageExhausted,
	}).Error)

	// Q1: internal caller bypasses self-purchase block so we can reach the
	// HasTrialPackage guard (the original intent of this test).
	_, err := b.CreateOrder(WithInternalCaller(context.Background()), uid, uid, model.ProductTypeTrial, 0, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrTrialAlreadyPurchased)
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

// ---------- Q1.5: C-end self-purchase block ----------

// Q1 business redesign: trial / monthly / yearly memberships must be granted
// via parent's B2B grant path (biz/credit.GrantMembership), not purchased
// by the C-end user. CreateOrder defensively rejects these product types
// for external callers (no WithInternalCaller on ctx).
func TestCreateOrder_Q1_SelfPurchaseDisabled(t *testing.T) {
	cases := []struct {
		name        string
		productType string
		months      int
	}{
		{"trial_rejected", model.ProductTypeTrial, 0},
		{"monthly_rejected", model.ProductTypeMonthly, 1},
		{"yearly_rejected", model.ProductTypeYearly, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newPaymentTestDB(t)
			ds := store.NewTestStore(db)
			b := newPaymentBizForTest(ds)

			uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

			_, err := b.CreateOrder(context.Background(), uid, uid, tc.productType, tc.months, model.PayChannelWechat)
			require.Error(t, err)
			assert.ErrorIs(t, err, errno.ErrMembershipSelfPurchaseDisabled,
				"Q1 must reject external %s self-purchase", tc.productType)
		})
	}
}

// Booster remains self-purchasable under Q1.
func TestCreateOrder_Q1_BoosterStillAllowed(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	future := time.Now().Add(30 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeCredits, &future)
	mustCreateActiveSubscriptionPackage(t, db, uid)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error (Q1 gate must not trip for booster)")
	assert.NotErrorIs(t, err, errno.ErrMembershipSelfPurchaseDisabled,
		"booster must not hit the Q1 self-purchase block")
}

// Internal callers (WithInternalCaller) bypass the Q1 block.
func TestCreateOrder_Q1_InternalCallerBypass(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

	_, err := b.CreateOrder(WithInternalCaller(context.Background()), uid, uid, model.ProductTypeMonthly, 1, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured (validation passes for internal caller)")
	assert.NotErrorIs(t, err, errno.ErrMembershipSelfPurchaseDisabled,
		"internal caller must bypass Q1 block")
}

// ---------- D.3: OnPaymentSuccess billing_mode switch ----------

// readBillingMode fetches billing_mode directly from DB (avoids GORM enum mismatch
// when scanning into *model.User via sqlite).
func readBillingMode(t *testing.T, db *gorm.DB, userID uint) string {
	t.Helper()
	var mode string
	require.NoError(t, db.Raw("SELECT billing_mode FROM user WHERE id = ?", userID).Scan(&mode).Error)
	return mode
}

// mustCreatePendingOrder inserts a pending order that fulfillOrder can consume.
func mustCreatePendingOrder(t *testing.T, db *gorm.DB, userID uint, productType string, months int) *model.Order {
	t.Helper()
	order := &model.Order{
		OrderNo:     "TEST" + time.Now().Format("150405.000000") + "X",
		UserID:      userID,
		PayerID:     userID,
		ProductType: productType,
		Months:      months,
		Amount:      model.GetProductAmount(productType, months),
		PayChannel:  model.PayChannelWechat,
		PayStatus:   model.OrderStatusPending,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(order).Error)
	return order
}

func TestFulfillOrder_LegacyTierUser_BillingModeSwitchedToCredits(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, fake := newPaymentBizWithFakeCredit(ds)

	future := time.Now().Add(30 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeLegacyTier, &future)

	order := mustCreatePendingOrder(t, db, uid, model.ProductTypeMonthly, 1)

	require.NoError(t, b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_TEST_1"))
	assert.Equal(t, 1, fake.rechargeCalls, "RechargeWithOrderTx should have been invoked")

	// billing_mode should have been switched to credits after order success
	assert.Equal(t, model.BillingModeCredits, readBillingMode(t, db, uid),
		"legacy_tier user should have billing_mode switched to credits after order success")

	// Order status should be paid
	var updated model.Order
	require.NoError(t, db.First(&updated, order.ID).Error)
	assert.Equal(t, model.OrderStatusPaid, updated.PayStatus)
}

func TestFulfillOrder_CreditsUser_BillingModeUnchanged(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

	order := mustCreatePendingOrder(t, db, uid, model.ProductTypeMonthly, 1)

	require.NoError(t, b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_TEST_2"))

	assert.Equal(t, model.BillingModeCredits, readBillingMode(t, db, uid),
		"credits user should remain credits (idempotent no-op on switch)")
}

// switchBillingModeIfLegacy on a non-existent user should not error
// (matches 0 rows, Update is idempotent). Real DB failures only log a warning
// and never fail the order — covered by the log.Warn branch.
func TestSwitchBillingModeIfLegacy_NoMatchingRow_Ok(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	err := b.switchBillingModeIfLegacy(context.Background(), 999999)
	assert.NoError(t, err, "must tolerate missing user rows")
}

// When the fulfill transaction succeeds but the switch fails (DB closed),
// the order is still marked paid. This isolates the separate-tx semantic:
// switch failure is warn-only.
func TestFulfillOrder_SwitchFailureLogsButOrderSucceeds(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	future := time.Now().Add(30 * 24 * time.Hour)
	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeLegacyTier, &future)

	order := mustCreatePendingOrder(t, db, uid, model.ProductTypeMonthly, 1)

	// Drop the `user` table to simulate a DB-level failure for switchBillingModeIfLegacy.
	// This happens AFTER the tx commit; fulfillOrder should still return nil.
	require.NoError(t, db.Exec("DROP TABLE user").Error)

	err := b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_TEST_3")
	require.NoError(t, err, "switch failure must not fail the order")

	var updated model.Order
	require.NoError(t, db.First(&updated, order.ID).Error)
	assert.Equal(t, model.OrderStatusPaid, updated.PayStatus,
		"order should still be paid when switch fails")
}
