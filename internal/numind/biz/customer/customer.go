package customer

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// ICustomerBiz 客户业务逻辑接口
type ICustomerBiz interface {
	// 子用户管理
	ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) (*v1.ListSubUsersResponse, error)
	GetSubUserDetail(ctx context.Context, parentUserID, subUserID uint) (*v1.SubUserDetailResponse, error)

	// 权限管理
	GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	BatchGrantTemplates(ctx context.Context, parentUserID uint, userIDs, templateIDs []uint) error
	RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	BatchRevokeTemplates(ctx context.Context, parentUserID uint, userIDs, templateIDs []uint) error
	CheckTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error)

	// 运行统计
	GetCustomerStatistics(ctx context.Context, userID uint) (*v1.CustomerStatisticsResponse, error)

	// 等级管理
	UpdateSubUserTier(ctx context.Context, parentUserID, subUserID uint, req *v1.UpdateTierRequest) error

	// 功能权限管理
	CheckFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error)
	GrantFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error
	RevokeFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error
	ListUserFeatures(ctx context.Context, parentUserID, subUserID uint) ([]string, error)

	// Chatbot 运行权限管理（child-run-permission spec §3.5）
	CheckChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error)
	ListSubUserChatbots(ctx context.Context, parentUserID, subUserID uint) ([]model.ChatbotConfig, error)
	GrantChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error
	RevokeChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error
	BatchGrantChatbots(ctx context.Context, parentUserID uint, subUserIDs, chatbotIDs []uint) error
	BatchRevokeChatbots(ctx context.Context, parentUserID uint, subUserIDs, chatbotIDs []uint) error
}

type customerBiz struct {
	ds            store.IStore
	membershipSvc *membership.MembershipService
}

var _ ICustomerBiz = (*customerBiz)(nil)

func New(ds store.IStore, opts ...func(*customerBiz)) ICustomerBiz {
	cb := &customerBiz{ds: ds}
	for _, opt := range opts {
		opt(cb)
	}
	return cb
}

// WithMembershipSvc returns an option that injects a MembershipService.
func WithMembershipSvc(svc *membership.MembershipService) func(*customerBiz) {
	return func(cb *customerBiz) {
		cb.membershipSvc = svc
	}
}

