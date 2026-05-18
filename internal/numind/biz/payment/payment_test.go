// payment_test.go: Task 11 booster-only order tests (§Task 11, Spec §5.2, §5.10).
//
// 7 core test cases:
//  1. self_purchase_active_member       — payer=beneficiary, active sub, qty=3 → passes validation, amount=8970
//  2. parent_proxy_purchase             — payer≠beneficiary, qty=1 → passes validation, amount=2990
//  3. quantity_exceeds_10000            — qty=10001 → ErrBoosterQuantityExceedsLimit
//  4. non_member_self_purchase          — no active membership → ErrNotActiveMember
//  5. non_booster_product_type          — trial/monthly → ErrInvalidProductType
//  6. fulfillOrder_BoosterPath          — payment success → balance=5×600=3000, event written
//  7. fulfillOrder_NonBoosterProductType_Rejected — dirty monthly order → ErrInvalidProductType
package payment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	membershipmodel "numind-server/internal/pkg/model/membership"
)

// ── Case 1: self-purchase, active subscription, qty=3, amount=8970 ────────────

func TestCreateOrder_Booster_SelfPurchase_ActiveMember(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db)
	mustCreateActiveSubscription(t, db, uid)

	// qty=3, expected amount = 3 × 2990 = 8970 cents. Validation passes; channel fails.
	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 3, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error, not validation error")
	assert.NotErrorIs(t, err, errno.ErrNotActiveMember)
	assert.NotErrorIs(t, err, errno.ErrBoosterQuantityExceedsLimit)
	assert.NotErrorIs(t, err, errno.ErrInvalidProductType)
	assert.Contains(t, err.Error(), "未配置", "must fail at channel layer not validation")
}

// Verify the amount formula: 3 × 2990 = 8970.
func TestCreateOrder_Booster_AmountFormula_Qty3(t *testing.T) {
	const qty = 3
	assert.Equal(t, int64(8970), boosterCentsPerUnit*int64(qty))
}

// ── Case 2: parent proxy purchase (payer=parent, beneficiary=child), qty=1, amount=2990 ──

func TestCreateOrder_Booster_ParentProxyPurchase(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	parentID := mustCreateUser(t, db)
	childID := mustCreateUser(t, db)
	mustCreateActiveSubscription(t, db, childID)

	// payer=parent, beneficiary=child, qty=1, amount=2990
	_, err := b.CreateOrder(context.Background(), parentID, childID, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error")
	assert.NotErrorIs(t, err, errno.ErrNotActiveMember)
	assert.NotErrorIs(t, err, errno.ErrInvalidProductType)
	assert.Contains(t, err.Error(), "未配置")
}

// Verify amount formula for qty=1: 2990 cents.
func TestCreateOrder_Booster_AmountFormula_Qty1(t *testing.T) {
	const qty = 1
	assert.Equal(t, int64(2990), boosterCentsPerUnit*int64(qty))
}

// ── Case 3: quantity > 10000 → ErrBoosterQuantityExceedsLimit ────────────────

func TestCreateOrder_Booster_QuantityExceeds10000(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 10001, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBoosterQuantityExceedsLimit, "qty=10001 must exceed limit")
}

// qty=0 is also rejected (below minimum of 1).
func TestCreateOrder_Booster_QuantityZero_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 0, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBoosterQuantityExceedsLimit, "qty=0 must be rejected")
}

// qty=10000 is the boundary — still allowed (membership gate fires next).
func TestCreateOrder_Booster_QuantityBoundary10000_NotLimit(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 10000, model.PayChannelWechat)
	require.Error(t, err)
	// qty gate passes; next gate is ErrNotActiveMember (no subscription).
	assert.NotErrorIs(t, err, errno.ErrBoosterQuantityExceedsLimit, "qty=10000 must not exceed limit")
	assert.ErrorIs(t, err, errno.ErrNotActiveMember, "next gate: membership required")
}

// ── Case 4: beneficiary has no active membership → ErrNotActiveMember ─────────

func TestCreateOrder_Booster_NonMember_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrNotActiveMember)
}

// Active trial (CreditsRemaining > 0, not expired) also satisfies the membership gate.
func TestCreateOrder_Booster_ActiveTrialMember_Passes(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)

	uid := mustCreateUser(t, db)

	now := time.Now()
	tg := membershipmodel.TrialGrant{
		UserID:           uint64(uid),
		GrantedAt:        now,
		ExpiresAt:        now.Add(3 * 24 * time.Hour),
		CreditsRemaining: 200,
		Source:           "test",
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(&tg).Error)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeBooster, 1, model.PayChannelWechat)
	require.Error(t, err, "expected channel-not-configured error")
	assert.NotErrorIs(t, err, errno.ErrNotActiveMember, "active trial must pass membership gate")
	assert.Contains(t, err.Error(), "未配置")
}

