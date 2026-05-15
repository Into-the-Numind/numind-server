package credit

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ICreditBiz 积分业务逻辑接口
type ICreditBiz interface {
	CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string)
	DeductCredits(ctx context.Context, userID uint, costCents int64, operation, bizRefType, bizRefID string, usageRecordID *uint64) error
	// DeductCreditsTx is the composable FIFO deduction primitive used by
	// ICreditService.Reserve so that deduction + reservation writes live in the
	// same outer transaction (spec §3.10). Callers manage the transaction
	// lifecycle; this method must NOT open its own tx.
	//
	// Contract:
	//   - FIFO by credit_package.expires_at ASC (same ordering as DeductCredits)
	//   - writes one CreditTransaction row per package debited (operation=reason)
	//   - updates CreditAccount.balance in place
	//   - returns ErrInsufficientCredits when total balance < requested credits
	//   - returned []PackageDeduction preserves the FIFO debit order (used to
	//     emit credit_reservation_item rows with seq=idx+1)
	//   - credits<=0 is a no-op (returns nil, nil)
	DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint, credits int64, reason string) ([]PackageDeduction, error)
	GetBalance(ctx context.Context, userID uint) (int64, error)
	RechargeCredits(ctx context.Context, userID uint, packageType string, totalCredits int64, expiresAt time.Time) error
	RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error
	// GrantMembership is the B2B2C grant path (spec Q1): parent user
	// grants membership to a child user without going through payment.
	GrantMembership(ctx context.Context, req GrantMembershipReq) error
	RunCronTasks(ctx context.Context) error
	GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error)
}

type creditBiz struct {
	ds            store.IStore
	membershipSvc *membership.MembershipService // wired via WithMembershipSvc; may be nil for tests
}

// NewCreditBiz 创建积分业务逻辑实例
func NewCreditBiz(ds store.IStore) ICreditBiz {
	return &creditBiz{ds: ds}
}

// InjectCreditBizMembershipSvc wires a MembershipService into a creditBiz
// instance after construction. Used by biz.NewBiz to avoid breaking the
// existing NewCreditBiz signature. No-op if the ICreditBiz isn't a *creditBiz
// (e.g. a test mock).
func InjectCreditBizMembershipSvc(b ICreditBiz, svc *membership.MembershipService) {
	if cb, ok := b.(*creditBiz); ok {
		cb.membershipSvc = svc
	}
}

// CanPerformAIOperation 检查用户是否可以执行 AI 操作
// legacy_tier 走旧逻辑（CanRunSOP + 次数制），credits 走积分余额预检。
// 注意：这是 controller 层的粗检，biz 层的 CheckAndEstimate 才是权威检查。
func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
	if isEffectiveLegacy(user) {
		// legacy 会员（含 billing_mode=credits 但会员仍在期的过渡用户）：
		// SOP 走 CanRunSOP（次数 + 过期检查），非 SOP 不限制
		if IsSopOperation(operation) {
			return user.CanRunSOP()
		}
		return true, ""
	}

	// credits 模式：粗粒度余额预检
	estimated := GetEstimatedCredits(operation)

	// credits-deduct-cycle-wiring T7: prefer MembershipService (reads
	// credit_cycle + user_booster_balance + trial_grant) over the legacy
	// credit_account.balance read which returns 0 for new-grant users.
	if b.membershipSvc != nil {
		view, err := b.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
		if err != nil {
			log.Errorw("CanPerformAIOperation: membershipSvc.GetBalance failed", "user_id", user.ID, "err", err)
			return false, "积分余额查询失败，请稍后重试"
		}
		total := view.CycleRemaining + view.TrialRemaining + view.BoosterUsable
		if total < int64(estimated) {
			return false, "积分不足，请充值积分"
		}
		return true, ""
	}

	// Fallback: legacy credit_account path (test-only after T0 wiring lands).
	balance, err := b.ds.Credits().GetBalance(ctx, user.ID)
	if err != nil {
		log.Errorw("Failed to get credit balance", "user_id", user.ID, "error", err)
		return false, "积分余额查询失败，请稍后重试"
	}

	if balance < estimated {
		return false, "积分不足，请充值积分"
	}

	return true, ""
}

// DeductCredits 扣减积分（FIFO 按到期时间顺序扣减积分包）
//
// Legacy wrapper kept for backward compatibility (sop.go:1825, sales_rag.go:1170).
// The Transaction body lives in deductCreditsTxFull so that DeductCreditsTx
// (used by ICreditService.Reserve) and this function share a single
// implementation — spec §3.10.
//
// Existing semantics preserved:
//   - opens its own transaction
//   - costCents ≈ credits 1:1 (math.Round)
//   - tolerates insufficient balance by deducting what is available and
//     logging a warning (does NOT return an error, unlike DeductCreditsTx).
//     This legacy "best-effort" behaviour is retained to avoid breaking
//     existing fire-and-forget callers; new code should use DeductCreditsTx
//     which returns ErrInsufficientCredits explicitly.
//
// NOTE (Task 7 / §3.5): For billing_mode=credits users the authoritative deduction
// path is MembershipService.DeductCredits (biz/membership/cycle.go), which applies
// the three-pool priority (trial → cycle → booster). Routing between the two paths
// at the call site (sop.go / sales_rag.go) will be wired in Task 16 (cleanup).
// This function remains the active path for billing_mode=legacy_tier callers.
func (b *creditBiz) DeductCredits(ctx context.Context, userID uint, costCents int64, operation, bizRefType, bizRefID string, usageRecordID *uint64) error {
	credits := int64(math.Round(float64(costCents)))
	if credits <= 0 {
		return nil
	}

	// 确保账户存在
	if _, err := b.ds.Credits().GetOrCreateAccount(ctx, userID); err != nil {
		return fmt.Errorf("ensure credit account: %w", err)
	}

	// 在事务中执行扣减（best-effort：余额不足时仅记录 warn，不回滚）
	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := b.deductCreditsTxFull(ctx, tx, userID, credits,
			operation, bizRefType, bizRefID, usageRecordID, true /* allowPartial */)
		return err
	})
}

