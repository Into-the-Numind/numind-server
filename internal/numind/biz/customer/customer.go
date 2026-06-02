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

	// 累计运行 = SOP run + chatbot session（不再读 user.TotalSopRuns，已 stale，参 [[project_total_sop_runs_frozen]]）
	subUserIDs := make([]uint, len(users))
	for i, u := range users {
		subUserIDs[i] = u.ID
	}
	runCounts, err := c.ds.Customers().GetSubUserRunCounts(ctx, subUserIDs)
	if err != nil {
		log.C(ctx).Warnw("GetSubUserRunCounts failed, falling back to zero", "err", err)
		runCounts = map[uint]int{}
	}

	// 转换为响应格式
	subUsers := make([]v1.SubUserInfo, 0, len(users))
	for _, user := range users {
		// "已授权模板" = SOP + chatbot + 销售智能体 (0/1)，业务含义是"该账号可使用的全部
		// AI 资产数"。计数口径按账号类型分流：
		//   - 子账号：白名单计数（user_template_permission / user_chatbot_permission）。
		//   - 父账号：父账号 bypass 所有运行权限、不持有白名单行，按"全部可用"计数——
		//     与 C 端 /v1/sop/templates、/v1/chatbot/list（即权限弹窗的全部条目）口径一致：
		//     自己创建的可见 SOP（ListVisibleTemplates 的 total）+ 名下 published chatbot。
		//     否则父账号行会恒显示 0+0+sales_agent=1（本次客户上报的 bug）。
		// 任一 store 调用失败 warn-log + 该项归零，不阻塞整个列表。
		var sopCount, chatbotCount int64
		if user.ParentUserID == nil {
			if _, total, terr := c.ds.Sop().ListVisibleTemplates(ctx, user.ID, 0, 1); terr != nil {
				log.C(ctx).Warnw("ListVisibleTemplates failed, falling back to 0",
					"user_id", user.ID, "err", terr)
			} else {
				sopCount = total
			}
			if bots, berr := c.ds.ChatbotConfig().ListPublishedByOwner(ctx, user.ID); berr != nil {
				log.C(ctx).Warnw("ListPublishedByOwner failed, falling back to 0",
					"user_id", user.ID, "err", berr)
			} else {
				chatbotCount = int64(len(bots))
			}
		} else {
			if sc, serr := c.ds.Customers().CountActiveAuthorizedSopTemplates(ctx, user.ID); serr != nil {
				log.C(ctx).Warnw("CountActiveAuthorizedSopTemplates failed, falling back to 0",
					"user_id", user.ID, "err", serr)
			} else {
				sopCount = sc
			}
			if cc, cerr := c.ds.Customers().CountActiveAuthorizedChatbots(ctx, user.ID); cerr != nil {
				log.C(ctx).Warnw("CountActiveAuthorizedChatbots failed, falling back to 0",
					"user_id", user.ID, "err", cerr)
			} else {
				chatbotCount = cc
			}
		}
		activeTemplateCount := int(sopCount) + int(chatbotCount)
		// 销售智能体是 0/1 整体授权（双层 AND），通过即 +1。
		// user 已是 range 副本（Go 1.22+ 每轮独立作用域），直接取址安全。
		if salesOK, salesErr := c.hasSalesAgentPermission(ctx, &user); salesErr != nil {
			log.C(ctx).Warnw("hasSalesAgentPermission failed, treating as not granted",
				"user_id", user.ID, "err", salesErr)
		} else if salesOK {
			activeTemplateCount++
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
		// credits-only 体系下所有用户的真实数据都在 msBatch 里（新表）。
		var creditBalance int64
		var creditExpires string
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

		subUsers = append(subUsers, v1.SubUserInfo{
			UserID:              user.ID,
			Username:            user.Username,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			Avatar:              user.AvatarURL,
			TotalSopRuns:        runCounts[user.ID],
			AuthorizedTemplates: activeTemplateCount,
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

	// 累计运行 = SOP run + chatbot session 实时计数（不读 stale user.TotalSopRuns）
	runCounts, err := c.ds.Customers().GetSubUserRunCounts(ctx, []uint{user.ID})
	if err != nil {
		log.C(ctx).Warnw("GetSubUserRunCounts failed for detail, falling back to zero", "sub_user_id", user.ID, "err", err)
		runCounts = map[uint]int{}
	}

	return &v1.SubUserDetailResponse{
		UserID:                   user.ID,
		Nickname:                 user.Nickname,
		Phone:                    user.Phone,
		Avatar:                   user.AvatarURL,
		TotalSopRuns:             runCounts[user.ID],
		AuthorizedTemplatesCount: len(templateList),
		AuthorizedTemplates:      templateList,
	}, nil
}

// GetCustomerStatistics 获取客户统计数据。
// 全部聚合走 store 一次性查询，本函数只做日志 + DTO 转换。
// （旧版同时读 user.TotalSopRuns + ListTemplates(0, 1000) 的逻辑已废弃，参 [[project_total_sop_runs_frozen]]）
func (c *customerBiz) GetCustomerStatistics(ctx context.Context, userID uint) (*v1.CustomerStatisticsResponse, error) {
	totalSubUsers, activeSubUsers, totalTemplates, totalRuns, err := c.ds.Customers().GetCustomerStatistics(ctx, userID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get customer statistics", "user_id", userID, "err", err)
		return nil, err
	}

	return &v1.CustomerStatisticsResponse{
		TotalSubUsers:  totalSubUsers,
		ActiveSubUsers: activeSubUsers,
		TotalTemplates: totalTemplates,
		TotalSopRuns:   totalRuns,
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

// CheckFeaturePermission 检查用户是否有 feature 权限。
// Dispatch by featureKey (spec D2):
//   - "sales_agent": 走 hasSalesAgentPermission 双层 AND，无父账户硬 bypass
//   - 其他 (content_monitor / self_service_config / 未来): 父账户 bypass + 子用户 grant 查询
func (c *customerBiz) CheckFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error) {
	var user model.User
	if err := c.ds.DB().WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, fmt.Errorf("CheckFeaturePermission: lookup user: %w", err)
	}

	if featureKey == model.FeatureKeySalesAgent {
		return c.hasSalesAgentPermission(ctx, &user)
	}

	// 其他 feature_key 保留父账户硬 bypass (本需求不动)
	if user.ParentUserID == nil {
		return true, nil
	}
	return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, featureKey)
}

// hasSalesAgentPermission 销售智能体双层 AND 检查 (spec §3.2):
//
//	Layer 0: 用户所属父账户必须在 sales_agent_owner 表中
//	Layer 1: 子用户必须额外在 user_feature_permission 表中有 sales_agent 行
//	父账户用户: 仅需 Layer 0
func (c *customerBiz) hasSalesAgentPermission(ctx context.Context, user *model.User) (bool, error) {
	parentID := user.ID
	if user.ParentUserID != nil {
		parentID = *user.ParentUserID
	}

	// Layer 0
	ownerExists, err := c.ds.SalesAgentOwners().Exists(ctx, parentID)
	if err != nil {
		return false, fmt.Errorf("hasSalesAgentPermission: owner check: %w", err)
	}
	if !ownerExists {
		return false, nil
	}

	// 父账户: Layer 0 已足够
	if user.ParentUserID == nil {
		return true, nil
	}

	// 子账户: Layer 1 必查
	return c.ds.Customers().CheckSubUserFeatureGrant(ctx, user.ID, model.FeatureKeySalesAgent)
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
