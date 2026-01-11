package customer

import (
	"context"
	"errors"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
	"time"
)

// ICustomerBiz 定义客户管理业务逻辑接口
type ICustomerBiz interface {
	// 客户管理
	ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) (*v1.ListSubUsersResponse, error)
	GetSubUserDetail(ctx context.Context, parentUserID, subUserID uint) (*v1.SubUserDetailResponse, error)
	GetCustomerStatistics(ctx context.Context, userID uint) (*v1.CustomerStatisticsResponse, error)

	// 权限管理
	GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	CheckTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error)

	// 运行统计
	IncrementSopRunCount(ctx context.Context, userID uint) error
	ResetMonthlySopRuns(ctx context.Context) error
}

type customerBiz struct {
	ds store.IStore
}

var _ ICustomerBiz = (*customerBiz)(nil)

// New 创建一个CustomerBiz实例
func New(ds store.IStore) *customerBiz {
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
		// 获取已授权的模板数量
		permissions, _ := c.ds.Customers().ListUserTemplatePermissions(ctx, user.ID)

		subUsers = append(subUsers, v1.SubUserInfo{
			UserID:              user.ID,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			AvatarURL:           user.AvatarURL,
			MembershipType:      user.MembershipType,
			MembershipExpires:   user.MembershipExpires,
			TotalSopRuns:        user.TotalSopRuns,
			MonthlySopRuns:      user.MonthlySopRuns,
			AuthorizedTemplates: len(permissions),
			CreatedAt:           user.CreatedAt,
			// 用户等级相关字段
			UserTier:         user.GetActualUserTier(),
			TierExpires:      user.TierExpires,
			RemainingSOPRuns: user.GetRemainingSOPRuns(),
		})
	}

	return &v1.ListSubUsersResponse{
		TotalCount: total,
		SubUsers:   subUsers,
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
	permissions, err := c.ds.Customers().ListUserTemplatePermissions(ctx, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to list user template permissions", "sub_user_id", subUserID, "err", err)
		return nil, err
	}

	// 转换为模板信息列表
	templateList := make([]v1.TemplateInfo, 0, len(permissions))
	for _, perm := range permissions {
		if perm.Template != nil {
			templateList = append(templateList, v1.TemplateInfo{
				TemplateID:  perm.TemplateID,
				Name:        perm.Template.Name,
				Description: perm.Template.Description,
				Status:      perm.Template.Status,
				GrantedAt:   perm.CreatedAt,
			})
		}
	}

	return &v1.SubUserDetailResponse{
		SubUserInfo: v1.SubUserInfo{
			UserID:              user.ID,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			AvatarURL:           user.AvatarURL,
			MembershipType:      user.MembershipType,
			MembershipExpires:   user.MembershipExpires,
			TotalSopRuns:        user.TotalSopRuns,
			MonthlySopRuns:      user.MonthlySopRuns,
			AuthorizedTemplates: len(templateList),
			CreatedAt:           user.CreatedAt,
			// 用户等级相关字段
			UserTier:         user.GetActualUserTier(),
			TierExpires:      user.TierExpires,
			RemainingSOPRuns: user.GetRemainingSOPRuns(),
		},
		AuthorizedTemplateList: templateList,
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

	return &v1.CustomerStatisticsResponse{
		TotalSubUsers:       int(totalSubUsers),
		ActiveSubUsers:      int(activeSubUsers),
		TotalTemplatesCount: len(templates),
		MyTotalSopRuns:      user.TotalSopRuns,
		MyMonthlySopRuns:    user.MonthlySopRuns,
		// 用户等级相关字段（用于侧边栏运行次数卡片）
		UserTier:         user.GetActualUserTier(),
		TierExpires:      user.TierExpires,
		RemainingSOPRuns: user.GetRemainingSOPRuns(),
	}, nil
}

// GrantTemplates 为二级客户授权模板
func (c *customerBiz) GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user relationship", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	// 验证模板是否存在
	for _, templateID := range templateIDs {
		_, err := c.ds.Sop().GetTemplate(templateID)
		if err != nil {
			log.C(ctx).Errorw("Template not found", "template_id", templateID, "err", err)
			return errors.New("模板不存在")
		}
	}

	// 执行授权
	err = c.ds.Customers().GrantTemplates(ctx, parentUserID, subUserID, templateIDs)
	if err != nil {
		log.C(ctx).Errorw("Failed to grant templates", "parent_user_id", parentUserID, "sub_user_id", subUserID, "template_ids", templateIDs, "err", err)
		return err
	}

	log.C(ctx).Infow("Templates granted successfully", "parent_user_id", parentUserID, "sub_user_id", subUserID, "template_ids", templateIDs)
	return nil
}

// RevokeTemplates 撤销二级客户的模板权限
func (c *customerBiz) RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 验证所属关系
	_, err := c.ds.Customers().GetSubUser(ctx, parentUserID, subUserID)
	if err != nil {
		log.C(ctx).Errorw("Failed to verify sub user relationship", "parent_user_id", parentUserID, "sub_user_id", subUserID, "err", err)
		return err
	}

	// 执行撤销
	err = c.ds.Customers().RevokeTemplates(ctx, parentUserID, subUserID, templateIDs)
	if err != nil {
		log.C(ctx).Errorw("Failed to revoke templates", "parent_user_id", parentUserID, "sub_user_id", subUserID, "template_ids", templateIDs, "err", err)
		return err
	}

	log.C(ctx).Infow("Templates revoked successfully", "parent_user_id", parentUserID, "sub_user_id", subUserID, "template_ids", templateIDs)
	return nil
}

// CheckTemplatePermission 检查用户是否有模板权限
func (c *customerBiz) CheckTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error) {
	hasPermission, err := c.ds.Customers().HasTemplatePermission(ctx, userID, templateID)
	if err != nil {
		log.C(ctx).Errorw("Failed to check template permission", "user_id", userID, "template_id", templateID, "err", err)
		return false, err
	}

	return hasPermission, nil
}

// IncrementSopRunCount 增加用户的SOP运行次数
func (c *customerBiz) IncrementSopRunCount(ctx context.Context, userID uint) error {
	err := c.ds.Customers().IncrementSopRunCount(ctx, userID)
	if err != nil {
		log.C(ctx).Errorw("Failed to increment sop run count", "user_id", userID, "err", err)
		return err
	}

	log.C(ctx).Debugw("SOP run count incremented", "user_id", userID)
	return nil
}

// ResetMonthlySopRuns 重置所有用户的月度运行次数(定时任务调用)
func (c *customerBiz) ResetMonthlySopRuns(ctx context.Context) error {
	now := time.Now()

	// 查找需要重置的用户
	needResetUsers, err := c.ds.Customers().GetUsersNeedMonthlyReset(ctx)
	if err != nil {
		log.C(ctx).Errorw("Failed to get users need monthly reset", "err", err)
		return err
	}

	log.C(ctx).Infow("Starting monthly SOP runs reset", "count", len(needResetUsers), "time", now)

	// 批量重置
	successCount := 0
	for _, user := range needResetUsers {
		err := c.ds.Customers().ResetMonthlySopRuns(ctx, user.ID)
		if err != nil {
			log.C(ctx).Errorw("Failed to reset monthly sop runs", "user_id", user.ID, "err", err)
			continue
		}
		successCount++
	}

	log.C(ctx).Infow("Monthly SOP runs reset completed",
		"total", len(needResetUsers),
		"success", successCount,
		"failed", len(needResetUsers)-successCount)

	return nil
}