// ListSubUsers 获取二级客户列表
func (c *customerBiz) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) (*v1.ListSubUsersResponse, error) {
	users, total, err := c.ds.Customers().ListSubUsers(ctx, parentUserID, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list sub users", "parent_user_id", parentUserID, "err", err)
		return nil, err
	}

	now := time.Now().UTC()

	// 批量收集 userID，从新 membership 系统查询会员状态
	userIDs64 := make([]uint64, 0, len(users))
	for _, u := range users {
		userIDs64 = append(userIDs64, uint64(u.ID))
	}

	var msBatch map[uint64]*membership.BatchMembershipState
	if c.membershipSvc != nil {
		msBatch, err = c.membershipSvc.GetMembershipStateBatch(ctx, userIDs64, now)
		if err != nil {
			log.C(ctx).Warnw("membershipSvc.GetMembershipStateBatch failed, will use zero values", "err", err)
			msBatch = map[uint64]*membership.BatchMembershipState{}
		}
	} else {
		msBatch = map[uint64]*membership.BatchMembershipState{}
	}

	// 转换为响应格式
	subUsers := make([]v1.SubUserInfo, 0, len(users))
	for _, user := range users {
		// Why: 走 GetAuthorizedTemplates + 手动过滤 active 而非 COUNT(*) — 与 GetSubUserDetail
		// 保持一致语义（同一个 store 方法 + 同一份 active 过滤逻辑），避免两个端点对"已授权模板"
		// 给出不同的数字。
		templates, _ := c.ds.Customers().GetAuthorizedTemplates(ctx, user.ID)
		activeTemplateCount := 0
		for _, t := range templates {
			if t.Status == "active" {
				activeTemplateCount++
			}
		}

		expiresStr := ""
		if user.TierExpires != nil {
			expiresStr = user.TierExpires.Format("2006-01-02")
		}

		ms := msBatch[uint64(user.ID)]
		var membershipState v1.SubUserMembershipState
		var hasUsedTrial bool
		var cycleRemaining int64
		if ms != nil {
			membershipState = v1.SubUserMembershipState{
				HasActiveTrial:        ms.HasActiveTrial,
				HasActiveSubscription: ms.HasActiveSubscription,
			}
			if ms.TrialExpiresAt != nil {
				membershipState.TrialExpiresAt = *ms.TrialExpiresAt
			}
			if ms.SubscriptionExpiresAt != nil {
				membershipState.SubscriptionExpiresAt = *ms.SubscriptionExpiresAt
			}
			hasUsedTrial = ms.HasUsedTrial
			cycleRemaining = ms.CycleRemaining
		}

		// credit_balance / credit_expires 是给前端兜底用的快捷字段。
		// credits-mode 用户的真实数据在 msBatch 里（新表），不能再读 credit_account /
		// credit_package（grant 路径已经不再写这两张表，读出来是 0 / 空串）。
		var creditBalance int64
		var creditExpires string
		if user.BillingMode == model.BillingModeCredits {
			if ms != nil {
				creditBalance = ms.CycleRemaining
				if ms.SubscriptionExpiresAt != nil {
					if t, err := time.Parse(time.RFC3339, *ms.SubscriptionExpiresAt); err == nil {
						creditExpires = t.Format("2006-01-02")
					}
				} else if ms.TrialExpiresAt != nil {
					if t, err := time.Parse(time.RFC3339, *ms.TrialExpiresAt); err == nil {
						creditExpires = t.Format("2006-01-02")
					}
				}
			}
		} else {
			creditBalance, _ = c.ds.Credits().GetBalance(ctx, user.ID)
			creditExpires, _ = c.ds.Credits().GetLatestCreditExpiry(ctx, user.ID)
		}

		subUsers = append(subUsers, v1.SubUserInfo{
			UserID:              user.ID,
			Username:            user.Username,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			Avatar:              user.AvatarURL,
			TotalSopRuns:        user.TotalSopRuns,
			MonthlySopRuns:      user.MonthlySopRuns,
			AuthorizedTemplates: activeTemplateCount,
			UserTier:            user.GetActualUserTier(),
			TierExpires:         expiresStr,
			RemainingSopRuns:    user.GetRemainingSOPRuns(),
			CreditBalance:       creditBalance,
			CreditExpires:       creditExpires,
			MembershipState:     membershipState,
			HasUsedTrial:        hasUsedTrial,
			CycleRemaining:      cycleRemaining,
		})
	}

	return &v1.ListSubUsersResponse{
		Total:    total,
		SubUsers: subUsers,
	}, nil
}

// GetSubUserDetail 获取二级客户详情
func (c *customerBiz) GetSubUserDetail(ctx context.Context, parentUserID, subUserID uint) (*v1.SubUserDetailResponse, error) {
	// 验证所属关系
	user, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get sub user", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return nil, err
	}

	// 获取已授权的模板列表
	templates, err := c.ds.Customers().GetAuthorizedTemplates(ctx, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get authorized templates", "sub_user_id", subUserID, "err", err)
		return nil, err
	}

	// 转换为模板信息列表
	templateList := make([]v1.TemplateInfo, 0, len(templates))
	for _, t := range templates {
		if t.Status != "active" {
			continue
		}
		templateList = append(templateList, v1.TemplateInfo{
			ID:          t.ID,
			Name:        t.Name,
			Description: t.Description,
		})
	}

	expiresStr := ""
	if user.TierExpires != nil {
		expiresStr = user.TierExpires.Format("2006-01-02")
	}

	return &v1.SubUserDetailResponse{
		UserID:                   user.ID,
		Nickname:                 user.Nickname,
		Phone:                    user.Phone,
		Avatar:                   user.AvatarURL,
		UserTier:                 user.GetActualUserTier(),
		TierExpires:              expiresStr,
		TotalSopRuns:             user.TotalSopRuns,
		MonthlySopRuns:           user.MonthlySopRuns,
		AuthorizedTemplatesCount: len(templateList),
		RemainingSopRuns:         user.GetRemainingSOPRuns(),
		AuthorizedTemplates:      templateList,
	}, nil
}

