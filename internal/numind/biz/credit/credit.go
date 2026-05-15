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
	membershipmodel "numind-server/internal/pkg/model/membership"
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

		// 创建流水记录（T1: 同时填 source_type=credit_package.Type, source_id=credit_package.ID
		// 保证 credit_package DROP 后历史 ledger 仍可区分 trial/cycle/booster 来源）
		pkgType := pkg.Type
		pkgID := pkg.ID
		txn := &model.CreditTransaction{
			UserID:        userID,
			PackageID:     pkg.ID,
			SourceType:    &pkgType,
			SourceID:      &pkgID,
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

// RechargeWithOrderTx is the payment-callback fulfillment hook for the booster
// product type. It writes to new membership tables only (no credit_package INSERT):
//
//   - user_booster_balance: incremented by quantity×600 credits (upsert)
//   - membership_event: append-only audit log row (idempotency_key = orderNo)
//
// T5 cleanup (membership-credits-redesign): the trial / monthly / yearly branches
// that used to create credit_package rows have been deleted. Those product types
// are exclusively handled by the B2B grant path (GrantMembership /
// MembershipService). The payment order layer (fulfillOrder in payment.go) already
// enforces product_type=booster via a §5.10 guard BEFORE calling this function, so
// the defensive error below is production-unreachable — it guards against future
// callers bypassing that guard.
//
// The `months` parameter for booster carries the purchased quantity (number of
// booster packs, each 600 credits). This repurposed semantic is retained from the
// original order-creation contract; see CreateOrder and fulfillOrder in payment.go.
func (b *creditBiz) RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error {
	// Defensive guard: only booster is supported via the payment callback.
	// Trial/monthly/yearly go through the B2B grant path (spec §5.10).
	if productType != model.ProductTypeBooster {
		return fmt.Errorf("%w: got %q", ErrUnsupportedProductType, productType)
	}

	// months parameter repurposed as booster quantity (spec §5.2 / CreateOrder).
	quantity := months
	if quantity < 1 {
		quantity = 1
	}
	delta := int64(quantity) * 600 // 600 credits per booster unit (spec §5.2)

	// Increment booster balance (upsert — creates row if not exists).
	if err := b.ds.Membership().BoosterBalances().Increment(ctx, tx, uint64(userID), delta); err != nil {
		return fmt.Errorf("increment booster balance: %w", err)
	}

	// Write membership audit event (idempotency_key = order ID string).
	now := time.Now()
	orderIDStr := fmt.Sprintf("order-%d", orderID)
	qty := uint16(quantity)               //nolint:gosec // quantity ∈ [1,10000], fits uint16
	amountCents := int64(quantity) * 2990 // ¥29.9 per unit (spec §5.2)
	event := &membershipmodel.MembershipEvent{
		UserID:         uint64(userID),
		EventType:      membershipmodel.EventTypeBoosterGranted,
		ProductType:    model.ProductTypeBooster,
		Quantity:       &qty,
		AmountCents:    amountCents,
		Source:         membershipmodel.SourceSelfPurchase,
		IdempotencyKey: &orderIDStr,
		OccurredAt:     now,
	}
	if err := b.ds.Membership().Events().Create(ctx, tx, event); err != nil {
		return fmt.Errorf("create membership event: %w", err)
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
