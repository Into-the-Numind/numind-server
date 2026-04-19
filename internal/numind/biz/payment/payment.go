package payment

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
)

// IPaymentBiz 支付业务逻辑接口
type IPaymentBiz interface {
	CreateOrder(ctx context.Context, payerID, userID uint, productType string, months int, payChannel string) (*model.Order, error)
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
// CreateOrder to bypass the Q1 C-end self-purchase block for trial/monthly/yearly.
// This is required because Q1 redesigned the business flow: C-end users no longer
// self-purchase memberships (they go through B2B grant), but the server still
// exercises the full payment path in tests and potentially in legacy admin flows.
func WithInternalCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalCallerKey{}, true)
}

// IsInternalCaller reports whether the context was marked as internal.
func IsInternalCaller(ctx context.Context) bool {
	v, _ := ctx.Value(internalCallerKey{}).(bool)
	return v
}

// CreateOrder 创建支付订单
func (b *paymentBiz) CreateOrder(ctx context.Context, payerID, userID uint, productType string, months int, payChannel string) (*model.Order, error) {
	// 校验产品类型
	amount := model.GetProductAmount(productType, months)
	if amount <= 0 {
		return nil, fmt.Errorf("invalid product type: %s", productType)
	}

	// Q1 B2B2C 防御性封禁: C 端不支持自购会员（trial/monthly/yearly）。
	// 会员只能通过父账户"帮开通"路径（credit.GrantMembership）赋予。
	// Booster(加量包) 保持 C 端自购（spec §3.7）。
	// 内部调用（WithInternalCaller）可绕过此检查。
	if !IsInternalCaller(ctx) {
		switch productType {
		case model.ProductTypeTrial, model.ProductTypeMonthly, model.ProductTypeYearly:
			return nil, errno.ErrMembershipSelfPurchaseDisabled
		}
	}

	// 购买限制检查
	switch productType {
	case model.ProductTypeTrial:
		hasTrial, err := b.ds.Credits().HasTrialPackage(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("check trial package: %w", err)
		}
		if hasTrial {
			return nil, errno.ErrTrialAlreadyPurchased
		}
		// spec §3.9: 在期会员不能"降级购买 trial"
		user, err := b.ds.Users().GetByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}
		if user.UserTier != model.UserTierFree && user.TierExpires != nil && user.TierExpires.After(time.Now()) {
			return nil, errno.ErrTrialNotAvailableInPeriod
		}
	case model.ProductTypeMonthly, model.ProductTypeYearly:
		hasActive, err := b.ds.Credits().HasActiveSubscription(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("check active subscription: %w", err)
		}
		if hasActive {
			return nil, fmt.Errorf("用户已有生效中的订阅，请到期后再购买")
		}
		// spec §3.9: 在期会员购买同类或更低类型会员被拒（升级放行）
		user, err := b.ds.Users().GetByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}
		if user.UserTier != model.UserTierFree && user.TierExpires != nil && user.TierExpires.After(time.Now()) {
			targetRank := productTypeToTierRank(productType)
			currentRank := model.TierRank(user.UserTier)
			if targetRank <= currentRank {
				return nil, errno.ErrTierInPeriod
			}
			// 升级场景（如 trial → standard / standard → premium）放行
		}
	case model.ProductTypeBooster:
		// spec §3.7: 加量包需要会员资格（active subscription credit package）
		// 复用现有 HasActiveSubscription；不新发明 HasActiveSubscriptionPackage
		hasActive, err := b.ds.Credits().HasActiveSubscription(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("check active subscription: %w", err)
		}
		if !hasActive {
			return nil, errno.ErrMembershipRequired
		}
		// P4a 决策：legacy_tier 用户不支持加量包
		user, err := b.ds.Users().GetByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}
		if user.BillingMode == model.BillingModeLegacyTier {
			return nil, errno.ErrBoosterNotAvailableForLegacy
		}
	}

	// 生成订单号
	orderNo := generateOrderNo()
	productName := model.GetProductName(productType, months)

	// 调用支付渠道创建预付单
	var codeURL string
	switch payChannel {
	case model.PayChannelWechat:
		if b.wechat == nil {
			return nil, fmt.Errorf("微信支付未配置")
		}
		var err error
		codeURL, err = b.wechat.NativePrepay(ctx, orderNo, amount, productName)
		if err != nil {
			return nil, fmt.Errorf("wechat prepay: %w", err)
		}
	case model.PayChannelAlipay:
		if b.alipay == nil {
			return nil, fmt.Errorf("支付宝未配置")
		}
		var err error
		codeURL, err = b.alipay.PagePay(ctx, orderNo, amount, productName)
		if err != nil {
			return nil, fmt.Errorf("alipay page pay: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported pay channel: %s", payChannel)
	}

	// 创建订单记录
	order := &model.Order{
		OrderNo:     orderNo,
		UserID:      userID,
		PayerID:     payerID,
		ProductType: productType,
		Months:      months,
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

// fulfillOrder 支付成功后的履约逻辑（更新订单状态 + 发放积分包，在同一事务中）
func (b *paymentBiz) fulfillOrder(ctx context.Context, orderNo string, tradeNo string) error {
	// 查询订单
	order, err := b.ds.Orders().GetByOrderNo(ctx, orderNo)
	if err != nil {
		return fmt.Errorf("get order by order_no %s: %w", orderNo, err)
	}

	// 幂等检查：已支付则跳过
	if order.PayStatus != model.OrderStatusPending {
		log.Infow("Order already processed, skipping", "order_no", orderNo, "pay_status", order.PayStatus)
		return nil
	}

	// 在单个事务中完成订单更新和积分发放
	now := time.Now()
	err = b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 更新订单状态为已支付（原子幂等：WHERE pay_status = pending + 检查 RowsAffected）
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
			// 并发回调已处理此订单，跳过
			log.Infow("Order already fulfilled by concurrent callback", "order_no", orderNo)
			return nil
		}

		// 发放积分包（使用同一个事务）
		if err := b.creditBiz.RechargeWithOrderTx(ctx, tx, order.UserID, order.ID, order.ProductType, order.Months); err != nil {
			return fmt.Errorf("recharge credits: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("fulfill order %s: %w", orderNo, err)
	}

	// spec §3.8: 独立短事务切换 billing_mode（不嵌套进订单事务，避免 User 行锁争用）
	// 切换失败不影响订单结果——由 D.4 cron 兜底
	if switchErr := b.switchBillingModeIfLegacy(ctx, order.UserID); switchErr != nil {
		log.Warnw("switch billing_mode failed; cron fallback will retry",
			"user_id", order.UserID, "order_id", order.ID, "order_no", orderNo, "error", switchErr)
	}

	log.Infow("Order fulfilled successfully", "order_no", orderNo, "trade_no", tradeNo, "user_id", order.UserID, "product_type", order.ProductType)
	return nil
}

// switchBillingModeIfLegacy 在独立短事务中将 legacy_tier 用户切换为 credits 模式。
// 幂等且安全：非 legacy_tier 用户或不存在的用户都匹配 0 行，Update 本身不会报错。
// 调用方（fulfillOrder）对返回的 error 仅记录 log warn，由 cron 兜底（D.4）。
// 见 spec §3.8。
func (b *paymentBiz) switchBillingModeIfLegacy(ctx context.Context, userID uint) error {
	return b.ds.DB().WithContext(ctx).
		Model(&model.User{}).
		Where("id = ? AND billing_mode = ?", userID, model.BillingModeLegacyTier).
		Update("billing_mode", model.BillingModeCredits).Error
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

// productTypeToTierRank 将会员类产品映射到目标 tier 的 rank（spec §3.9 防提前续费）
// monthly → standard=2 / yearly → standard=2（未来 premium_yearly → premium=3）
// 返回 0 表示未知产品类型（由调用方保证只在 Monthly/Yearly 分支调用）
func productTypeToTierRank(productType string) int {
	switch productType {
	case model.ProductTypeMonthly, model.ProductTypeYearly:
		return model.TierRank(model.UserTierStandard)
	default:
		return 0
	}
}