// GetCustomerStatistics 获取客户统计数据
func (c *customerBiz) GetCustomerStatistics(ctx context.Context, userID uint) (*v1.CustomerStatisticsResponse, error) {
	// 获取用户信息
	user, err := c.ds.Users().GetByID(ctx, userID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get user", "user_id", userID, "err", err)
		return nil, err
	}

	// 获取二级客户统计
	totalSubUsers, activeSubUsers, err := c.ds.Customers().GetCustomerStatistics(ctx, userID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get customer statistics", "user_id", userID, "err", err)
		return nil, err
	}

	// 获取总模板数
	templates, _, err := c.ds.Sop().ListTemplates(0, 1000) // 获取所有模板
	if err != nil {
		log.C(ctx).Errorw("Failed to list templates", "err", err)
		return nil, err
	}

	expiresStr := ""
	if user.TierExpires != nil {
		expiresStr = user.TierExpires.Format("2006-01-02")
	}

	return &v1.CustomerStatisticsResponse{
		TotalSubUsers:    totalSubUsers,
		ActiveSubUsers:   activeSubUsers,
		TotalTemplates:   int64(len(templates)),
		TotalSopRuns:     int64(user.TotalSopRuns),
		UserTier:         user.GetActualUserTier(),
		TierExpires:      expiresStr,
		RemainingSopRuns: user.GetRemainingSOPRuns(),
	}, nil
}

// GrantTemplates 为二级客户授权模板
func (c *customerBiz) GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	return c.ds.Customers().GrantTemplates(ctx, parentUserID, subUserID, templateIDs)
}

// BatchGrantTemplates 批量为多个二级客户授权模板
func (c *customerBiz) BatchGrantTemplates(ctx context.Context, parentUserID uint, userIDs, templateIDs []uint) error {
	// 验证所有模板是否存在
	for _, templateID := range templateIDs {
		_, err := c.ds.Sop().GetTemplate(templateID)
		if err != nil {
			log.C(ctx).Errorw("Template not found", "template_id", templateID, "err", err)
			return fmt.Errorf("模板ID %d 不存在", templateID)
		}
	}

	// 为每个用户执行授权
	for _, userID := range userIDs {
		// 验证所属关系
		_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, userID)
		if err != nil {
			log.C(ctx).Warnw("Sub user not found or not belonging to parent", "parent_user_id", parentUserID, "sub_user_id", userID)
			continue
		}

		// 使用SetTemplates代替GrantTemplates，实现"所见即所得"的权限设置（包含移除未选中的权限）
		err = c.ds.Customers().SetTemplates(ctx, parentUserID, userID, templateIDs)
		if err != nil {
			log.C(ctx).Errorw("Failed to batch set templates for user", "sub_user_id", userID, "err", err)
			return err
		}
	}

	log.C(ctx).Infow("Batch templates granted successfully", "parent_user_id", parentUserID, "user_count", len(userIDs), "template_count", len(templateIDs))
	return nil
}

// BatchRevokeTemplates 批量为多个二级客户撤销模板权限
func (c *customerBiz) BatchRevokeTemplates(ctx context.Context, parentUserID uint, userIDs, templateIDs []uint) error {
	// 为每个用户执行撤销
	for _, userID := range userIDs {
		// 验证所属关系
		_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, userID)
		if err != nil {
			log.C(ctx).Warnw("Sub user not found or not belonging to parent for revoke", "parent_user_id", parentUserID, "sub_user_id", userID)
			continue
		}

		err = c.ds.Customers().RevokeTemplates(ctx, parentUserID, userID, templateIDs)
		if err != nil {
			log.C(ctx).Errorw("Failed to batch revoke templates from user", "sub_user_id", userID, "err", err)
			return err
		}
	}

	log.C(ctx).Infow("Batch templates revoked successfully", "parent_user_id", parentUserID, "user_count", len(userIDs), "template_count", len(templateIDs))
	return nil
}

// RevokeTemplates 撤销二级客户的模板权限
func (c *customerBiz) RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	return c.ds.Customers().RevokeTemplates(ctx, parentUserID, subUserID, templateIDs)
}

// CheckTemplatePermission 检查子客户是否有模板权限
func (c *customerBiz) CheckTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error) {
	return c.ds.Customers().HasTemplatePermission(ctx, userID, templateID)
}

