package credit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// GrantMembership 相关哨兵错误。Controller 层使用 errors.Is 识别后
// 映射到对应 HTTP 状态 (400/403/404/409)，避免全部落到 500。
var (
	// ErrGrantChildNotFound 子账户不存在。映射 HTTP 404。
	ErrGrantChildNotFound = errors.New("grant: child user not found")
	// ErrGrantForbidden 调用者无权为目标账户开通会员（跨父越权 / 子账户自开通）。映射 HTTP 403。
	ErrGrantForbidden = errors.New("grant: caller does not have permission to grant to target")
	// ErrGrantInvalidProductType 不支持的产品类型（yearly/booster/其它）。映射 HTTP 400。
	ErrGrantInvalidProductType = errors.New("grant: product_type not supported by grant path")
	// ErrGrantInvalidMonths monthly 的 months 参数越界。映射 HTTP 400。
	ErrGrantInvalidMonths = errors.New("grant: months must be in [1,12]")
	// ErrGrantTrialAlreadyPurchased 子账户已用过 trial（lifetime 限一次）。映射 HTTP 409。
	ErrGrantTrialAlreadyPurchased = errors.New("grant: child already consumed trial (lifetime single-use)")
	// ErrGrantActiveSubscription 子账户已有在期订阅，不能重复开通。映射 HTTP 409。
	ErrGrantActiveSubscription = errors.New("grant: child already has an active subscription")
)

// GrantMembershipReq 父账户（B 端）帮子账户开通会员的请求参数。
//
// 此路径不走支付流程（spec Q1 B2B2C）: 父账户通过"帮开通"功能赋予子账户
// 会员，月末对公转账结算。因此 credit_package.order_id 保持为 NULL，
// grant_source='b2b_grant'，granter_user_id=ParentUserID。
type GrantMembershipReq struct {
	// ParentUserID 调用方 ID（已由 auth middleware 解析）。
	ParentUserID uint
	// ChildUserID 目标子账户 ID；必须满足 child.parent_user_id == ParentUserID。
	ChildUserID uint
	// ProductType 支持 "trial" / "monthly"；yearly 保留未来扩展，booster 不走此路径。
	ProductType string
	// Months 仅 monthly 使用，取值 1-12；trial 固定 3 天，忽略此字段。
	Months int
	// Reason 审计理由（写入 action_log.detail）。
	Reason string
}

