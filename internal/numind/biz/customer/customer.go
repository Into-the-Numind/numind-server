package customer

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// CustomerBiz 客户业务逻辑接口
type CustomerBiz interface {
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
}

type customerBiz struct {
	ds store.IStore
}

var _ CustomerBiz = (*customerBiz)(nil)

func New(ds store.IStore) CustomerBiz {
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
			AuthorizedTemplates: len(permissions),
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
				ID:          perm.TemplateID,
				Name:        perm.Template.Name,
				Description: perm.Template.Description,
			})
		}
	}

	expiresStr := ""
	if user.TierExpires != nil {
		expiresStr = user.TierExpires.Format("2006-01-02")
	}

	return &v1.SubUserDetailResponse{
		SubUserInfo: v1.SubUserInfo{
			UserID:              user.ID,
			Nickname:            user.Nickname,
			Phone:               user.Phone,
			Avatar:              user.AvatarURL,
			TotalSopRuns:        user.TotalSopRuns,
			MonthlySopRuns:      user.MonthlySopRuns,
			AuthorizedTemplates: len(templateList),
			UserTier:            user.GetActualUserTier(),
			TierExpires:         expiresStr,
			RemainingSopRuns:    user.GetRemainingSOPRuns(),
		},
		AuthorizedTemplates: templateList,
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

		err = c.ds.Customers().GrantTemplates(ctx, parentUserID, userID, templateIDs)
		if err != nil {
			log.C(ctx).Errorw("Failed to batch grant templates to user", "sub_user_id", userID, "err", err)
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
