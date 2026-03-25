package customer

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
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
}

type customerBiz struct {
	ds store.IStore
}

var _ ICustomerBiz = (*customerBiz)(nil)

func New(ds store.IStore) ICustomerBiz {
	return &customerBiz{ds: ds}
}

// ListSubUsers 获取二级客户列表
func (c *customerBiz) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) (*v1.ListSubUsersResponse, error) {
	users, total, err := c.ds.Customers().ListSubUsers(ctx, parentUserID, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list sub users", "parent_user_id", parentUserID, "err", err)
		return nil, err
	}

	// 转换为响应格式
	subUsers := make([]v1.SubUserInfo, 0, len(users))
	for _, user := range users {
		// 获取已授权的模板数量（使用GetAuthorizedTemplates并过滤active状态，与GetSubUserDetail保持一致）
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

		subUsers = append(subUsers, v1.SubUserInfo{
			UserID:              user.ID,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			Avatar:              user.AvatarURL,
			TotalSopRuns:        user.TotalSopRuns,
			MonthlySopRuns:      user.MonthlySopRuns,
			AuthorizedTemplates: activeTemplateCount,
			UserTier:            user.GetActualUserTier(),
			TierExpires:         expiresStr,
			RemainingSopRuns:    user.GetRemainingSOPRuns(),
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
