package credit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// GrantMembership 相关哨兵错误。Controller 层使用 errors.Is 识别后
// 映射到对应 HTTP 状态 (400/403/404/409)，避免全部落到 500。
var (
	// ErrGrantChildNotFound 子账户不存在。映射 HTTP 404。
	ErrGrantChildNotFound = errors.New("grant: child user not found")
	// ErrGrantForbidden 子账户不属于当前父账户。映射 HTTP 403。
	ErrGrantForbidden = errors.New("grant: child does not belong to caller")
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
// 与 RechargeWithOrderTx 的差异:
//   - 不关联 Order (OrderID 保持 NULL)
//   - credit_package.grant_source='b2b_grant', granter_user_id=ParentUserID
//   - 记录 action_log 以供 B2B 月度结算报表审计
//
// 校验链:
//  1. ChildUser 存在且 ChildUser.ParentUserID == ParentUserID（防越权）
//  2. ProductType 支持性 (trial / monthly)；monthly 要求 months ∈ [1,12]
//  3. trial 防重复（lifetime 单次）；monthly 防重复开通在期订阅（spec §3.9 防提前续费）
//  4. billing_mode legacy_tier → credits 切换（同 RechargeWithOrderTx 触发条件）
//
// 所有写操作在同一 transaction 中完成，失败自动 rollback。
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

	// Step 3: anti-duplicate checks (mirror payment.CreateOrder Trial/Monthly guards)
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

	// Step 4: ensure credit account exists (outside tx — GetOrCreateAccount uses its own connection)
	if _, err := b.ds.Credits().GetOrCreateAccount(ctx, req.ChildUserID); err != nil {
		return fmt.Errorf("GrantMembership: ensure credit account: %w", err)
	}

	// Step 5: build packages + run in a single transaction (create packages, bump balance, switch billing_mode, log audit)
	now := time.Now()
	packages, err := buildGrantPackages(req, now)
	if err != nil {
		return err
	}

	err = b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Insert packages
		if err := tx.Create(&packages).Error; err != nil {
			return fmt.Errorf("create credit packages: %w", err)
		}

		// Sum immediately-active credits for balance bump
		var immediate int64
		for _, p := range packages {
			if p.Status == model.CreditPackageActive {
				immediate += p.TotalCredits
			}
		}
		if immediate > 0 {
			if err := b.ds.Credits().UpdateBalance(ctx, tx, req.ChildUserID, immediate); err != nil {
				return fmt.Errorf("update balance: %w", err)
			}
		}

		// Switch billing_mode legacy_tier → credits (same trigger as RechargeWithOrderTx path,
		// spec §3.8). Guarded by WHERE billing_mode='legacy_tier' so no-op for credits users.
		if err := tx.Model(&model.User{}).
			Where("id = ? AND billing_mode = ?", req.ChildUserID, model.BillingModeLegacyTier).
			Update("billing_mode", model.BillingModeCredits).Error; err != nil {
			return fmt.Errorf("switch billing_mode: %w", err)
		}

		// Write action log for B2B billing report audit
		detail := map[string]interface{}{
			"product_type": req.ProductType,
			"months":       req.Months,
			"reason":       req.Reason,
			"package_ids":  collectPackageIDs(packages),
		}
		detailJSON, _ := json.Marshal(detail)
		childID := req.ChildUserID
		actionLog := &model.ActionLogM{
			UserID:    req.ParentUserID,
			Action:    "grant_membership",
			Target:    "user",
			TargetID:  &childID,
			Detail:    string(detailJSON),
			CreatedAt: now,
		}
		if err := tx.Create(actionLog).Error; err != nil {
			return fmt.Errorf("write action log: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("GrantMembership: %w", err)
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

// buildGrantPackages constructs the credit_package rows for a grant request.
// Mirrors the trial / monthly branches of RechargeWithOrderTx but stamps
// GrantSource='b2b_grant' + GranterUserID and leaves OrderID nil.
func buildGrantPackages(req GrantMembershipReq, now time.Time) ([]model.CreditPackage, error) {
	granter := req.ParentUserID

	switch req.ProductType {
	case model.ProductTypeTrial:
		return []model.CreditPackage{
			{
				UserID:        req.ChildUserID,
				Type:          model.CreditTypeTrial,
				TotalCredits:  200,
				RemainCredits: 200,
				ActivatedAt:   now,
				ExpiresAt:     now.Add(3 * 24 * time.Hour),
				Status:        model.CreditPackageActive,
				GrantSource:   model.GrantSourceB2BGrant,
				GranterUserID: &granter,
			},
		}, nil
	case model.ProductTypeMonthly:
		m := req.Months
		pkgs := make([]model.CreditPackage, 0, m)
		for i := 0; i < m; i++ {
			status := model.CreditPackagePending
			if i == 0 {
				status = model.CreditPackageActive
			}
			pkgs = append(pkgs, model.CreditPackage{
				UserID:        req.ChildUserID,
				Type:          model.CreditTypeSubscription,
				TotalCredits:  2000,
				RemainCredits: 2000,
				ActivatedAt:   now.AddDate(0, i, 0),
				ExpiresAt:     now.AddDate(0, i+1, 0),
				Status:        status,
				GrantSource:   model.GrantSourceB2BGrant,
				GranterUserID: &granter,
			})
		}
		return pkgs, nil
	default:
		// validateGrantProductType already covers this — defensive.
		return nil, fmt.Errorf("GrantMembership: unreachable product_type %q", req.ProductType)
	}
}

func collectPackageIDs(pkgs []model.CreditPackage) []uint64 {
	ids := make([]uint64, 0, len(pkgs))
	for _, p := range pkgs {
		ids = append(ids, p.ID)
	}
	return ids
}
