package credit

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ICreditBiz 积分业务逻辑接口
type ICreditBiz interface {
	CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string)
	DeductCredits(ctx context.Context, userID uint, costCents int64, operation, bizRefType, bizRefID string, usageRecordID *uint64) error
	GetBalance(ctx context.Context, userID uint) (int64, error)
	RechargeCredits(ctx context.Context, userID uint, packageType string, totalCredits int64, expiresAt time.Time) error
	RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error
	RunCronTasks(ctx context.Context) error
}

type creditBiz struct {
	ds store.IStore
}

// NewCreditBiz 创建积分业务逻辑实例
func NewCreditBiz(ds store.IStore) ICreditBiz {
	return &creditBiz{ds: ds}
}

// CanPerformAIOperation 检查用户是否可以执行 AI 操作
// 旧会员走旧逻辑，新用户走积分逻辑
func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
	// 旧会员：走旧逻辑
	if user.HasActiveMembership() {
		if IsSopOperation(operation) {
			return user.CanRunSOP()
		}
		// 旧会员对非 SOP 操作不做限制
		return true, ""
	}

	// 新用户（free）：走积分逻辑
	estimated := GetEstimatedCredits(operation)
	balance, err := b.ds.Credits().GetBalance(ctx, user.ID)
	if err != nil {
		log.Errorw("Failed to get credit balance", "user_id", user.ID, "error", err)
		// 查不到余额时也返回不足，避免免费使用
		return false, "积分不足，请联系管理员充值"
	}

	if balance < estimated {
		return false, "积分不足，请联系管理员充值"
	}

	return true, ""
}

// DeductCredits 扣减积分（FIFO 按到期时间顺序扣减积分包）
func (b *creditBiz) DeductCredits(ctx context.Context, userID uint, costCents int64, operation, bizRefType, bizRefID string, usageRecordID *uint64) error {
	credits := int64(math.Round(float64(costCents)))
	if credits <= 0 {
		return nil
	}

	// 确保账户存在
	if _, err := b.ds.Credits().GetOrCreateAccount(ctx, userID); err != nil {
		return fmt.Errorf("ensure credit account: %w", err)
	}

	// 在事务中执行扣减
	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 获取有效积分包（FIFO by ExpiresAt，加行锁）
		packages, err := b.ds.Credits().GetActivePackagesForUpdate(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("get active packages: %w", err)
		}

		remaining := credits
		for i := range packages {
			if remaining <= 0 {
				break
			}

			pkg := &packages[i]
			deduct := remaining
			if deduct > pkg.RemainCredits {
				deduct = pkg.RemainCredits
			}

			pkg.RemainCredits -= deduct
			if pkg.RemainCredits <= 0 {
				pkg.Status = model.CreditPackageExhausted
			}

			if err := b.ds.Credits().UpdatePackage(ctx, tx, pkg); err != nil {
				return fmt.Errorf("update package %d: %w", pkg.ID, err)
			}

			// 创建流水记录
			txn := &model.CreditTransaction{
				UserID:        userID,
				PackageID:     pkg.ID,
				Amount:        -deduct,
				Operation:     operation,
				UsageRecordID: usageRecordID,
				BizRefType:    bizRefType,
				BizRefID:      bizRefID,
			}
			if err := b.ds.Credits().CreateTransaction(ctx, tx, txn); err != nil {
				return fmt.Errorf("create transaction: %w", err)
			}

			remaining -= deduct
		}

		if remaining > 0 {
			log.Errorw("Insufficient credits during deduction", "user_id", userID, "remaining", remaining, "operation", operation)
		}

		// 更新账户余额缓存
		if err := b.ds.Credits().UpdateBalance(ctx, tx, userID, -credits); err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		// 如果有 usageRecordID，更新 UsageRecord 的 credits_deducted
		if usageRecordID != nil {
			if err := tx.Model(&model.UsageRecord{}).Where("id = ?", *usageRecordID).Update("credits_deducted", credits).Error; err != nil {
				return fmt.Errorf("update usage record credits_deducted: %w", err)
			}
		}

		return nil
	})
}

// GetBalance 获取用户积分余额
func (b *creditBiz) GetBalance(ctx context.Context, userID uint) (int64, error) {
	return b.ds.Credits().GetBalance(ctx, userID)
}

// RechargeCredits 充值积分（创建积分包并更新余额）
func (b *creditBiz) RechargeCredits(ctx context.Context, userID uint, packageType string, totalCredits int64, expiresAt time.Time) error {
	// 确保账户存在
	if _, err := b.ds.Credits().GetOrCreateAccount(ctx, userID); err != nil {
		return fmt.Errorf("ensure credit account: %w", err)
	}

	now := time.Now()

	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 创建积分包
		pkg := model.CreditPackage{
			UserID:        userID,
			Type:          packageType,
			TotalCredits:  totalCredits,
			RemainCredits: totalCredits,
			ActivatedAt:   now,
			ExpiresAt:     expiresAt,
			Status:        model.CreditPackageActive,
		}
		if err := tx.Create(&pkg).Error; err != nil {
			return fmt.Errorf("create credit package: %w", err)
		}

		// 更新余额
		if err := b.ds.Credits().UpdateBalance(ctx, tx, userID, totalCredits); err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		return nil
	})
}

