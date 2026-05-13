package payment

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	membershipmodel "numind-server/internal/pkg/model/membership"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
)

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
)

// IPaymentBiz 支付业务逻辑接口
type IPaymentBiz interface {
	// CreateOrder creates a payment order. Only product_type=booster is accepted;
	// trial/monthly/yearly memberships are granted via the B2B grant path.
	// quantity specifies the number of booster units (1–10000).
	CreateOrder(ctx context.Context, payerID, userID uint, productType string, quantity int, payChannel string) (*model.Order, error)
	HandleWechatNotify(ctx context.Context, request *http.Request) error
	HandleAlipayNotify(ctx context.Context, request *http.Request) error
	GetOrder(ctx context.Context, orderID uint64) (*model.Order, error)
	ListOrdersByPayer(ctx context.Context, payerID uint, offset, limit int) ([]model.Order, int64, error)
	CloseExpiredOrders(ctx context.Context) error
}

type paymentBiz struct {
	ds        store.IStore
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
func (b *paymentBiz) CreateOrder(ctx context.Context, payerID, userID uint, productType string, quantity int, payChannel string) (*model.Order, error) {
	// §5.10: Only booster is accepted via the order interface. trial/monthly/yearly
	// must go through the grant path. Reject everything else.
	if productType != model.ProductTypeBooster {
		return nil, errno.ErrInvalidProductType
	}

	// §5.2: quantity ∈ [1, 10000].
	if quantity < 1 || quantity > boosterMaxQuantity {
		return nil, errno.ErrBoosterQuantityExceedsLimit
	}

	// §5.2: Beneficiary must have an active membership (subscription or trial).
	// Use the new membership store for accurate real-time check.
	now := time.Now()
	hasSub, err := b.ds.Membership().Subscriptions().HasActive(ctx, uint64(userID), now)
	if err != nil {
		return nil, fmt.Errorf("check active subscription: %w", err)
	}
	hasTrial, err := b.ds.Membership().TrialGrants().HasActive(ctx, uint64(userID), now)
	if err != nil {
		return nil, fmt.Errorf("check active trial: %w", err)
	}
	if !hasSub && !hasTrial {
		return nil, errno.ErrNotActiveMember
	}

	// P4a decision: legacy_tier users are not supported for booster purchases.
	beneficiary, err := b.ds.Users().GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get beneficiary: %w", err)
	}
	if beneficiary.BillingMode == model.BillingModeLegacyTier {
		return nil, errno.ErrBoosterNotAvailableForLegacy
	}

	// Compute total amount: quantity × ¥29.9 per unit.
	amount := boosterCentsPerUnit * int64(quantity)
	productName := fmt.Sprintf("有数AI工作台-加量包 × %d", quantity)

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

	// Persist order. Quantity is stored in the Months field (booster never uses
	// months; this avoids a schema migration for the payment_order table).
	order := &model.Order{
		OrderNo:     orderNo,
		UserID:      userID,
		PayerID:     payerID,
		ProductType: productType,
		Months:      quantity, // repurposed: stores booster quantity for fulfillOrder
		Amount:      amount,
		PayChannel:  payChannel,
		PayStatus:   model.OrderStatusPending,
		CodeURL:     codeURL,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}

	if err := b.ds.Orders().Create(ctx, order); err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	return order, nil
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