// GrantMembership 由父账户为子账户赋予会员（B2B2C 路径，不走支付）。
//
// T4 重写（membership-credits-redesign cleanup）：
//   - trial  → 调 MembershipService.GrantTrial（写 trial_grant 新表）
//   - monthly → 调 MembershipService.GrantOrRenewSubscription（写 subscription 新表）
//   - 不再写 credit_package（INSERT 路径已切走）
//   - 不再调 UpdateBalance（credit_account.balance 将在 T11 废弃）
//   - 保留 billing_mode legacy_tier→credits 切换（MembershipService 不做此操作）
//   - 保留 action_log 写入（B2B 月结审计需要）
//
// 与 RechargeWithOrderTx 的差异:
//   - 不关联 Order (OrderID 保持 NULL)
//   - grant_source='b2b_grant', granter_user_id=ParentUserID
//   - 记录 action_log 以供 B2B 月度结算报表审计
//
// 校验链:
//  1. ChildUser 存在且 ChildUser.ParentUserID == ParentUserID（防越权）
//  2. ProductType 支持性 (trial / monthly)；monthly 要求 months ∈ [1,12]
//  3. trial 防重复（lifetime 单次）；monthly 防重复开通在期订阅（spec §3.9 防提前续费）
//  4. billing_mode legacy_tier → credits 切换（同 RechargeWithOrderTx 触发条件）
//
// Guard reader 切换（T4 atomicity）：
//
//	HasActiveSubscription 和 HasTrialPackage 现在读 subscription / trial_grant 新表，
//	与 MembershipService 写路径对齐，确保 trial lifetime 保护不失效。
func (b *creditBiz) GrantMembership(ctx context.Context, req GrantMembershipReq) error {
	// Step 1: validate product type early (cheap)
	if err := validateGrantProductType(req); err != nil {
		return err
	}

	// Step 2: verify parent-child relationship (spec Q1: child must belong to caller)
	child, err := b.ds.Users().GetByID(ctx, req.ChildUserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: child_id=%d", ErrGrantChildNotFound, req.ChildUserID)
		}
		return fmt.Errorf("GrantMembership: get child user %d: %w", req.ChildUserID, err)
	}
	if req.ChildUserID == req.ParentUserID {
		// Self-grant: 仅允许父账户（parent_user_id IS NULL）给自己开通。
		// 子账户 (parent_user_id != NULL) 禁止自开通，防越权。
		if child.ParentUserID != nil {
			return fmt.Errorf("%w: caller=%d is a sub-user, self-grant only allowed for parent accounts",
				ErrGrantForbidden, req.ParentUserID)
		}
		// 放行：父账户 self-grant
	} else {
		// Delegate-grant: 目标必须是 caller 的子账户
		if child.ParentUserID == nil || *child.ParentUserID != req.ParentUserID {
			return fmt.Errorf("%w: child=%d parent=%d", ErrGrantForbidden, req.ChildUserID, req.ParentUserID)
		}
	}

	// Step 3: anti-duplicate checks (guard readers now read new tables — T4 atomicity)
	switch req.ProductType {
	case model.ProductTypeTrial:
		// Spec §3.9: trial not available in-period — if child has an active subscription, reject.
		hasActive, err := b.ds.Credits().HasActiveSubscription(ctx, req.ChildUserID)
		if err != nil {
			return fmt.Errorf("GrantMembership: check active subscription for trial: %w", err)
		}
		if hasActive {
			return fmt.Errorf("%w: child=%d (active subscription blocks trial)", ErrGrantActiveSubscription, req.ChildUserID)
		}
		hasTrial, err := b.ds.Credits().HasTrialPackage(ctx, req.ChildUserID)
		if err != nil {
			return fmt.Errorf("GrantMembership: check trial package: %w", err)
		}
		if hasTrial {
			return fmt.Errorf("%w: child=%d", ErrGrantTrialAlreadyPurchased, req.ChildUserID)
		}
	case model.ProductTypeMonthly:
		hasActive, err := b.ds.Credits().HasActiveSubscription(ctx, req.ChildUserID)
		if err != nil {
			return fmt.Errorf("GrantMembership: check active subscription: %w", err)
		}
		if hasActive {
			return fmt.Errorf("%w: child=%d", ErrGrantActiveSubscription, req.ChildUserID)
		}
	}

	// Step 4: dispatch to MembershipService (writes new tables, no credit_package INSERT)
	now := time.Now()
	granterID := uint64(req.ParentUserID)
	childID64 := uint64(req.ChildUserID)

	switch req.ProductType {
	case model.ProductTypeTrial:
		// MembershipService.GrantTrial performs its own transactional write to trial_grant.
		// No idempotency key is passed here — the calling layer (controller) is expected
		// to be idempotent at the HTTP level. If req carries an idempotency key in future,
		// wire it here.
		_, err := b.membershipSvc.GrantTrial(ctx, membership.GrantTrialRequest{
			UserID:        childID64,
			GranterUserID: &granterID,
			Now:           now,
		})
		if err != nil {
			return fmt.Errorf("GrantMembership: trial: %w", err)
		}

	case model.ProductTypeMonthly:
		// For self-grant (ParentUserID == ChildUserID), pass parentForValidation=0 to bypass
		// ErrMembershipSelfPurchaseDisabled (which guards against C-end self-purchase, not
		// B2B admin self-grant). See GrantOrRenewSubscription.validateSubscriptionInput.
		parentForValidation := uint64(req.ParentUserID)
		if req.ParentUserID == req.ChildUserID {
			parentForValidation = 0 // bypass self-purchase check for parent self-grant
		}
		_, err := b.membershipSvc.GrantOrRenewSubscription(ctx, membership.GrantSubscriptionRequest{
			ParentUserID:  parentForValidation,
			UserID:        childID64,
			ProductType:   "monthly",
			Months:        req.Months,
			GranterUserID: &granterID,
			Now:           now,
		})
		if err != nil {
			return fmt.Errorf("GrantMembership: subscription: %w", err)
		}
	}

	// Step 5: post-grant side effects in a single DB transaction:
	//   a. Switch billing_mode legacy_tier → credits (MembershipService does not do this)
	//   b. Write action_log for B2B billing report audit
	err = b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// a. Switch billing_mode legacy_tier → credits.
		// Guarded by WHERE billing_mode='legacy_tier' so no-op for credits users.
		if err := tx.Model(&model.User{}).
			Where("id = ? AND billing_mode = ?", req.ChildUserID, model.BillingModeLegacyTier).
			Update("billing_mode", model.BillingModeCredits).Error; err != nil {
			return fmt.Errorf("switch billing_mode: %w", err)
		}

		// b. Write action log for B2B billing report audit.
		detail := map[string]interface{}{
			"product_type": req.ProductType,
			"months":       req.Months,
			"reason":       req.Reason,
		}
		detailJSON, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("marshal action log detail: %w", err)
		}
		childIDPtr := req.ChildUserID
		actionLog := &model.ActionLogM{
			UserID:    req.ParentUserID,
			Action:    "grant_membership",
			Target:    "user",
			TargetID:  &childIDPtr,
			Detail:    string(detailJSON),
			CreatedAt: now,
		}
		if err := tx.Create(actionLog).Error; err != nil {
			return fmt.Errorf("write action log: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("GrantMembership: post-grant side effects: %w", err)
	}

	log.Infow("B2B membership granted",
		"parent_user_id", req.ParentUserID,
		"child_user_id", req.ChildUserID,
		"product_type", req.ProductType,
		"months", req.Months,
		"reason", req.Reason,
	)
	return nil
}

// validateGrantProductType restricts grant to trial/monthly only.
// yearly is reserved for future; booster is self-purchase (C-end) only.
func validateGrantProductType(req GrantMembershipReq) error {
	switch req.ProductType {
	case model.ProductTypeTrial:
		// months ignored for trial (固定 3 天)
		return nil
	case model.ProductTypeMonthly:
		if req.Months < 1 || req.Months > 12 {
			return fmt.Errorf("%w: got %d", ErrGrantInvalidMonths, req.Months)
		}
		return nil
	default:
		return fmt.Errorf("%w: got %q (allowed: trial, monthly)", ErrGrantInvalidProductType, req.ProductType)
	}
}
