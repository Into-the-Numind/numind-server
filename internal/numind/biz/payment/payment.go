package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	membershipmodel "numind-server/internal/pkg/model/membership"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
)

// idempotencyKeyIndexName is the UNIQUE-index identifier on
// payment_order.idempotency_key. Used by recoverIdempotentInsert to detect
// the race where two concurrent requests with the same Idempotency-Key
// both pass the pre-check and one of the INSERTs hits the unique constraint.
const idempotencyKeyIndexName = "uniq_order_idempotency_key"

const (
	// boosterCreditsPerUnit is the number of credits granted per booster unit purchased.
	// Spec §5.2: each booster unit = 600 credits.
	boosterCreditsPerUnit int64 = 600

	// boosterCentsPerUnit is the price in cents for one booster unit.
	// Spec §5.2: ¥29.9 = 2990 cents per unit.
	boosterCentsPerUnit int64 = 2990

	// boosterMaxQuantity is the maximum number of booster units allowed per order.
	// Spec §5.2: quantity ∈ [1, 10000].
	boosterMaxQuantity = 10000

	xhsScriptGenerationsPerPack int64 = 10
	xhsScriptCentsPerPack       int64 = 1990
)

// IPaymentBiz 支付业务逻辑接口
type IPaymentBiz interface {
	// CreateOrder creates a payment order for booster credits or XHS script packs.
	// trial/monthly/yearly memberships are granted via the B2B grant path.
	// quantity specifies the number of booster units or script packs (1–10000).
	//
	// idempotencyKey carries the value of the Idempotency-Key header from
	// POST /v1/orders. When non-empty, a prior order with the same key is
	// returned as-is (idempotent retry); a concurrent insert that loses the
	// race recovers by re-querying. Empty key disables dedup (used by internal
	// callers + legacy tests).
	CreateOrder(ctx context.Context, payerID, userID uint, productType string, quantity int, payChannel string, idempotencyKey string) (*model.Order, error)
	HandleWechatNotify(ctx context.Context, request *http.Request) error
	HandleAlipayNotify(ctx context.Context, request *http.Request) error
	GetOrder(ctx context.Context, orderID uint64) (*model.Order, error)
	ListOrdersByPayer(ctx context.Context, payerID uint, offset, limit int) ([]model.Order, int64, error)
	CloseExpiredOrders(ctx context.Context) error
}

type paymentBiz struct {
	ds store.IStore
	// TODO(T11): creditBiz field is currently unused — paymentBiz writes membership
	// tables directly via Membership() store. Remove when ICreditBiz interface is
	// pruned in T11.
	creditBiz credit.ICreditBiz
	wechat    *WechatPayClient
	alipay    *AlipayClient
}

// NewPaymentBiz 创建支付业务逻辑实例
func NewPaymentBiz(ds store.IStore, creditBiz credit.ICreditBiz) IPaymentBiz {
	b := &paymentBiz{
		ds:        ds,
		creditBiz: creditBiz,
	}

	// 初始化微信支付客户端（可选，失败不影响启动）
	wechat, err := NewWechatPayClient()
	if err != nil {
		log.Errorw("Failed to initialize WechatPayClient, wechat payment disabled", "error", err)
	} else {
		b.wechat = wechat
	}

	// 初始化支付宝客户端（可选，失败不影响启动）
	alipay, err := NewAlipayClient()
	if err != nil {
		log.Errorw("Failed to initialize AlipayClient, alipay payment disabled", "error", err)
	} else {
		b.alipay = alipay
	}

	return b
}

// internalCallerKey is the context key that suppresses the Q1 C-end
// self-purchase block. Set via WithInternalCaller(ctx) when invoking
// CreateOrder from inside the server (e.g. historical flows, admin
// operations). External API callers never set this — the authGroup
// controllers build the context without the key, so the block triggers.
type internalCallerKey struct{}