// UpdateSubUserTier 升级子用户会员等级
func (c *customerBiz) UpdateSubUserTier(ctx context.Context, parentUserID, subUserID uint, req *v1.UpdateTierRequest) error {
	// 1. 验证所属关系
	user, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return fmt.Errorf("子用户不存在或不属于当前用户")
	}

	// 2. 获取当前实际等级
	currentTier := user.GetActualUserTier()

	// 3. 校验只能升级不能降级
	if model.TierRank(req.Tier) <= model.TierRank(currentTier) {
		return fmt.Errorf("只能升级等级，不能降级（当前: %s, 目标: %s）", currentTier, req.Tier)
	}

	// 4. 计算到期时间
	now := time.Now()
	var newExpires time.Time
	if req.Tier == model.UserTierTrial {
		// 体验会员固定3天
		newExpires = now.AddDate(0, 0, model.TrialDurationDays)
	} else {
		// 非 trial 等级必须指定有效的开通时长
		if req.Months < 1 || req.Months > 12 {
			return fmt.Errorf("开通时长需在 1-12 个月之间")
		}
		newExpires = now.AddDate(0, 0, req.Months*30)
	}

	// 5. 在事务中更新等级并写入变更日志
	changeLog := &model.TierChangeLog{
		ParentUserID:   parentUserID,
		SubUserID:      subUserID,
		OldTier:        currentTier,
		NewTier:        req.Tier,
		Months:         req.Months,
		OldTierExpires: user.TierExpires,
		NewTierExpires: newExpires,
	}
	if err := c.ds.Customers().UpdateSubUserTierWithLog(ctx, subUserID, req.Tier, newExpires, changeLog); err != nil {
		log.C(ctx).Errorw("Failed to update sub user tier", "sub_user_id", subUserID, "err", err)
		return fmt.Errorf("更新等级失败: %w", err)
	}

	log.C(ctx).Infow("Sub user tier upgraded",
		"parent_user_id", parentUserID,
		"sub_user_id", subUserID,
		"old_tier", currentTier,
		"new_tier", req.Tier,
		"months", req.Months,
		"new_expires", newExpires.Format("2006-01-02"),
	)

	return nil
}

// CheckFeaturePermission 检查用户是否有功能权限
func (c *customerBiz) CheckFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error) {
	return c.ds.Customers().HasFeaturePermission(ctx, userID, featureKey)
}

// GrantFeatures 为子用户授权功能
func (c *customerBiz) GrantFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership for feature grant", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	return c.ds.Customers().GrantFeatures(ctx, parentUserID, subUserID, featureKeys)
}

// RevokeFeatures 撤销子用户的功能权限
func (c *customerBiz) RevokeFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership for feature revoke", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	return c.ds.Customers().RevokeFeatures(ctx, parentUserID, subUserID, featureKeys)
}

// ListUserFeatures 获取用户的所有已授权功能（需验证所属关系）
func (c *customerBiz) ListUserFeatures(ctx context.Context, parentUserID, subUserID uint) ([]string, error) {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership for feature list", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return nil, err
	}

	return c.ds.Customers().ListUserFeatures(ctx, subUserID)
}

// ============================================================================
// Chatbot 运行权限管理（child-run-permission spec §3.5）
//
// 所有 Grant / Revoke 方法在 entry 处执行两道校验：
//  1. 父子关系校验：subUser.ParentUserID == parentUserID（GetSubUser 实现）
//     —— 父账号 A 不能给父账号 B 的子账号授权。失败 → errno.ErrForbidden。
//     自我授权（parentID == subID）在 GetSubUser 层自然被拒绝，与既有
//     GrantTemplates 语义一致（父账号 row 的 parent_user_id 为 NULL，
//     不匹配 parentID）。
//  2. Chatbot 归属校验：所有 chatbotIDs 的 chatbot_config.user_id 必须等于
//     parentUserID。失败 → errno.ErrChatbotNotFound。
// ============================================================================

// CheckChatbotPermission 检查用户是否有权运行指定 chatbot（直接委托 store）
func (c *customerBiz) CheckChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error) {
	return c.ds.Customers().HasChatbotPermission(ctx, userID, chatbotID)
}

