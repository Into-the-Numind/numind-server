package store

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
)

// ICustomerStore 定义客户管理相关的数据库操作接口
type ICustomerStore interface {
	// 子客户管理
	ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) ([]model.User, int64, error)
	GetSubUser(ctx context.Context, parentUserID, subUserID uint) (*model.User, error)

	// 模板权限管理
	GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	BulkGrantAllTemplates(ctx context.Context, parentUserID, subUserID uint) error
	GrantTemplateToConfiguredSubUsers(ctx context.Context, templateID uint) error
	SetTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error
	HasTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error)
	ListUserTemplatePermissions(ctx context.Context, userID uint) ([]model.UserTemplatePermission, error)
	GetAuthorizedTemplates(ctx context.Context, userID uint) ([]model.SopTemplate, error)

	// 统计相关
	GetCustomerStatistics(ctx context.Context, userID uint) (totalSubUsers, activeSubUsers int64, err error)
	GetUsersNeedMonthlyReset(ctx context.Context) ([]model.User, error)

	// 运行次数更新
	IncrementSopRunCount(ctx context.Context, userID uint) error
	ResetMonthlySopRuns(ctx context.Context, userID uint) error

	// 等级管理
	UpdateSubUserTierWithLog(ctx context.Context, subUserID uint, tier string, tierExpires time.Time, changeLog *model.TierChangeLog) error
}

type customerStore struct {
	db *gorm.DB
}

var _ ICustomerStore = (*customerStore)(nil)

func NewCustomerStore(db *gorm.DB) *customerStore {
	return &customerStore{db}
}

// ListSubUsers 获取指定直接客户的所有二级客户
func (c *customerStore) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	query := c.db.WithContext(ctx).Model(&model.User{}).Where("parent_user_id = ?", parentUserID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// GetSubUser 获取指定二级客户信息(需验证所属关系)
func (c *customerStore) GetSubUser(ctx context.Context, parentUserID, subUserID uint) (*model.User, error) {
	var user model.User
	err := c.db.WithContext(ctx).
		Where("id = ? AND parent_user_id = ?", subUserID, parentUserID).
		First(&user).Error

	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GrantTemplates 为二级客户批量授权模板
func (c *customerStore) GrantTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 使用事务确保原子性
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, templateID := range templateIDs {
			permission := &model.UserTemplatePermission{
				ParentUserID: parentUserID,
				SubUserID:    subUserID,
				TemplateID:   templateID,
			}

			// 使用FirstOrCreate避免重复授权
			if err := tx.Where(model.UserTemplatePermission{
				SubUserID:  subUserID,
				TemplateID: templateID,
			}).FirstOrCreate(permission).Error; err != nil {
				return fmt.Errorf("failed to grant template %d: %w", templateID, err)
			}
		}
		return nil
	})
}

// BulkGrantAllTemplates 为子用户批量授权所有现有SOP模板（用于新用户创建）
func (c *customerStore) BulkGrantAllTemplates(ctx context.Context, parentUserID, subUserID uint) error {
	// 单条SQL：将所有现有模板一次性授权给子用户
	sql := `INSERT INTO user_template_permission (parent_user_id, sub_user_id, template_id, created_at, updated_at)
		SELECT ?, ?, id, NOW(), NOW() FROM sop_template WHERE deleted_at IS NULL`
	return c.db.WithContext(ctx).Exec(sql, parentUserID, subUserID).Error
}

// GrantTemplateToConfiguredSubUsers 将新模板自动授权给所有已配置权限的子用户
func (c *customerStore) GrantTemplateToConfiguredSubUsers(ctx context.Context, templateID uint) error {
	// 单条SQL：找到所有已有权限记录的子用户，为其授权新模板
	sql := `INSERT INTO user_template_permission (parent_user_id, sub_user_id, template_id, created_at, updated_at)
		SELECT DISTINCT parent_user_id, sub_user_id, ?, NOW(), NOW()
		FROM user_template_permission
		WHERE deleted_at IS NULL
		AND (sub_user_id, ?) NOT IN (
			SELECT sub_user_id, template_id FROM user_template_permission WHERE deleted_at IS NULL
		)`
	return c.db.WithContext(ctx).Exec(sql, templateID, templateID).Error
}

// SetTemplates 设置二级客户的模板权限(覆盖模式)
// 注意：删除时不限制parent_user_id，因为权限可能是由管理员或其他父用户创建的
func (c *customerStore) SetTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	// 使用事务确保原子性
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 删除该子用户的所有现有权限（不限制parent_user_id）
		if err := tx.Where("sub_user_id = ?", subUserID).
			Delete(&model.UserTemplatePermission{}).Error; err != nil {
			return fmt.Errorf("failed to clear existing permissions: %w", err)
		}

		// 2. 添加新权限
		if len(templateIDs) > 0 {
			var permissions []model.UserTemplatePermission
			for _, templateID := range templateIDs {
				permissions = append(permissions, model.UserTemplatePermission{
					ParentUserID: parentUserID,
					SubUserID:    subUserID,
					TemplateID:   templateID,
				})
			}
			if err := tx.Create(&permissions).Error; err != nil {
				return fmt.Errorf("failed to set templates: %w", err)
			}
		}
		return nil
	})
}

