package credit

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	membershipmodel "numind-server/internal/pkg/model/membership"
)

// ICreditBiz 积分业务逻辑接口
//
// T6 (credits-cleanup): the legacy DeductCreditsTx + DeductCredits + RunCronTasks
// chain has been deleted. All credit deduction now goes through
// membership.MembershipService.DeductCreditsTx (biz/membership/cycle.go), which
// reads/writes the new credit_cycle / user_booster_balance / trial_grant tables.
// The old credit_package FIFO deduction (with GetActivePackagesForUpdate +
// UpdatePackage) is gone.
type ICreditBiz interface {
	CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string)
	GetBalance(ctx context.Context, userID uint) (int64, error)
	// RechargeWithOrderTx accepts ONLY productType="booster" since T5.
	// Other product types return ErrUnsupportedProductType.
	// Note: this function is dead code on the live HTTP path — fulfillOrder in
	// payment.go writes new tables directly. Kept for interface stability.
	// TODO(T11): drop this method from ICreditBiz once the credits-cleanup
	// arc lands and no callers remain (verified by `grep RechargeWithOrderTx`
	// — only test files exercise it post-T5).
	RechargeWithOrderTx(ctx context.Context, tx *gorm.DB, userID uint, orderID uint64, productType string, months int) error
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
// 走 credits 模式：MembershipService 读 credit_cycle + user_booster_balance +
// trial_grant 三池，粗粒度余额预检。注意：这是 controller 层的粗检，biz 层的
// CheckAndEstimate 才是权威检查。
//
// Post legacy-deprecation (T1) the legacy_tier branch and the fallback
// credit_account.GetBalance path are gone; membershipSvc is always wired in
// production via biz.NewBiz, so a nil here is a wiring bug.
func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, operation string) (bool, string) {
	if b.membershipSvc == nil {
		log.Errorw("CanPerformAIOperation: membershipSvc not wired", "user_id", user.ID)
		return false, "积分系统初始化错误，请联系管理员"
	}

	estimated := GetEstimatedCredits(operation)
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
