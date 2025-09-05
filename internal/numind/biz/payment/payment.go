package payment

import (
	"context"
	"fmt"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"time"

	"numind-server/internal/pkg/log"

	"gorm.io/gorm"
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
		OutTradeNo:       req.OutTradeNo,
		UserID:           userID,
		Amount:           req.Amount,
		Description:      req.Description,
		Channel:          model.PaymentChannelWechat, // 默认微信支付
		Status:           model.PaymentStatusPending,
		PayMethod:        req.PayMethod,
		OpenID:           req.OpenID,
		MembershipType:   req.MembershipType,
		SubscriptionType: req.SubscriptionType,
		PackageCount:     req.PackageCount,
		ExpireAt:         &time.Time{}, // 设置过期时间
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
		// 处理会员购买逻辑
		if err := b.handleMembershipPurchase(ctx, payment); err != nil {
			log.C(ctx).Errorw("Failed to handle membership purchase", "error", err.Error(), "out_trade_no", outTradeNo)
			// 记录错误但不影响支付状态更新
		}
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

// handleMembershipPurchase 处理会员购买
func (b *paymentBiz) handleMembershipPurchase(ctx context.Context, payment *model.PaymentM) error {
	// 获取用户信息
	user, err := b.ds.Users().GetUserByID(ctx, payment.UserID)
	if err != nil {
		return fmt.Errorf("获取用户信息失败: %w", err)
	}

	// 根据会员类型更新用户状态
	switch payment.MembershipType {
	case model.MembershipTypeSubscription:
		// 订阅会员（月度或年度）
		now := time.Now()
		var expiresAt *time.Time

		// 根据订阅类型计算到期时间
		if payment.SubscriptionType == model.SubscriptionTypeMonthly {
			expires := now.AddDate(0, 1, 0) // 1个月后
			expiresAt = &expires
		} else if payment.SubscriptionType == model.SubscriptionTypeYearly {
			expires := now.AddDate(1, 0, 0) // 1年后
			expiresAt = &expires
		} else {
			return fmt.Errorf("无效的订阅类型: %s", payment.SubscriptionType)
		}

		// 更新用户会员信息
		updateData := map[string]interface{}{
			"membership_type":    model.MembershipTypeSubscription,
			"subscription_type":  payment.SubscriptionType,
			"is_pro":             true,
			"membership_expires": expiresAt,
		}

		// 如果用户已有订阅会员且未过期，延长到期时间
		if user.MembershipType == model.MembershipTypeSubscription &&
			user.MembershipExpires != nil &&
			user.MembershipExpires.After(now) {
			// 在现有到期时间基础上延长
			if payment.SubscriptionType == model.SubscriptionTypeMonthly {
				newExpires := user.MembershipExpires.AddDate(0, 1, 0)
				updateData["membership_expires"] = &newExpires
			} else if payment.SubscriptionType == model.SubscriptionTypeYearly {
				newExpires := user.MembershipExpires.AddDate(1, 0, 0)
				updateData["membership_expires"] = &newExpires
			}
		}

		if err := b.ds.DB().Model(&model.User{}).Where("id = ?", payment.UserID).
			Updates(updateData).Error; err != nil {
			return fmt.Errorf("更新用户会员状态失败: %w", err)
		}

	case model.MembershipTypePackage:
		// 包次数类型，增加次数
		if err := b.ds.DB().Model(&model.User{}).Where("id = ?", payment.UserID).
			UpdateColumns(map[string]interface{}{
				"membership_type": model.MembershipTypePackage,
				"is_pro":          true,
				"package_count":   gorm.Expr("package_count + ?", payment.PackageCount),
			}).Error; err != nil {
			return fmt.Errorf("更新用户包次数失败: %w", err)
		}

	default:
		return fmt.Errorf("不支持的会员类型: %s", payment.MembershipType)
	}

	log.C(ctx).Infow("Membership purchase processed successfully",
		"user_id", payment.UserID,
		"membership_type", payment.MembershipType,
		"subscription_type", payment.SubscriptionType,
		"package_count", payment.PackageCount)

	return nil
}
