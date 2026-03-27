package payment

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
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

// CreateOrder 创建支付订单
func (b *paymentBiz) CreateOrder(ctx context.Context, payerID, userID uint, productType string, months int, payChannel string) (*model.Order, error) {
	// 校验产品类型
	amount := model.GetProductAmount(productType, months)
	if amount <= 0 {
		return nil, fmt.Errorf("invalid product type: %s", productType)
	}

	// 购买限制检查
	switch productType {
	case model.ProductTypeTrial:
		hasTrial, err := b.ds.Credits().HasTrialPackage(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("check trial package: %w", err)
		}
		if hasTrial {
			return nil, fmt.Errorf("用户已购买过体验卡，不可重复购买")
		}
	case model.ProductTypeMonthly, model.ProductTypeYearly:
		hasActive, err := b.ds.Credits().HasActiveSubscription(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("check active subscription: %w", err)
		}
		if hasActive {
			return nil, fmt.Errorf("用户已有生效中的订阅，请到期后再购买")
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

	log.Infow("Order fulfilled successfully", "order_no", orderNo, "trade_no", tradeNo, "user_id", order.UserID, "product_type", order.ProductType)
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