// WithInternalCaller marks a context as an internal-caller context, allowing
// CreateOrder to bypass certain validation checks for internal/admin flows.
func WithInternalCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalCallerKey{}, true)
}

// IsInternalCaller reports whether the context was marked as internal.
func IsInternalCaller(ctx context.Context) bool {
	v, _ := ctx.Value(internalCallerKey{}).(bool)
	return v
}

// CreateOrder creates a payment order for a booster package.
//
// Spec §5.2: only product_type=booster is accepted. Trial/monthly/yearly memberships
// are granted exclusively through the B2B grant path (POST /v1/users/children/:id/grant-membership).
// quantity specifies the number of booster units to purchase (1–10000).
// payer = token subject; userID = beneficiary (self-purchase when equal).
//
// Audit P2#10: idempotencyKey, when non-empty, dedups double-submit retries.
//  1. Pre-check: if an order with the same key already exists, return it
//     immediately (no new payment-channel call, no new row).
//  2. New row is persisted with the key set; the UNIQUE constraint on
//     payment_order.idempotency_key (migration 20260518_125529) backs the
//     dedup invariant against concurrent inserts that both pass the pre-check.
//  3. Race recovery: if Create() fails with a unique-violation on the
//     idempotency-key index, re-query and return the row inserted by the
//     racing request — equivalent to (1) but resolved post-hoc.
func (b *paymentBiz) CreateOrder(ctx context.Context, payerID, userID uint, productType string, quantity int, payChannel string, idempotencyKey string) (*model.Order, error) {
	if productType != model.ProductTypeBooster && productType != model.ProductTypeXhsScriptPack {
		return nil, errno.ErrInvalidProductType
	}

	// §5.2: quantity ∈ [1, 10000].
	if quantity < 1 || quantity > boosterMaxQuantity {
		return nil, errno.ErrBoosterQuantityExceedsLimit
	}

	// P2#10 pre-check: same Idempotency-Key → return existing order.
	// FindByIdempotencyKey short-circuits on empty key, so this is a no-op
	// for internal callers that don't pass through a header.
	if existing, err := b.ds.Orders().FindByIdempotencyKey(ctx, idempotencyKey); err != nil {
		return nil, fmt.Errorf("idempotency pre-check: %w", err)
	} else if existing != nil {
		log.Infow("CreateOrder idempotent replay",
			"order_no", existing.OrderNo, "idempotency_key", idempotencyKey)
		return existing, nil
	}

	amount, productName, err := b.orderPricing(ctx, userID, productType, quantity)
	if err != nil {
		return nil, err
	}
	if productType == model.ProductTypeXhsScriptPack && payChannel != model.PayChannelWechat {
		return nil, errno.ErrInvalidParameter.SetMessage("小红书口播稿购买仅支持微信支付")
	}

	// Generate order number and call payment channel.
	orderNo := generateOrderNo()

	var codeURL string
	switch payChannel {
	case model.PayChannelWechat:
		if b.wechat == nil {
			return nil, fmt.Errorf("微信支付未配置")
		}
		codeURL, err = b.wechat.NativePrepay(ctx, orderNo, amount, productName)
		if err != nil {
			return nil, fmt.Errorf("wechat prepay: %w", err)
		}
	case model.PayChannelAlipay:
		if b.alipay == nil {
			return nil, fmt.Errorf("支付宝未配置")
		}
		codeURL, err = b.alipay.PagePay(ctx, orderNo, amount, productName)
		if err != nil {
			return nil, fmt.Errorf("alipay page pay: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported pay channel: %s", payChannel)
	}

	// Pointerize the idempotency key so empty maps to NULL (not the empty
	// string) in the database — preserves "multiple NULLs allowed under
	// UNIQUE index" semantics for callers without a header.
	var idemKeyPtr *string
	if idempotencyKey != "" {
		k := idempotencyKey
		idemKeyPtr = &k
	}

	// Persist order. Quantity is stored in the Months field (booster never uses
	// months; this avoids a schema migration for the payment_order table).
	order := &model.Order{
		OrderNo:        orderNo,
		UserID:         userID,
		PayerID:        payerID,
		ProductType:    productType,
		Months:         quantity, // repurposed: stores booster quantity for fulfillOrder
		Quantity:       quantity,
		Amount:         amount,
		PayChannel:     payChannel,
		PayStatus:      model.OrderStatusPending,
		CodeURL:        codeURL,
		ExpiredAt:      time.Now().Add(30 * time.Minute),
		IdempotencyKey: idemKeyPtr,
	}

	if err := b.ds.Orders().Create(ctx, order); err != nil {
		// P2#10 race recovery: a concurrent request with the same key beat
		// us to the insert. The pre-check missed because both reads happened
		// before either insert committed. Re-query — the racing request's
		// row is now committed — and return it.
		if idempotencyKey != "" && isIdempotencyKeyConflict(err) {
			existing, qerr := b.ds.Orders().FindByIdempotencyKey(ctx, idempotencyKey)
			if qerr != nil {
				return nil, fmt.Errorf("idempotency race recovery query: %w (original: %v)", qerr, err)
			}
			if existing != nil {
				log.Infow("CreateOrder idempotent race recovered",
					"order_no", existing.OrderNo, "idempotency_key", idempotencyKey)
				return existing, nil
			}
			// Constraint fired but row not found — should be impossible.
			// Fall through with the original error for visibility.
		}
		return nil, fmt.Errorf("create order: %w", err)
	}

	return order, nil
}

func (b *paymentBiz) orderPricing(ctx context.Context, userID uint, productType string, quantity int) (int64, string, error) {
	switch productType {
	case model.ProductTypeBooster:
		// §5.2: Beneficiary must have an active membership (subscription or trial).
		now := time.Now()
		hasSub, err := b.ds.Membership().Subscriptions().HasActive(ctx, uint64(userID), now)
		if err != nil {
			return 0, "", fmt.Errorf("check active subscription: %w", err)
		}
		hasTrial, err := b.ds.Membership().TrialGrants().HasActive(ctx, uint64(userID), now)
		if err != nil {
			return 0, "", fmt.Errorf("check active trial: %w", err)
		}
		if !hasSub && !hasTrial {
			return 0, "", errno.ErrNotActiveMember
		}
		return boosterCentsPerUnit * int64(quantity), fmt.Sprintf("有数AI工作台-加量包 × %d", quantity), nil
	case model.ProductTypeXhsScriptPack:
		generations := int64(quantity) * xhsScriptGenerationsPerPack
		return xhsScriptCentsPerPack * int64(quantity), fmt.Sprintf("小红书口播稿生成 %d 次", generations), nil
	default:
		return 0, "", errno.ErrInvalidProductType
	}
}

// isIdempotencyKeyConflict reports whether err is a UNIQUE-constraint
// violation on the idempotency_key index. Handles MySQL (error 1062 /
// "Duplicate entry") and GORM v2's ErrDuplicatedKey wrapper used by the
// SQLite test driver.
func isIdempotencyKeyConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return strings.Contains(err.Error(), idempotencyKeyIndexName)
	}
	msg := err.Error()
	if !strings.Contains(msg, "1062") && !strings.Contains(msg, "Duplicate entry") {
		return false
	}
	return strings.Contains(msg, idempotencyKeyIndexName)
}

