package payment

import (
	"context"
	"fmt"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
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
	ds             store.IStore
	priceValidator *PriceValidator
}

// NewPaymentBiz 创建支付业务实例
func NewPaymentBiz(ds store.IStore) PaymentBiz {
	return &paymentBiz{
		ds:             ds,
		priceValidator: NewPriceValidator(),
	}
}

// CreatePayment 创建支付记录
func (b *paymentBiz) CreatePayment(ctx context.Context, req *model.CreatePaymentRequest, userID uint) (*model.CreatePaymentResponse, error) {
	// 检查订单号是否已存在
	existingPayment, err := b.ds.Payments().GetByOutTradeNo(ctx, req.OutTradeNo)
	if err == nil && existingPayment != nil {
		return nil, fmt.Errorf("订单号 %s 已存在", req.OutTradeNo)
	}

	// 严格验证价格和会员类型的对应关系
	if err := b.validatePaymentRequest(req); err != nil {
		log.C(ctx).Warnw("支付请求验证失败", "error", err.Error(), "user_id", userID, "amount", req.Amount, "membership_type", req.MembershipType)
		return nil, fmt.Errorf("支付请求验证失败: %w", err)
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
		PackageCount:     req.PackageCount,
		SubscriptionDays: req.SubscriptionDays,
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
	log.C(ctx).Infow("UpdatePaymentStatus called",
		"out_trade_no", outTradeNo,
		"status", status,
		"transaction_id", transactionID,
		"paid_at", paidAt)

	// 获取支付记录
	payment, err := b.ds.Payments().GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		log.C(ctx).Errorw("Payment record not found",
			"out_trade_no", outTradeNo,
			"error", err.Error())
		return fmt.Errorf("支付记录不存在: %w", err)
	}

	log.C(ctx).Infow("Payment record found",
		"payment_id", payment.ID,
		"user_id", payment.UserID,
		"current_status", payment.Status,
		"amount", payment.Amount,
		"membership_type", payment.MembershipType,
		"subscription_days", payment.SubscriptionDays)

	// 检查状态转换是否合法
	if !isValidStatusTransition(payment.Status, status) {
		log.C(ctx).Errorw("Invalid status transition",
			"current_status", payment.Status,
			"target_status", status,
			"out_trade_no", outTradeNo)
		return fmt.Errorf("非法的状态转换: %s -> %s", payment.Status, status)
	}

	// 更新状态
	log.C(ctx).Infow("Updating payment status in database",
		"out_trade_no", outTradeNo,
		"from_status", payment.Status,
		"to_status", status)

	if err := b.ds.Payments().UpdateStatus(ctx, outTradeNo, status, transactionID, paidAt); err != nil {
		log.C(ctx).Errorw("Failed to update payment status in database",
			"error", err.Error(),
			"out_trade_no", outTradeNo,
			"status", status)
		return fmt.Errorf("更新支付状态失败: %w", err)
	}

	log.C(ctx).Infow("Payment status updated in database successfully",
		"out_trade_no", outTradeNo,
		"status", status)

	// 记录支付状态变更审计日志
	if err := b.logPaymentAudit(ctx, payment, status, transactionID, paidAt); err != nil {
		log.C(ctx).Errorw("Failed to log payment audit",
			"error", err.Error(),
			"out_trade_no", outTradeNo)
		// 审计日志记录失败不影响支付流程
	} else {
		log.C(ctx).Infow("Payment audit logged successfully", "out_trade_no", outTradeNo)
	}

	// 如果支付成功，可以在这里添加其他业务逻辑
	if status == model.PaymentStatusSuccess {
		log.C(ctx).Infow("Payment successful, processing membership purchase",
			"out_trade_no", outTradeNo,
			"transaction_id", transactionID,
			"user_id", payment.UserID,
			"membership_type", payment.MembershipType)

		// 这里可以添加支付成功后的业务逻辑，比如更新订单状态、发送通知等
		// 处理会员购买逻辑
		if err := b.handleMembershipPurchase(ctx, payment); err != nil {
			log.C(ctx).Errorw("Failed to handle membership purchase",
				"error", err.Error(),
				"out_trade_no", outTradeNo,
				"user_id", payment.UserID,
				"membership_type", payment.MembershipType,
				"amount", payment.Amount)
			// 记录错误但不影响支付状态更新
		} else {
			log.C(ctx).Infow("Membership purchase handled successfully",
				"out_trade_no", outTradeNo,
				"user_id", payment.UserID)
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
	log.C(ctx).Infow("handleMembershipPurchase called",
		"payment_id", payment.ID,
		"user_id", payment.UserID,
		"membership_type", payment.MembershipType,
		"amount", payment.Amount,
		"subscription_days", payment.SubscriptionDays,
		"package_count", payment.PackageCount)

	// 获取用户信息
	user, err := b.ds.Users().GetUserByID(ctx, payment.UserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get user info",
			"user_id", payment.UserID,
			"error", err.Error())
		return fmt.Errorf("获取用户信息失败: %w", err)
	}

	log.C(ctx).Infow("User info retrieved",
		"user_id", user.ID,
		"current_membership_type", user.MembershipType,
		"current_is_pro", user.IsPro,
		"current_membership_expires", user.MembershipExpires)

	// 根据会员类型更新用户状态
	switch payment.MembershipType {
	case model.MembershipTypeSubscription:
		// 订阅会员 - 使用天数累加
		now := time.Now()

		// 根据订阅天数计算到期时间
		days := payment.SubscriptionDays
		if days <= 0 {
			// 如果没有传递天数，根据金额判断（兼容旧逻辑）
			log.C(ctx).Warnw("Subscription days is 0 or negative, inferring from amount",
				"subscription_days", payment.SubscriptionDays,
				"amount", payment.Amount)
			// 检查是否为开发环境
			runmode := viper.GetString("runmode")
			if runmode == "debug" {
				// 开发环境：1元（100分），默认30天
				if payment.Amount == 100 {
					days = 30 // 开发环境默认30天
				} else {
					days = 30 // 默认30天
				}
			} else {
				// 生产环境：正常价格
				if payment.Amount == 1600 {
					days = 30
				} else if payment.Amount == 11900 {
					days = 365
				} else {
					days = 30 // 默认30天
				}
			}
			log.C(ctx).Infow("Inferred subscription days from amount",
				"amount", payment.Amount,
				"inferred_days", days)
		} else {
			log.C(ctx).Infow("Using subscription days from payment",
				"subscription_days", days)
		}

		// 确定新的会员类型
		var newMembershipType string = model.MembershipTypeSubscription

		// 计算新的到期时间
		// 如果用户已有订阅会员且未过期，在现有到期时间基础上累加天数
		// 否则从当前时间开始计算
		var expiresAt *time.Time
		if user.MembershipExpires != nil && user.MembershipExpires.After(now) {
			// 在现有到期时间基础上累加天数（续费）
			newExpires := user.MembershipExpires.AddDate(0, 0, days)
			expiresAt = &newExpires
			log.C(ctx).Infow("Renewing subscription",
				"user_id", user.ID,
				"old_expires", user.MembershipExpires,
				"new_expires", newExpires,
				"days_added", days)
		} else {
			// 从当前时间开始计算（新购买或已过期）
			expires := now.AddDate(0, 0, days)
			expiresAt = &expires
			log.C(ctx).Infow("New subscription purchase",
				"user_id", user.ID,
				"new_expires", expires,
				"days", days)
		}

		// 更新用户会员信息
		updateData := map[string]interface{}{
			"membership_type":    newMembershipType,
			"is_pro":             true,
			"membership_expires": expiresAt,
		}

		// 如果是新订阅用户或订阅已过期，设置/重置会员开始时间和月度计数
		isNewSubscription := user.MembershipType != model.MembershipTypeSubscription && user.MembershipType != model.MembershipTypeBoth
		isExpired := user.MembershipExpires == nil || !user.MembershipExpires.After(now)

		if isNewSubscription || isExpired {
			updateData["membership_start_date"] = &now
			updateData["monthly_book_count"] = 0 // 重置月度计数
			log.C(ctx).Infow("Reset membership start date and monthly count",
				"user_id", user.ID,
				"is_new", isNewSubscription,
				"is_expired", isExpired)
		}

		log.C(ctx).Infow("Updating user membership",
			"user_id", payment.UserID,
			"update_data", updateData)

		if err := b.ds.DB().Model(&model.User{}).Where("id = ?", payment.UserID).
			Updates(updateData).Error; err != nil {
			log.C(ctx).Errorw("Failed to update user membership in database",
				"user_id", payment.UserID,
				"error", err.Error(),
				"update_data", updateData)
			return fmt.Errorf("更新用户会员状态失败: %w", err)
		}

		log.C(ctx).Infow("User membership updated successfully",
			"user_id", payment.UserID,
			"new_membership_type", newMembershipType,
			"new_expires_at", expiresAt)

		// 重新获取用户信息，验证更新结果
		updatedUser, err := b.ds.Users().GetUserByID(ctx, payment.UserID)
		if err != nil {
			log.C(ctx).Errorw("Failed to get updated user info for verification",
				"user_id", payment.UserID,
				"error", err.Error())
		} else {
			log.C(ctx).Infow("User membership updated and verified",
				"user_id", updatedUser.ID,
				"membership_type", updatedUser.MembershipType,
				"is_pro", updatedUser.IsPro,
				"membership_expires", updatedUser.MembershipExpires,
				"is_membership_active", updatedUser.IsMembershipActive(),
				"can_use_subscription", updatedUser.CanUseSubscription())
		}

	default:
		log.C(ctx).Errorw("Unsupported membership type",
			"membership_type", payment.MembershipType,
			"payment_id", payment.ID)
		return fmt.Errorf("不支持的会员类型: %s", payment.MembershipType)
	}

	log.C(ctx).Infow("Membership purchase processed successfully",
		"user_id", payment.UserID,
		"membership_type", payment.MembershipType,
		"amount", payment.Amount,
		"package_count", payment.PackageCount)

	return nil
}

// validatePaymentRequest 验证支付请求的价格和参数
func (b *paymentBiz) validatePaymentRequest(req *model.CreatePaymentRequest) error {
	switch req.MembershipType {
	case model.MembershipTypeSubscription:
		// 验证订阅会员价格和天数
		log.C(context.Background()).Infow("支付验证订阅天数", "subscription_days", req.SubscriptionDays, "type", fmt.Sprintf("%T", req.SubscriptionDays))
		if req.SubscriptionDays <= 0 {
			return fmt.Errorf("订阅天数必须大于0")
		}
		if req.SubscriptionDays != 30 && req.SubscriptionDays != 365 {
			return fmt.Errorf("订阅天数只支持30天和365天，当前值: %d", req.SubscriptionDays)
		}

		// 验证价格是否与天数匹配
		expectedPrice, err := b.priceValidator.GetSubscriptionPrice(req.SubscriptionDays)
		if err != nil {
			b.priceValidator.LogPriceValidation(context.Background(), req.MembershipType, req.Amount, req.SubscriptionDays, false, err)
			return err
		}
		if req.Amount != expectedPrice {
			return fmt.Errorf("订阅价格不匹配: 期望%d分，实际%d分", expectedPrice, req.Amount)
		}

		b.priceValidator.LogPriceValidation(context.Background(), req.MembershipType, req.Amount, req.SubscriptionDays, true, nil)

	default:
		return fmt.Errorf("不支持的会员类型: %s", req.MembershipType)
	}

	return nil
}

// logPaymentAudit 记录支付审计日志
func (b *paymentBiz) logPaymentAudit(ctx context.Context, payment *model.PaymentM, status, transactionID string, paidAt *time.Time) error {
	// 使用支付记录中的订阅天数
	subscriptionDays := payment.SubscriptionDays
	if subscriptionDays <= 0 && payment.MembershipType == model.MembershipTypeSubscription {
		// 如果没有记录天数，根据金额计算（兼容旧逻辑）
		runmode := viper.GetString("runmode")
		if runmode == "debug" {
			// 开发环境：1元（100分），默认30天
			if payment.Amount == 100 {
				subscriptionDays = 30
			}
		} else {
			// 生产环境：正常价格
			if payment.Amount == 1600 {
				subscriptionDays = 30
			} else if payment.Amount == 11900 {
				subscriptionDays = 365
			}
		}
	}

	auditLog := &model.PaymentAuditLog{
		OutTradeNo:       payment.OutTradeNo,
		UserID:           payment.UserID,
		Amount:           payment.Amount,
		MembershipType:   payment.MembershipType,
		PackageCount:     payment.PackageCount,
		SubscriptionDays: subscriptionDays,
		Status:           status,
		TransactionID:    transactionID,
		PaidAt:           paidAt,
	}

	// 这里应该调用数据存储层的方法来保存审计日志
	// 由于当前没有对应的存储方法，我们先用日志记录
	log.C(ctx).Infow("Payment audit log",
		"out_trade_no", auditLog.OutTradeNo,
		"user_id", auditLog.UserID,
		"amount", auditLog.Amount,
		"membership_type", auditLog.MembershipType,
		"package_count", auditLog.PackageCount,
		"subscription_days", auditLog.SubscriptionDays,
		"status", auditLog.Status,
		"transaction_id", auditLog.TransactionID,
		"paid_at", auditLog.PaidAt)

	return nil
}
