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
// **HTTP-path-bypass note**：本方法定义在 ICreditBiz interface，但 HTTP 路由
// POST /v1/users/children/:child_id/grant-membership 直接调 MembershipService
// **跳过本方法**（见 controller/v1/credit/grant_membership.go）。本方法仅为单测
// 覆盖 orchestration 逻辑；T6+ 可选择把 controller 改路由经 biz，或废弃本方法。
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
//
// **Atomicity 3-step ordering（round-1 review 修复）**：
//
//	全过程拆成 3 个原子边界，每一步可独立失败而不留半成品：
//	 Step A (pre-grant): flip billing_mode legacy_tier→credits（自带小 tx，幂等
//	                     — 已 credits 即 no-op；失败 = 整个请求失败，无任何副作用）
//	 Step B (grant)    : 调 MembershipService（自带 tx，写 subscription/trial_grant
//	                     + membership_event）。失败 = billing_mode 已切（user 无 credits
//	                     可用但状态一致）；spec 允许此降级（用户可重试）。
//	 Step C (audit log): 写 action_log（独立 tx）。失败时 **Warn 但不返回错误** —
//	                     action_log 是 admin UI 审计 trail；B2B 月结报表实际读
//	                     membership_event（已在 Step B 成功），与 action_log 失败无关。
//	                     完全失败语义违反原子性更糟（会造成 grant 已写但报错重试）。
//	 顺序为什么不能反：若先 Step B 再 Step A，B 成功 A 失败 → 用户拿到 grant 但
//	                     billing_mode 仍是 legacy_tier，扣费走旧路径无法消费 cycle credits。
func (b *creditBiz) GrantMembership(ctx context.Context, req GrantMembershipReq) error {
	// Guard: membershipSvc 必须已通过 InjectCreditBizMembershipSvc 注入。
	// 缺失说明 wiring 错误（NewBiz 没调注入函数 / 测试 setup 漏掉）— 直接报错
	// 而非 nil-pointer panic。
	if b.membershipSvc == nil {
		return fmt.Errorf("GrantMembership: membershipSvc not wired (test/build setup issue)")
	}

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

	now := time.Now()
	granterID := uint64(req.ParentUserID)
	childID64 := uint64(req.ChildUserID)

	// Step A (pre-grant): flip billing_mode legacy_tier→credits BEFORE granting.
	// Idempotent: WHERE billing_mode='legacy_tier' guard makes "already credits" a no-op.
	// Safe to fail here — nothing else has been written yet, the request just fails.
	// Reason ordering: if we flipped AFTER grant, a flip-failure would leave the user
	// with grant rows but legacy billing_mode, breaking credit consumption.
	if err := b.ds.DB().WithContext(ctx).
		Model(&model.User{}).
		Where("id = ? AND billing_mode = ?", req.ChildUserID, model.BillingModeLegacyTier).
		Update("billing_mode", model.BillingModeCredits).Error; err != nil {
		return fmt.Errorf("GrantMembership: switch billing_mode: %w", err)
	}

	// Step B (grant): dispatch to MembershipService (writes new tables, no credit_package INSERT).
	// MembershipService manages its own internal tx for subscription/trial_grant/membership_event.
	// Safe to fail here — billing_mode is already credits (consistent state, just no credits granted
	// yet); the user may retry.
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

	// Step C (post-grant audit log): write action_log for admin UI audit trail.
	// Trade-off (documented): action_log failure is logged Warn but does NOT fail the whole
	// request. Rationale:
	//   - action_log is the admin UI audit trail only. The B2B monthly billing report
	//     (biz/b2b_billing/b2b_billing.go) reads membership_event directly (already
	//     committed in Step B) and does NOT depend on action_log — so action_log loss
	//     has no billing impact.
	//   - Failing the request here would force the caller to retry, but Step B is not
	//     idempotent at the biz level (second call returns ErrGrantTrialAlreadyPurchased
	//     / ErrGrantActiveSubscription) — caller would see 409 on retry, masking the
	//     real cause.
	// Run in its own small tx for crash-safety (action_log row is atomic; either it
	// commits fully or it doesn't appear at all).
	if logErr := b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	}); logErr != nil {
		// Degraded but recoverable: primary audit (membership_event) is intact.
		log.Warnw("GrantMembership: action_log write failed (grant itself succeeded; "+
			"membership_event remains as primary audit source for B2B billing reconciliation)",
			"parent_user_id", req.ParentUserID,
			"child_user_id", req.ChildUserID,
			"product_type", req.ProductType,
			"err", logErr,
		)
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