// HandleWechatNotify 处理微信支付回调通知
func (b *paymentBiz) HandleWechatNotify(ctx context.Context, request *http.Request) error {
	if b.wechat == nil {
		return fmt.Errorf("wechat pay client not initialized")
	}

	outTradeNo, transactionID, err := b.wechat.ParseNotifyRequest(ctx, request)
	if err != nil {
		return fmt.Errorf("parse wechat notify: %w", err)
	}

	return b.fulfillOrder(ctx, outTradeNo, transactionID)
}

// HandleAlipayNotify 处理支付宝回调通知
func (b *paymentBiz) HandleAlipayNotify(ctx context.Context, request *http.Request) error {
	if b.alipay == nil {
		return fmt.Errorf("alipay client not initialized")
	}

	notification, err := b.alipay.VerifyNotify(request)
	if err != nil {
		return fmt.Errorf("verify alipay notify: %w", err)
	}

	// 只处理交易成功的通知
	if notification.TradeStatus != "TRADE_SUCCESS" && notification.TradeStatus != "TRADE_FINISHED" {
		log.Infow("Alipay notification ignored, trade not successful", "trade_status", notification.TradeStatus, "out_trade_no", notification.OutTradeNo)
		return nil
	}

	return b.fulfillOrder(ctx, notification.OutTradeNo, notification.TradeNo)
}