// RevokeTemplates 撤销二级客户的模板权限
// 注意：权限可能是由管理员或其他父用户创建的，所以不限制parent_user_id
func (c *customerStore) RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	return c.db.WithContext(ctx).
		Where("sub_user_id = ? AND template_id IN ?", subUserID, templateIDs).
		Delete(&model.UserTemplatePermission{}).Error
}

// HasTemplatePermission 检查用户是否有模板权限
func (c *customerStore) HasTemplatePermission(ctx context.Context, userID, templateID uint) (bool, error) {
	// 先查询用户信息
	var user model.User
	if err := c.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, err
	}

	// 如果是直接客户(parent_user_id为NULL),有权限执行所有模板
	if user.ParentUserID == nil {
		return true, nil
	}

	// 检查用户是否有任何权限配置记录
	var totalPermissions int64
	if err := c.db.WithContext(ctx).Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", userID).
		Count(&totalPermissions).Error; err != nil {
		return false, err
	}

	// 如果没有任何权限配置记录（新用户或被重置），允许所有
	if totalPermissions == 0 {
		return true, nil
	}

	// 如果有配置记录，则检查白名单
	var count int64
	err := c.db.WithContext(ctx).Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ? AND template_id = ?", userID, templateID).
		Count(&count).Error

	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// ListUserTemplatePermissions 获取用户的所有模板权限
func (c *customerStore) ListUserTemplatePermissions(ctx context.Context, userID uint) ([]model.UserTemplatePermission, error) {
	var permissions []model.UserTemplatePermission
	err := c.db.WithContext(ctx).
		Where("sub_user_id = ?", userID).
		Preload("Template").
		Find(&permissions).Error

	return permissions, err
}

// GetAuthorizedTemplates 获取用户已授权的所有模板
func (c *customerStore) GetAuthorizedTemplates(ctx context.Context, userID uint) ([]model.SopTemplate, error) {
	var templates []model.SopTemplate

	err := c.db.WithContext(ctx).
		Distinct("sop_template.*").
		Joins("INNER JOIN user_template_permission ON user_template_permission.template_id = sop_template.id").
		Where("user_template_permission.sub_user_id = ? AND user_template_permission.deleted_at IS NULL", userID).
		Find(&templates).Error

	return templates, err
}

// GetCustomerStatistics 获取客户统计数据
func (c *customerStore) GetCustomerStatistics(ctx context.Context, userID uint) (totalSubUsers, activeSubUsers int64, err error) {
	// 获取二级客户总数
	err = c.db.WithContext(ctx).Model(&model.User{}).
		Where("parent_user_id = ?", userID).
		Count(&totalSubUsers).Error

	if err != nil {
		return 0, 0, err
	}

	// 获取本月活跃的二级客户数(monthly_sop_runs > 0)
	err = c.db.WithContext(ctx).Model(&model.User{}).
		Where("parent_user_id = ? AND monthly_sop_runs > 0", userID).
		Count(&activeSubUsers).Error

	return totalSubUsers, activeSubUsers, err
}

// GetUsersNeedMonthlyReset 获取需要月度重置的用户（30天会员月周期）
func (c *customerStore) GetUsersNeedMonthlyReset(ctx context.Context) ([]model.User, error) {
	var users []model.User

	// 查找 monthly_reset_at 为空 或 距上次重置已超过30天的用户
	threshold := time.Now().AddDate(0, 0, -30)
	err := c.db.WithContext(ctx).
		Where("monthly_reset_at IS NULL OR monthly_reset_at < ?", threshold).
		Find(&users).Error

	return users, err
}

// IncrementSopRunCount 增加用户的SOP运行次数
func (c *customerStore) IncrementSopRunCount(ctx context.Context, userID uint) error {
	// 先查询用户
	var user model.User
	if err := c.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return err
	}

	now := time.Now()
	needReset := false

	if user.MonthlyResetAt == nil {
		needReset = true
	} else {
		// 检查是否已过30天会员月周期（与 IsInNewSOPMonth 统一）
		lastReset := *user.MonthlyResetAt
		if now.After(lastReset.AddDate(0, 0, 30)) {
			needReset = true
		}
	}

	// 更新统计
	updates := map[string]interface{}{
		"total_sop_runs": gorm.Expr("total_sop_runs + ?", 1),
	}

	if needReset {
		updates["monthly_sop_runs"] = 1
		updates["monthly_reset_at"] = now
	} else {
		updates["monthly_sop_runs"] = gorm.Expr("monthly_sop_runs + ?", 1)
	}

	return c.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Updates(updates).Error
}

// ResetMonthlySopRuns 重置用户的月度运行次数
func (c *customerStore) ResetMonthlySopRuns(ctx context.Context, userID uint) error {
	now := time.Now()
	return c.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"monthly_sop_runs": 0,
		"monthly_reset_at": now,
	}).Error
}

// UpdateSubUserTierWithLog 在事务中更新子用户等级并写入变更日志
func (c *customerStore) UpdateSubUserTierWithLog(ctx context.Context, subUserID uint, tier string, tierExpires time.Time, changeLog *model.TierChangeLog) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&model.User{}).Where("id = ?", subUserID).Updates(map[string]interface{}{
			"user_tier":        tier,
			"tier_expires":     tierExpires,
			"monthly_sop_runs": 0,
			"monthly_reset_at": now,
		}).Error; err != nil {
			return fmt.Errorf("failed to update user tier: %w", err)
		}

		if err := tx.Create(changeLog).Error; err != nil {
			return fmt.Errorf("failed to create tier change log: %w", err)
		}

		return nil
	})
}