// RechargeWithOrderTx 在调用方的事务中创建积分包并更新余额（支付回调用）
func (b *creditBiz) RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error {
	// 确保账户存在（幂等操作，不需要在事务中）
	if _, err := b.ds.Credits().GetOrCreateAccount(ctx, userID); err != nil {
		return fmt.Errorf("ensure credit account: %w", err)
	}

	cfg := model.GetProductConfig(productType, months)
	if cfg == nil {
		return fmt.Errorf("unknown product type: %s", productType)
	}

	now := time.Now()
	var packages []model.CreditPackage

	switch productType {
	case model.ProductTypeTrial:
		packages = []model.CreditPackage{
			{
				UserID:        userID,
				Type:          model.CreditTypeTrial,
				TotalCredits:  200,
				RemainCredits: 200,
				ActivatedAt:   now,
				ExpiresAt:     now.Add(3 * 24 * time.Hour),
				OrderID:       &orderID,
				Status:        model.CreditPackageActive,
			},
		}
	case model.ProductTypeMonthly:
		m := cfg.Months
		if m < 1 {
			m = 1
		}
		for i := 0; i < m; i++ {
			status := model.CreditPackagePending
			if i == 0 {
				status = model.CreditPackageActive
			}
			activatedAt := now.AddDate(0, i, 0)
			expiresAt := now.AddDate(0, i+1, 0)
			packages = append(packages, model.CreditPackage{
				UserID:        userID,
				Type:          model.CreditTypeSubscription,
				TotalCredits:  2000,
				RemainCredits: 2000,
				ActivatedAt:   activatedAt,
				ExpiresAt:     expiresAt,
				OrderID:       &orderID,
				Status:        status,
			})
		}
	case model.ProductTypeYearly:
		for i := 0; i < 12; i++ {
			status := model.CreditPackagePending
			if i == 0 {
				status = model.CreditPackageActive
			}
			activatedAt := now.AddDate(0, i, 0)
			expiresAt := now.AddDate(0, i+1, 0)
			packages = append(packages, model.CreditPackage{
				UserID:        userID,
				Type:          model.CreditTypeSubscription,
				TotalCredits:  2000,
				RemainCredits: 2000,
				ActivatedAt:   activatedAt,
				ExpiresAt:     expiresAt,
				OrderID:       &orderID,
				Status:        status,
			})
		}
	case model.ProductTypeBooster:
		packages = []model.CreditPackage{
			{
				UserID:        userID,
				Type:          model.CreditTypeBooster,
				TotalCredits:  600,
				RemainCredits: 600,
				ActivatedAt:   now,
				ExpiresAt:     now.Add(90 * 24 * time.Hour),
				OrderID:       &orderID,
				Status:        model.CreditPackageActive,
			},
		}
	default:
		return fmt.Errorf("unsupported product type: %s", productType)
	}

	// 在调用方的事务中创建积分包
	if err := tx.Create(&packages).Error; err != nil {
		return fmt.Errorf("create credit packages: %w", err)
	}

	// 计算立即可用的积分（仅 active 状态的包）
	var immediateCredits int64
	for _, pkg := range packages {
		if pkg.Status == model.CreditPackageActive {
			immediateCredits += pkg.TotalCredits
		}
	}

	// 更新余额（使用同一个事务）
	if err := b.ds.Credits().UpdateBalance(ctx, tx, userID, immediateCredits); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}

	return nil
}

// RunCronTasks 执行积分定时任务（激活 pending 包、过期 active 包、重算余额）
func (b *creditBiz) RunCronTasks(ctx context.Context) error {
	affectedUsers := make(map[uint]struct{})

	// 激活到期的 pending 包
	activatedUsers, err := b.ds.Credits().ActivatePendingPackages(ctx)
	if err != nil {
		log.Errorw("Failed to activate pending packages", "error", err)
	} else {
		for _, uid := range activatedUsers {
			affectedUsers[uid] = struct{}{}
		}
		if len(activatedUsers) > 0 {
			log.Infow("Activated pending packages", "user_count", len(activatedUsers))
		}
	}

	// 过期超时的 active 包
	expiredUsers, err := b.ds.Credits().ExpireActivePackages(ctx)
	if err != nil {
		log.Errorw("Failed to expire active packages", "error", err)
	} else {
		for _, uid := range expiredUsers {
			affectedUsers[uid] = struct{}{}
		}
		if len(expiredUsers) > 0 {
			log.Infow("Expired active packages", "user_count", len(expiredUsers))
		}
	}

	// 重算受影响用户的余额
	for uid := range affectedUsers {
		if err := b.ds.Credits().RecalculateBalance(ctx, uid); err != nil {
			log.Errorw("Failed to recalculate balance", "user_id", uid, "error", err)
		}
	}

	return nil
}