// ListSubUserChatbots 列出子账号已授权的 chatbot 详情（JOIN chatbot_config）。
// 校验：subUser 必须属于 parentUser。
func (c *customerBiz) ListSubUserChatbots(ctx context.Context, parentUserID, subUserID uint) ([]model.ChatbotConfig, error) {
	// 1. 验证父子关系
	if _, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID); err != nil {
		log.C(ctx).Errorw("Failed to verify sub user ownership for chatbot list",
			"parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return nil, errno.ErrForbidden
	}

	// 2. 拿白名单 chatbot_id 列表
	ids, err := c.ds.Customers().ListSubUserChatbotIDs(ctx, subUserID)
	if err != nil {
		return nil, fmt.Errorf("ListSubUserChatbots list ids: %w", err)
	}
	if len(ids) == 0 {
		return []model.ChatbotConfig{}, nil
	}

	// 3. 批量查 chatbot_config 详情（限制 user_id = parentUserID 防止历史脏数据
	//    泄漏非本父账号的 chatbot 详情）。走 store 层保持 biz 不裸访问 DB。
	configs, err := c.ds.ChatbotConfig().ListByIDsOwnedBy(ctx, ids, parentUserID)
	if err != nil {
		return nil, fmt.Errorf("ListSubUserChatbots fetch configs: %w", err)
	}
	return configs, nil
}

// validateChatbotOwnership 校验所有 chatbotIDs 都属于 parentUserID。
// 返回 errno.ErrChatbotNotFound 如果有任何一个不匹配（包括不存在和跨父账号）。
func (c *customerBiz) validateChatbotOwnership(ctx context.Context, parentUserID uint, chatbotIDs []uint) error {
	if len(chatbotIDs) == 0 {
		return nil
	}
	// 去重避免 count 比较出错（UNIQUE PK id 本不应有重复，但入参可能携带）
	seen := make(map[uint]struct{}, len(chatbotIDs))
	unique := make([]uint, 0, len(chatbotIDs))
	for _, id := range chatbotIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	count, err := c.ds.ChatbotConfig().CountByIDsOwnedBy(ctx, unique, parentUserID)
	if err != nil {
		return fmt.Errorf("validateChatbotOwnership: %w", err)
	}
	if count != int64(len(unique)) {
		log.C(ctx).Warnw("Chatbot ownership check failed",
			"parent_user_id", parentUserID,
			"requested_ids", unique,
			"owned_count", count,
		)
		return errno.ErrChatbotNotFound
	}
	return nil
}

// GrantChatbots 为子账号授权 chatbot（幂等）
//
// 错误语义说明：本方法（以及 Revoke/Batch 同族方法）在父子关系校验失败时统一返回
// errno.ErrForbidden，而非既有 GrantTemplates 的透传 raw err。这是有意的向前改进：
// 避免内部 DB/GORM 错误细节（表名、SQL 片段、not-found 语义）泄漏到 HTTP client。
// 调用方只需要区分「禁止」（403）和「内部错误」（500），这正是 errno 的定位。
func (c *customerBiz) GrantChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error {
	// 1. 父子关系校验
	if _, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID); err != nil {
		log.C(ctx).Errorw("GrantChatbots: sub user ownership check failed",
			"parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return errno.ErrForbidden
	}
	// 2. Chatbot 归属校验
	if err := c.validateChatbotOwnership(ctx, parentUserID, chatbotIDs); err != nil {
		return err
	}
	// 3. 写入白名单
	if err := c.ds.Customers().GrantChatbotPermissions(ctx, subUserID, chatbotIDs); err != nil {
		return fmt.Errorf("GrantChatbots: %w", err)
	}
	log.C(ctx).Infow("Chatbots granted",
		"parent_user_id", parentUserID, "sub_user_id", subUserID, "count", len(chatbotIDs))
	return nil
}