// ── Case 5: non-booster product type → ErrInvalidProductType ─────────────────

func TestCreateOrder_TrialProductType_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeTrial, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidProductType, "trial must return ErrInvalidProductType")
}

func TestCreateOrder_MonthlyProductType_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b := newPaymentBizForTest(ds)
	uid := mustCreateUser(t, db)

	_, err := b.CreateOrder(context.Background(), uid, uid, model.ProductTypeMonthly, 1, model.PayChannelWechat)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidProductType, "monthly must return ErrInvalidProductType")
}

// ── Case 6: fulfillOrder_BoosterPath ─────────────────────────────────────────
// qty=5: credits_remaining += 5×600=3000; membership_event.quantity=5, amount=14950

func TestFulfillOrder_BoosterPath(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	uid := mustCreateUser(t, db)

	const qty = 5
	order := mustCreatePendingBoosterOrder(t, db, uid, uid, qty)

	require.NoError(t, b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_BOOSTER_6"))

	// Order must be paid.
	var updated model.Order
	require.NoError(t, db.First(&updated, order.ID).Error)
	assert.Equal(t, model.OrderStatusPaid, updated.PayStatus)

	// Booster balance: 5 × 600 = 3000.
	credits := readBoosterCredits(t, db, uid)
	assert.Equal(t, int64(5*600), credits, "booster credits must be 5×600=3000")

	// One membership_event of type booster_granted.
	var eventCount int64
	db.Raw("SELECT COUNT(*) FROM membership_event WHERE user_id = ? AND event_type = 'booster_granted'", uid).Scan(&eventCount)
	assert.Equal(t, int64(1), eventCount, "one membership_event must be written")

	// Event fields: quantity=5, amount_cents=5×2990=14950.
	var eventQty int
	var eventAmount int64
	require.NoError(t, db.Raw(
		"SELECT quantity, amount_cents FROM membership_event WHERE user_id = ? AND event_type = 'booster_granted'",
		uid,
	).Row().Scan(&eventQty, &eventAmount))
	assert.Equal(t, qty, eventQty, "event.quantity must be 5")
	assert.Equal(t, int64(5)*boosterCentsPerUnit, eventAmount, "event.amount_cents must be 5×2990=14950")
}

// fulfillOrder is idempotent: double callback must not double-increment balance.
func TestFulfillOrder_BoosterPath_Idempotent(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	uid := mustCreateUser(t, db)
	order := mustCreatePendingBoosterOrder(t, db, uid, uid, 2)

	require.NoError(t, b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_IDEM_A"))
	require.NoError(t, b.fulfillOrder(context.Background(), order.OrderNo, "TRADE_IDEM_A"))

	credits := readBoosterCredits(t, db, uid)
	assert.Equal(t, int64(2*600), credits, "idempotent: balance must not be doubled")
}

// ── Case 7: fulfillOrder rejects non-booster legacy orders ───────────────────

func TestFulfillOrder_NonBoosterProductType_Rejected(t *testing.T) {
	db := newPaymentTestDB(t)
	ds := store.NewTestStore(db)
	b, _ := newPaymentBizWithFakeCredit(ds)

	uid := mustCreateUser(t, db)

	// Insert a legacy pending order with product_type=monthly (dirty row).
	legacyOrder := &model.Order{
		OrderNo:     "TEST_LEGACY_MONTHLY",
		UserID:      uid,
		PayerID:     uid,
		ProductType: model.ProductTypeMonthly,
		Months:      1,
		Amount:      9900,
		PayChannel:  model.PayChannelWechat,
		PayStatus:   model.OrderStatusPending,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(legacyOrder).Error)

	err := b.fulfillOrder(context.Background(), legacyOrder.OrderNo, "TRADE_LEGACY_7")
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidProductType,
		"fulfillOrder must reject non-booster legacy orders with ErrInvalidProductType")

	// Order remains pending (not paid).
	var check model.Order
	require.NoError(t, db.First(&check, legacyOrder.ID).Error)
	assert.Equal(t, model.OrderStatusPending, check.PayStatus, "legacy order must remain pending")
}
