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
	membershipmodel "numind-server/internal/pkg/model/membership"
)

// newPaymentTestDB opens an in-memory SQLite DB and creates the minimal set of
// tables required by the credits-system payment tests.
//
// Includes: User, CreditAccount, CreditPackage, CreditTransaction, Order,
// and the new membership tables (subscription, trial_grant, user_booster_balance,
// membership_event, credit_cycle).
func newPaymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// File-backed sqlite in a tmpdir so every connection sees the same DB.
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
		// new membership tables
		&membershipmodel.Subscription{},
		&membershipmodel.TrialGrant{},
		&membershipmodel.UserBoosterBalance{},
		&membershipmodel.MembershipEvent{},
		&membershipmodel.CreditCycle{},
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

// mustCreateActiveSubscription creates an active subscription row in the new membership store.
func mustCreateActiveSubscription(t *testing.T, db *gorm.DB, userID uint) {
	t.Helper()
	now := time.Now()
	sub := &membershipmodel.Subscription{
		UserID:               uint64(userID),
		FirstStartedAt:       now,
		CurrentStartedAt:     now,
		ExpiresAt:            now.AddDate(0, 1, 0),
		TotalMonthsPurchased: 1,
		Source:               "b2b_grant",
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, db.Create(sub).Error)
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
type fakeCreditBiz struct {
	rechargeCalls int
}

func (f *fakeCreditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
	return true, ""
}
func (f *fakeCreditBiz) GetBalance(ctx context.Context, userID uint) (int64, error) {
	return 0, nil
}
func (f *fakeCreditBiz) RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error {
	f.rechargeCalls++
	return nil
}
func (f *fakeCreditBiz) GrantMembership(ctx context.Context, req credit.GrantMembershipReq) error {
	return nil
}
func (f *fakeCreditBiz) GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error) {
	return 0, 0, 0, 0, nil
}

// newPaymentBizWithFakeCredit builds a paymentBiz whose creditBiz is a stub.
func newPaymentBizWithFakeCredit(ds store.IStore) (*paymentBiz, *fakeCreditBiz) {
	fake := &fakeCreditBiz{}
	return &paymentBiz{ds: ds, creditBiz: fake}, fake
}

// ---------- D.1: Booster — active membership gate ----------

func TestCreateOrder_Booster_NoActiveMembership_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	// Free user — no subscription, no trial.
	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrNotActiveMember, "booster without active membership must return ErrNotActiveMember")
}

func TestCreateOrder_Booster_LegacyTierWithSubscription_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeLegacyTier, nil)
	mustCreateActiveSubscription(t, db, uid) // has active subscription

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBoosterNotAvailableForLegacy, "legacy_tier user must be rejected even with active subscription")
}

func TestCreateOrder_Booster_CreditsWithSubscription_PassesValidation(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db, model.UserTierStandard, model.BillingModeCredits, nil)
	mustCreateActiveSubscription(t, db, uid)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error (validation passed)")
	// wechat client is nil → "微信支付未配置"; assert we did NOT hit one of the gate errors
	assert.NotErrorIs(t, err, errno.ErrNotActiveMember)
	assert.NotErrorIs(t, err, errno.ErrBoosterNotAvailableForLegacy)
	assert.NotErrorIs(t, err, errno.ErrInvalidProductType)
	assert.NotErrorIs(t, err, errno.ErrBoosterQuantityExceedsLimit)
}

// ---------- D.2: Non-booster product type rejection ----------

// §5.10: trial/monthly/yearly are no longer accepted via the order interface.
// They must go through the B2B grant path.
func TestCreateOrder_NonBooster_AlwaysRejected(t *testing.T) {
	cases := []struct {
		name        string
		productType string
	}{
		{"trial_rejected", model.ProductTypeTrial},
		{"monthly_rejected", model.ProductTypeMonthly},
		{"yearly_rejected", model.ProductTypeYearly},
		{"unknown_rejected", "premium"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newPaymentTestDB(t)
			ds := store.NewTestStore(db)
			b := newPaymentBizForTest(ds)
			uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

			_, err := b.CreateOrder(context.Background(), uid, uid, tc.productType, 1, model.PayChannelWechat)
			require.Error(t, err)
			assert.ErrorIs(t, err, errno.ErrInvalidProductType,
				"product_type=%s must return ErrInvalidProductType", tc.productType)
		})
	}
}

// Even WithInternalCaller cannot bypass the product type gate for non-booster.
func TestCreateOrder_NonBooster_InternalCallerAlsoRejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db, model.UserTierFree, model.BillingModeCredits, nil)

	_, err := b.CreateOrder(WithInternalCaller(context.Background()), uid, uid, model.ProductTypeMonthly, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidProductType,
		"internal caller cannot bypass product type gate")
}