// fulfillOrder handles post-payment fulfillment for booster orders.
//
// Spec §5.10: Only booster product_type is supported. Any order with a
// different product_type (legacy dirty rows) is rejected with ErrInvalidProductType.
// Fulfillment: increment user_booster_balance.credits_remaining + write membership_event.
func (b *paymentBiz) fulfillOrder(ctx context.Context, orderNo string, tradeNo string) error {
	// 查询订单
	order, err := b.ds.Orders().GetByOrderNo(ctx, orderNo)
	if err != nil {
		return fmt.Errorf("get order by order_no %s: %w", orderNo, err)
	}

	if order.ProductType == model.ProductTypeXhsScriptPack {
		return b.fulfillXhsScriptOrder(ctx, order, tradeNo)
	}

	// §5.10: Reject non-booster orders (legacy dirty rows).
	if order.ProductType != model.ProductTypeBooster {
		return errno.ErrInvalidProductType
	}

	// 幂等检查：已支付则跳过
	if order.PayStatus != model.OrderStatusPending {
		log.Infow("Order already processed, skipping", "order_no", orderNo, "pay_status", order.PayStatus)
		return nil
	}

	// quantity was stored in the Months field (see CreateOrder comment).
	quantity := order.Months
	if quantity < 1 {
		quantity = 1 // safety fallback for legacy rows with quantity=0
	}
	delta := int64(quantity) * boosterCreditsPerUnit
	amountCents := int64(quantity) * boosterCentsPerUnit

	// Determine granter: if payer != beneficiary, record payer as granter.
	var granterUserID *uint64
	if order.PayerID != order.UserID {
		g := uint64(order.PayerID)
		granterUserID = &g
	}

	now := time.Now()

	err = b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Atomically mark order paid (WHERE pay_status=pending guards idempotency).
		result := tx.Model(&model.Order{}).Where("id = ? AND pay_status = ?", order.ID, model.OrderStatusPending).
			Updates(map[string]interface{}{
				"pay_status": model.OrderStatusPaid,
				"trade_no":   tradeNo,
				"paid_at":    now,
				"updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("update order status: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			// Concurrent callback already processed this order — safe to skip.
			log.Infow("Order already fulfilled by concurrent callback", "order_no", orderNo)
			return nil
		}

		// Increment booster balance (upsert — creates row if not exists).
		if err := b.ds.Membership().BoosterBalances().Increment(ctx, tx, uint64(order.UserID), delta); err != nil {
			return fmt.Errorf("increment booster balance: %w", err)
		}

		// Write membership audit event.
		qty := uint16(quantity) //nolint:gosec // quantity ∈ [1,10000], fits uint16
		idempotencyKey := order.OrderNo
		// source enum (spec §2.x) only allows 'self_purchase' / 'b2b_grant' —
		// 旧代码写死 'payment' 触发 DB Error 1265 Data truncated。改用 granter
		// 是否存在判断：有 granter → 父代购 → b2b_grant；没 granter → 用户自购。
		source := membershipmodel.SourceSelfPurchase
		if granterUserID != nil {
			source = membershipmodel.SourceB2BGrant
		}
		event := &membershipmodel.MembershipEvent{
			UserID:         uint64(order.UserID),
			EventType:      "booster_granted",
			ProductType:    model.ProductTypeBooster,
			Quantity:       &qty,
			AmountCents:    amountCents,
			Source:         source,
			GranterUserID:  granterUserID,
			IdempotencyKey: &idempotencyKey,
			OccurredAt:     now,
		}
		if err := b.ds.Membership().Events().Create(ctx, tx, event); err != nil {
			return fmt.Errorf("create membership event: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("fulfill order %s: %w", orderNo, err)
	}

	log.Infow("Booster order fulfilled",
		"order_no", orderNo, "trade_no", tradeNo,
		"user_id", order.UserID, "quantity", quantity, "delta_credits", delta)
	return nil
}

func (b *paymentBiz) fulfillXhsScriptOrder(ctx context.Context, order *model.Order, tradeNo string) error {
	if order == nil {
		return fmt.Errorf("nil order")
	}
	quantity := xhsScriptOrderQuantity(order)
	if order.PayStatus != model.OrderStatusPending {
		log.Infow("XHS script order already processed, skipping", "order_no", order.OrderNo, "pay_status", order.PayStatus)
		if order.PayStatus == model.OrderStatusPaid {
			b.recordXhsScriptPaymentAnalyticsBestEffort(ctx, order, quantity)
		}
		return nil
	}
	delta := int64(quantity) * xhsScriptGenerationsPerPack
	now := time.Now()

	err := b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Order{}).Where("id = ? AND pay_status = ?", order.ID, model.OrderStatusPending).
			Updates(map[string]interface{}{
				"pay_status": model.OrderStatusPaid,
				"trade_no":   tradeNo,
				"paid_at":    now,
				"updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("update order status: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			log.Infow("XHS script order already fulfilled by concurrent callback", "order_no", order.OrderNo)
			return nil
		}

		if err := grantXhsScriptQuotaTx(ctx, tx, order.UserID, delta, order.OrderNo); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("fulfill xhs script order %s: %w", order.OrderNo, err)
	}
	b.recordXhsScriptPaymentAnalyticsBestEffort(ctx, order, quantity)
	log.Infow("XHS script order fulfilled",
		"order_no", order.OrderNo, "trade_no", tradeNo,
		"user_id", order.UserID, "quantity", quantity, "delta_generations", delta)
	return nil
}

func xhsScriptOrderQuantity(order *model.Order) int {
	if order == nil {
		return 1
	}
	quantity := order.Months
	if quantity < 1 {
		quantity = order.Quantity
	}
	if quantity < 1 {
		quantity = 1
	}
	return quantity
}

func (b *paymentBiz) recordXhsScriptPaymentAnalyticsBestEffort(ctx context.Context, order *model.Order, quantity int) {
	if order == nil {
		return
	}
	props := map[string]interface{}{
		"order_id":     order.ID,
		"order_no":     order.OrderNo,
		"amount_cents": order.Amount,
		"quantity":     quantity,
		"channel":      order.PayChannel,
		"product_type": order.ProductType,
	}
	b.insertXhsScriptAnalyticsEventBestEffort(ctx, order.UserID, "backend:xhs_script:payment_success:"+order.OrderNo, "payment_success", props)

	if !b.hasPreviousPaidXhsScriptOrder(ctx, order.UserID, order.ID) {
		return
	}
	b.insertXhsScriptAnalyticsEventBestEffort(ctx, order.UserID, "backend:xhs_script:repeat_purchase_success:"+order.OrderNo, "repeat_purchase_success", props)
}

func (b *paymentBiz) hasPreviousPaidXhsScriptOrder(ctx context.Context, userID uint, currentOrderID uint64) bool {
	var count int64
	err := b.ds.DB().WithContext(ctx).Model(&model.Order{}).
		Where("user_id = ? AND product_type = ? AND pay_status = ? AND id <> ?", userID, model.ProductTypeXhsScriptPack, model.OrderStatusPaid, currentOrderID).
		Count(&count).Error
	if err != nil {
		log.Warnw("xhs-script repeat purchase analytics check failed", "user_id", userID, "order_id", currentOrderID, "error", err)
		return false
	}
	return count > 0
}

func (b *paymentBiz) insertXhsScriptAnalyticsEventBestEffort(ctx context.Context, userID uint, eventID, eventName string, properties map[string]interface{}) {
	userIDCopy := userID
	event := &model.XhsScriptAnalyticsEvent{
		EventID:    eventID,
		EventName:  eventName,
		UserID:     &userIDCopy,
		Properties: paymentAnalyticsJSON(properties),
		CreatedAt:  time.Now(),
	}
	if err := b.ds.XhsScript().InsertAnalyticsEvent(ctx, event); err != nil {
		log.Warnw("xhs-script payment analytics event failed", "event_name", eventName, "event_id", eventID, "user_id", userID, "error", err)
	}
}

func paymentAnalyticsJSON(properties map[string]interface{}) datatypes.JSON {
	if properties == nil {
		properties = map[string]interface{}{}
	}
	b, err := json.Marshal(properties)
	if err != nil {
		return datatypes.JSON([]byte("{}"))
	}
	return datatypes.JSON(b)
}

func grantXhsScriptQuotaTx(ctx context.Context, tx *gorm.DB, userID uint, delta int64, refID string) error {
	if delta <= 0 {
		return fmt.Errorf("xhs script quota delta must be positive")
	}
	account := model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 3,
		PaidRemaining: 0,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
		return fmt.Errorf("create xhs script quota account: %w", err)
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userID).
		First(&account).Error; err != nil {
		return fmt.Errorf("lock xhs script quota account: %w", err)
	}

	ledger := model.XhsScriptQuotaLedger{
		UserID:  userID,
		Delta:   delta,
		Bucket:  model.XhsScriptQuotaBucketPaid,
		Reason:  model.XhsScriptLedgerReasonPurchase,
		RefType: model.XhsScriptLedgerRefTypePurchase,
		RefID:   refID,
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&ledger)
	if result.Error != nil {
		return fmt.Errorf("create xhs script purchase ledger: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil
	}

	if err := tx.WithContext(ctx).Model(&model.XhsScriptQuotaAccount{}).
		Where("id = ?", account.ID).
		Updates(map[string]interface{}{
			"paid_remaining": account.PaidRemaining + delta,
			"updated_at":     time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("update xhs script paid quota: %w", err)
	}
	return nil
}

// GetOrder 查询订单详情
func (b *paymentBiz) GetOrder(ctx context.Context, orderID uint64) (*model.Order, error) {
	return b.ds.Orders().GetByID(ctx, orderID)
}

// ListOrdersByPayer 查询付款人的订单列表
func (b *paymentBiz) ListOrdersByPayer(ctx context.Context, payerID uint, offset, limit int) ([]model.Order, int64, error) {
	return b.ds.Orders().ListByPayer(ctx, payerID, offset, limit)
}

// CloseExpiredOrders 关闭超时未支付的订单
func (b *paymentBiz) CloseExpiredOrders(ctx context.Context) error {
	affected, err := b.ds.Orders().CloseExpiredOrders(ctx)
	if err != nil {
		return fmt.Errorf("close expired orders: %w", err)
	}
	if affected > 0 {
		log.Infow("Closed expired orders", "count", affected)
	}
	return nil
}

// generateOrderNo 生成订单号: NU + 时间戳 + uuid前8位
func generateOrderNo() string {
	return fmt.Sprintf("NU%s%s", time.Now().Format("20060102150405"), uuid.New().String()[:8])
}