// RevokeChatbots 撤销子账号的 chatbot 权限
func (c *customerBiz) RevokeChatbots(ctx context.Context, parentUserID, subUserID uint, chatbotIDs []uint) error {
	// 1. 父子关系校验
	if _, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID); err != nil {
		log.C(ctx).Errorw("RevokeChatbots: sub user ownership check failed",
			"parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return errno.ErrForbidden
	}
	// 2. Chatbot 归属校验 —— 撤销动作也校验归属，防止攻击者绕过父子关系测试
	//    传入别人的 chatbot_id（即便 revoke 写数据库时因 sub_user_id 约束不生效，
	//    业务层仍应拒绝非法入参，与 Grant 对称）
	if err := c.validateChatbotOwnership(ctx, parentUserID, chatbotIDs); err != nil {
		return err
	}
	// 3. 删除白名单行
	if err := c.ds.Customers().RevokeChatbotPermissions(ctx, subUserID, chatbotIDs); err != nil {
		return fmt.Errorf("RevokeChatbots: %w", err)
	}
	log.C(ctx).Infow("Chatbots revoked",
		"parent_user_id", parentUserID, "sub_user_id", subUserID, "count", len(chatbotIDs))
	return nil
}

// BatchGrantChatbots 为多个子账号批量授权多个 chatbot。
// 策略：先统一校验所有 chatbotIDs 的归属（1 次查询），再逐 subUser 做父子校验 +
// grant 写入。任一子账号校验失败 → 立即返回错误，已处理的子账号权限已写入（接受
// 与既有 BatchGrantTemplates 同级的部分成功语义，调用方可重试幂等）。
//
// Fail-fast 语义说明：本方法与既有 BatchGrantTemplates 的 continue-on-error 语义
// 有意不同，采用「任一子账号失败立即返回错误」。配合 grant 写入自身的幂等（UNIQUE
// 冲突 upsert），调用方发现错误后可安全重试——已成功写入的子账号二次 grant 不会
// 产生副作用，失败点之后的子账号重试覆盖即可。这比 continue-on-error 更利于调用
// 方感知和处理部分失败。
func (c *customerBiz) BatchGrantChatbots(ctx context.Context, parentUserID uint, subUserIDs, chatbotIDs []uint) error {
	// 1. Chatbot 归属统一校验（1 次 DB 查询）
	if err := c.validateChatbotOwnership(ctx, parentUserID, chatbotIDs); err != nil {
		return err
	}
	// 2. 逐子账号处理
	for _, subUserID := range subUserIDs {
		if _, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID); err != nil {
			log.C(ctx).Errorw("BatchGrantChatbots: sub user ownership check failed",
				"parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
			return errno.ErrForbidden
		}
		if err := c.ds.Customers().GrantChatbotPermissions(ctx, subUserID, chatbotIDs); err != nil {
			return fmt.Errorf("BatchGrantChatbots sub_user=%d: %w", subUserID, err)
		}
	}
	log.C(ctx).Infow("Batch chatbots granted",
		"parent_user_id", parentUserID,
		"sub_user_count", len(subUserIDs),
		"chatbot_count", len(chatbotIDs),
	)
	return nil
}

// BatchRevokeChatbots 为多个子账号批量撤销多个 chatbot。
//
// Fail-fast 语义说明：与 BatchGrantChatbots 对称，任一子账号失败立即返回。Revoke
// 本身幂等（DELETE WHERE 不存在行不报错），失败后重试安全。
func (c *customerBiz) BatchRevokeChatbots(ctx context.Context, parentUserID uint, subUserIDs, chatbotIDs []uint) error {
	// 1. Chatbot 归属统一校验
	if err := c.validateChatbotOwnership(ctx, parentUserID, chatbotIDs); err != nil {
		return err
	}
	// 2. 逐子账号处理
	for _, subUserID := range subUserIDs {
		if _, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID); err != nil {
			log.C(ctx).Errorw("BatchRevokeChatbots: sub user ownership check failed",
				"parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
			return errno.ErrForbidden
		}
		if err := c.ds.Customers().RevokeChatbotPermissions(ctx, subUserID, chatbotIDs); err != nil {
			return fmt.Errorf("BatchRevokeChatbots sub_user=%d: %w", subUserID, err)
		}
	}
	log.C(ctx).Infow("Batch chatbots revoked",
		"parent_user_id", parentUserID,
		"sub_user_count", len(subUserIDs),
		"chatbot_count", len(chatbotIDs),
	)
	return nil
}