// ---------- D.3: helper for fulfillOrder tests ----------

// mustCreatePendingBoosterOrder inserts a pending booster order.
// quantity is stored in the Months field (booster design, see CreateOrder).
func mustCreatePendingBoosterOrder(t *testing.T, db *gorm.DB, payerID, userID uint, quantity int) *model.Order {
	t.Helper()
	order := &model.Order{
		OrderNo:     "TEST" + time.Now().Format("150405.000000") + "X",
		UserID:      userID,
		PayerID:     payerID,
		ProductType: model.ProductTypeBooster,
		Months:      quantity, // quantity stored in Months field
		Amount:      boosterCentsPerUnit * int64(quantity),
		PayChannel:  model.PayChannelWechat,
		PayStatus:   model.OrderStatusPending,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(order).Error)
	return order
}

// readBoosterCredits fetches credits_remaining from user_booster_balance.
func readBoosterCredits(t *testing.T, db *gorm.DB, userID uint) int64 {
	t.Helper()
	var credits int64
	err := db.Raw("SELECT COALESCE(credits_remaining, 0) FROM user_booster_balance WHERE user_id = ?", userID).Scan(&credits).Error
	require.NoError(t, err)
	return credits
}

// ---------- tech-debt fix: Order.Quantity field for booster orders ----------

// mustCreateBoosterOrder inserts a pending booster order with explicit quantity.
func mustCreateBoosterOrder(t *testing.T, db *gorm.DB, userID uint, quantity int) *model.Order {
	t.Helper()
	order := &model.Order{
		OrderNo:     "BOOST" + time.Now().Format("150405.000000"),
		UserID:      userID,
		PayerID:     userID,
		ProductType: model.ProductTypeBooster,
		Months:      0,        // no longer used for booster
		Quantity:    quantity, // the new dedicated field
		Amount:      model.GetBoosterAmount(quantity),
		PayChannel:  model.PayChannelWechat,
		PayStatus:   model.OrderStatusPending,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(order).Error)
	return order
}

// TestGetBoosterAmount verifies the model helper used by both CreateOrder
// and fulfillOrder to compute booster pricing. Direct unit test of the pure
// function, independent of biz layer mock setup.
func TestGetBoosterAmount(t *testing.T) {
	assert.Equal(t, int64(14950), model.GetBoosterAmount(5),
		"5 booster packs should cost 5 * 2990 = 14950 cents")
	assert.Equal(t, int64(2990), model.GetBoosterAmount(1),
		"1 booster pack should cost 2990 cents")
	assert.Equal(t, int64(2990), model.GetBoosterAmount(0),
		"quantity=0 should be treated as 1 pack")
}

// Note: the former TestCreateBoosterOrder_QuantityField (testing CreateOrder
// reaches the payment channel error) and TestFulfillBoosterOrder_UsesQuantity
// (testing fulfillOrder calls RechargeWithOrderTx) were removed during the
// 2026-05-14 merge of S4 into develop: they targeted develop's interim
// implementation that this branch replaces (CreateOrder now strictly enforces
// active membership via subscription table; fulfillOrder writes user_booster_balance
// + membership_event directly, no longer calls credit.RechargeWithOrderTx).
// Equivalent coverage exists in payment_test.go (qty=5 success path, qty=10001
// boundary, membership_event quantity assertion).

// TestCreateBoosterOrder_QuantityField_OrderRecord verifies that a booster
// order created via the biz layer stores Quantity correctly and Months=0.
// We bypass the payment channel by inserting the order directly (mirroring
// what CreateOrder does internally) and checking the DB state.
func TestCreateBoosterOrder_QuantityField_OrderRecord(t *testing.T) {
	db := newPaymentTestDB(t)

	order := mustCreateBoosterOrder(t, db, 1, 5)

	// Read back from DB
	var fetched model.Order
	require.NoError(t, db.First(&fetched, order.ID).Error)

	assert.Equal(t, 5, fetched.Quantity, "Order.Quantity must store the number of booster packs")
	assert.Equal(t, 0, fetched.Months, "Order.Months must be 0 for booster orders (no business meaning)")
	assert.Equal(t, int64(14950), fetched.Amount, "Order amount must equal quantity * 2990")
}

// Note: TestFulfillBoosterOrder_UsesQuantity removed during 2026-05-14 merge.
// The test asserted RechargeWithOrderTx is called exactly once, but the
// branch's fulfillOrder no longer calls that method — it writes directly to
// user_booster_balance + membership_event via Membership store (spec §3.5).
// Equivalent coverage: payment_test.go qty=5 success path verifies user_booster_balance
// is incremented and membership_event.quantity is written correctly.
