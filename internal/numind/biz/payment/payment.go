package payment

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/model"
	"numind-server/internal/numind/store"
	"time"

	"numind-server/internal/pkg/log"
)

// PaymentBiz 支付业务接口
type PaymentBiz interface {
	CreatePayment(ctx context.Context, req *model.CreatePaymentRequest, userID uint) (*model.CreatePaymentResponse, error)
	GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*model.PaymentM, error)
	UpdatePaymentStatus(ctx context.Context, outTradeNo, status, transactionID string, paidAt *time.Time) error
	ListPaymentsByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.PaymentM, error)
	ListPaymentsByStatus(ctx context.Context, status string, offset, limit int) ([]*model.PaymentM, error)
	CountPaymentsByUser(ctx context.Context, userID uint) (int64, error)
	CountPaymentsByStatus(ctx context.Context, status string) (int64, error)
	DeletePayment(ctx context.Context, id uint) error
}

// paymentBiz 支付业务实现
type paymentBiz struct {
	ds store.IStore
}

// NewPaymentBiz 创建支付业务实例
func NewPaymentBiz(ds store.IStore) PaymentBiz {
	return &paymentBiz{ds: ds}
}

// CreatePayment 创建支付记录
func (b *paymentBiz) CreatePayment(ctx context.Context, req *model.CreatePaymentRequest, userID uint) (*model.CreatePaymentResponse, error) {
	// 检查订单号是否已存在
	existingPayment, err := b.ds.Payments().GetByOutTradeNo(ctx, req.OutTradeNo)
	if err == nil && existingPayment != nil {
		return nil, fmt.Errorf("订单号 %s 已存在", req.OutTradeNo)
	}

	// 创建支付记录
	payment := &model.PaymentM{
		OutTradeNo:  req.OutTradeNo,
		UserID:      userID,
		Amount:      req.Amount,
		Description: req.Description,
		Channel:     model.PaymentChannelWechat, // 默认微信支付
		Status:      model.PaymentStatusPending,
		PayMethod:   req.PayMethod,
		OpenID:      req.OpenID,
		ExpireAt:    &time.Time{}, // 设置过期时间
	}

	// 设置过期时间为30分钟后
	*payment.ExpireAt = time.Now().Add(30 * time.Minute)

	if err := b.ds.Payments().Create(ctx, payment); err != nil {
		log.C(ctx).Errorw("Failed to create payment record", "error", err.Error(), "out_trade_no", req.OutTradeNo)
		return nil, fmt.Errorf("创建支付记录失败: %w", err)
	}

	// 根据支付方式返回不同的响应
	response := &model.CreatePaymentResponse{
		OutTradeNo: req.OutTradeNo,
	}

	// 这里可以根据不同的支付方式调用对应的支付接口
	// 暂时返回基础信息，具体的支付接口调用在controller层处理
	return response, nil
}

// GetPaymentByOutTradeNo 根据商户订单号获取支付记录
func (b *paymentBiz) GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*model.PaymentM, error) {
	return b.ds.Payments().GetByOutTradeNo(ctx, outTradeNo)
}

// UpdatePaymentStatus 更新支付状态
func (b *paymentBiz) UpdatePaymentStatus(ctx context.Context, outTradeNo, status, transactionID string, paidAt *time.Time) error {
	// 获取支付记录
	payment, err := b.ds.Payments().GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return fmt.Errorf("支付记录不存在: %w", err)
	}

	// 检查状态转换是否合法
	if !isValidStatusTransition(payment.Status, status) {
		return fmt.Errorf("非法的状态转换: %s -> %s", payment.Status, status)
	}

	// 更新状态
	if err := b.ds.Payments().UpdateStatus(ctx, outTradeNo, status, transactionID, paidAt); err != nil {
		log.C(ctx).Errorw("Failed to update payment status", "error", err.Error(), "out_trade_no", outTradeNo, "status", status)
		return fmt.Errorf("更新支付状态失败: %w", err)
	}

	// 如果支付成功，可以在这里添加其他业务逻辑
	if status == model.PaymentStatusSuccess {
		log.C(ctx).Infow("Payment successful", "out_trade_no", outTradeNo, "transaction_id", transactionID)
		// 这里可以添加支付成功后的业务逻辑，比如更新订单状态、发送通知等
	}

	return nil
}

// ListPaymentsByUser 获取用户的支付记录列表
func (b *paymentBiz) ListPaymentsByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.PaymentM, error) {
	return b.ds.Payments().ListByUser(ctx, userID, offset, limit)
}

// ListPaymentsByStatus 根据状态获取支付记录列表
func (b *paymentBiz) ListPaymentsByStatus(ctx context.Context, status string, offset, limit int) ([]*model.PaymentM, error) {
	return b.ds.Payments().ListByStatus(ctx, status, offset, limit)
}

// CountPaymentsByUser 统计用户的支付记录数量
func (b *paymentBiz) CountPaymentsByUser(ctx context.Context, userID uint) (int64, error) {
	return b.ds.Payments().CountByUser(ctx, userID)
}

// CountPaymentsByStatus 根据状态统计支付记录数量
func (b *paymentBiz) CountPaymentsByStatus(ctx context.Context, status string) (int64, error) {
	return b.ds.Payments().CountByStatus(ctx, status)
}

// DeletePayment 删除支付记录
func (b *paymentBiz) DeletePayment(ctx context.Context, id uint) error {
	return b.ds.Payments().Delete(ctx, id)
}

// isValidStatusTransition 检查状态转换是否合法
func isValidStatusTransition(fromStatus, toStatus string) bool {
	validTransitions := map[string][]string{
		model.PaymentStatusPending: {
			model.PaymentStatusSuccess,
			model.PaymentStatusFailed,
			model.PaymentStatusCancelled,
		},
		model.PaymentStatusSuccess: {
			// 支付成功后不能转换为其他状态
		},
		model.PaymentStatusFailed: {
			model.PaymentStatusPending, // 失败后可以重试
		},
		model.PaymentStatusCancelled: {
			// 取消后不能转换为其他状态
		},
	}

	allowedStatuses, exists := validTransitions[fromStatus]
	if !exists {
		return false
	}

	for _, allowed := range allowedStatuses {
		if allowed == toStatus {
			return true
		}
	}

	return false
}