// DeductCreditsTx is the transactional composable variant used by Reserve.
// It writes CreditTransaction rows with Operation=reason and empty BizRef /
// nil UsageRecordID — Reserve persists the richer metadata (reference_type,
// reference_id, coefficient_id, idempotency_key) on the credit_reservation row
// rather than spreading it across each debit item.
//
// Callers are responsible for ensuring the user's CreditAccount row exists
// BEFORE opening the outer transaction (call GetOrCreateAccount first). This
// avoids connection-pool deadlocks when a caller holds a tx and needs to open
// a second connection to create the account.
func (b *creditBiz) DeductCreditsTx(ctx context.Context, tx *gorm.DB, userID uint, credits int64, reason string) ([]PackageDeduction, error) {
	if credits <= 0 {
		return nil, nil
	}
	return b.deductCreditsTxFull(ctx, tx, userID, credits, reason, "", "", nil, false /* allowPartial */)
}

// deductCreditsTxFull is the single source of truth for FIFO credit deduction
// inside a caller-managed transaction. Parameters:
//   - allowPartial=true preserves legacy DeductCredits semantics (best-effort,
//     logs warn on insufficient)
//   - allowPartial=false returns ErrInsufficientCredits when the total balance
//     is short (Reserve path — caller rolls back outer tx)
//
// Returned []PackageDeduction is ordered by FIFO debit sequence; Reserve uses
// this to emit credit_reservation_item rows with seq=idx+1.
func (b *creditBiz) deductCreditsTxFull(
	ctx context.Context, tx *gorm.DB, userID uint, credits int64,
	operation, bizRefType, bizRefID string, usageRecordID *uint64,
	allowPartial bool,
) ([]PackageDeduction, error) {
	// 获取有效积分包（FIFO by ExpiresAt，加行锁）
	packages, err := b.ds.Credits().GetActivePackagesForUpdate(ctx, tx, userID)
	if err != nil {
		return nil, fmt.Errorf("get active packages: %w", err)
	}

	items := make([]PackageDeduction, 0, len(packages))
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
			return nil, fmt.Errorf("update package %d: %w", pkg.ID, err)
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
			return nil, fmt.Errorf("create transaction: %w", err)
		}

		items = append(items, PackageDeduction{
			PackageID:   pkg.ID,
			Credits:     deduct,
			PackageType: pkg.Type,
			ExpiresAt:   pkg.ExpiresAt,
		})

		remaining -= deduct
	}

	// Insufficient handling: Reserve path returns error + rolls back; legacy
	// DeductCredits path logs + continues (already-written partial deductions
	// are persisted).
	if remaining > 0 {
		if !allowPartial {
			return nil, fmt.Errorf("%w: requested %d, available %d",
				ErrInsufficientCredits, credits, credits-remaining)
		}
		log.Warnw("Partial credit deduction (insufficient balance)",
			"user_id", userID, "requested", credits, "actual", credits-remaining,
			"operation", operation)
	}

	// 只扣减实际消耗的积分，避免余额变负
	actualDeducted := credits - remaining
	if actualDeducted > 0 {
		if err := b.ds.Credits().UpdateBalance(ctx, tx, userID, -actualDeducted); err != nil {
			return nil, fmt.Errorf("update balance: %w", err)
		}
	}

	// 如果有 usageRecordID，更新 UsageRecord 的 credits_deducted
	if usageRecordID != nil {
		if err := tx.Model(&model.UsageRecord{}).Where("id = ?", *usageRecordID).
			Update("credits_deducted", actualDeducted).Error; err != nil {
			return nil, fmt.Errorf("update usage record credits_deducted: %w", err)
		}
	}

	return items, nil
}

// GetBalance 获取用户额度余额
func (b *creditBiz) GetBalance(ctx context.Context, userID uint) (int64, error) {
	return b.ds.Credits().GetBalance(ctx, userID)
}

// GetQuotaBreakdown 获取用户额度分布（订阅 vs 加量包）
func (b *creditBiz) GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error) {
	return b.ds.Credits().GetQuotaBreakdown(ctx, userID)
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
		// months 参数在 booster 路径中语义为 quantity（购买份数），每份 600 积分。
		// 每份独立创建一个 CreditPackage，各自有独立的 90 天到期时间（FIFO 扣减）。
		quantity := months
		if quantity < 1 {
			quantity = 1
		}
		for i := 0; i < quantity; i++ {
			packages = append(packages, model.CreditPackage{
				UserID:        userID,
				Type:          model.CreditTypeBooster,
				TotalCredits:  600,
				RemainCredits: 600,
				ActivatedAt:   now,
				ExpiresAt:     now.Add(90 * 24 * time.Hour),
				OrderID:       &orderID,
				Status:        model.CreditPackageActive,
			})
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

// RunCronTasks is deprecated (Task 16 / Spec §10 cleanup).
//
// The credit_package lifecycle (ActivatePending / ExpireActive / RecalculateBalance)
// and the billing_mode reconciliation (reconcileBillingMode) are no longer driven
// by a time-based cron: the new membership model uses the Subscriptions /
// TrialGrants / CreditCycles tables which are event-driven. The cron ticker in
// server.go has been removed; this method is retained as a no-op so that
// ICreditBiz mock implementations in tests do not require changes.
func (b *creditBiz) RunCronTasks(_ context.Context) error {
	return nil
}
