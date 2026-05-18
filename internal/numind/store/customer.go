package store

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	ResetMonthlySopRuns(ctx context.Context, userID uint) error

	// 等级管理
	UpdateSubUserTierWithLog(ctx context.Context, subUserID uint, tier string, tierExpires time.Time, changeLog *model.TierChangeLog) error

	// 模板批量授权（B端发布SOP时使用）
	GrantTemplateToAllSubUsers(ctx context.Context, parentUserID uint, templateID uint) error

	// 功能权限管理
	HasFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error)
	GrantFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error
	RevokeFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error
	ListUserFeatures(ctx context.Context, subUserID uint) ([]string, error)

	// Chatbot 运行权限管理（default-deny 白名单，spec §3.4 / child-run-permission）
	HasChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error)
	ListSubUserChatbotIDs(ctx context.Context, subUserID uint) ([]uint, error)
	GrantChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error
	RevokeChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error
}

type customerStore struct {
	db *gorm.DB
}

var _ ICustomerStore = (*customerStore)(nil)

func NewCustomerStore(db *gorm.DB) *customerStore {
	return &customerStore{db}
}

// ListSubUsers 获取指定直接客户的所有二级客户，包含父账户自己（self-grant 支持）。
// 返回列表中父自己永远置顶，其它子账户按 created_at DESC 排序。
func (c *customerStore) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	// 包含父自己（self-grant 支持）+ 其直接子账户
	query := c.db.WithContext(ctx).Model(&model.User{}).
		Where("parent_user_id = ? OR id = ?", parentUserID, parentUserID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 父自己永远置顶（CASE WHEN id=parent THEN 0 ELSE 1），其它子账户按 created_at DESC
	// 用 fmt.Sprintf 拼接 parentUserID 是安全的——uint 类型不可承载 SQL 注入
	orderClause := fmt.Sprintf("CASE WHEN id = %d THEN 0 ELSE 1 END, created_at DESC", parentUserID)
	if err := query.Offset(offset).Limit(limit).Order(orderClause).Find(&users).Error; err != nil {
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
//
// 使用 Unscoped 物理删除而非软删除：UNIQUE(sub_user_id, template_id) 不含 deleted_at，
// 若只软删除，后续 re-grant 同 (sub_user_id, template_id) 组合会触发唯一索引冲突。
func (c *customerStore) SetTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("sub_user_id = ?", subUserID).
			Delete(&model.UserTemplatePermission{}).Error; err != nil {
			return fmt.Errorf("failed to clear existing permissions: %w", err)
		}

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
//
// 使用 Unscoped 物理删除：与 SetTemplates 同理，UNIQUE(sub_user_id, template_id)
// 不含 deleted_at，软删除残留会阻止后续 re-grant。
func (c *customerStore) RevokeTemplates(ctx context.Context, parentUserID, subUserID uint, templateIDs []uint) error {
	return c.db.WithContext(ctx).Unscoped().
		Where("sub_user_id = ? AND template_id IN ?", subUserID, templateIDs).
		Delete(&model.UserTemplatePermission{}).Error
}

// HasTemplatePermission 检查用户是否有模板权限。
//
// 语义（child-run-permission feature 翻转后）：
//   - 父账号（parent_user_id IS NULL） → 永远放行
//   - 子账号 0 活跃记录 → **deny**（default-deny，从原 default-allow 翻转）
//   - 子账号有活跃记录但不含目标 templateID → deny
//   - 子账号有活跃记录且含目标 templateID → allow
//
// 软删除：UserTemplatePermission 嵌入 gorm.Model（含 DeletedAt），GORM 默认 scope
// 会自动过滤 deleted_at IS NOT NULL 的行 —— 所以 Count 返回的是"活跃记录数"。
// 曾被父账号 RevokeTemplates 撤权（软删除）的子账号，活跃记录 = 0，按 default-deny
// 被拒绝 —— 这是正确行为。
//
// 部署顺序约束：本翻转必须在 backfill migration
// （20260420_230001_backfill_default_template_permissions.sql）之后上线，否则所有
// 未显式授权的存量子账号会被临时 deny-all。详见 spec §6。
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

	// 检查用户是否有任何活跃权限配置记录（软删除行由 GORM 默认 scope 过滤）
	var totalPermissions int64
	if err := c.db.WithContext(ctx).Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", userID).
		Count(&totalPermissions).Error; err != nil {
		return false, err
	}

	// child-run-permission: 0 活跃记录 → deny（default-deny，翻转）
	if totalPermissions == 0 {
		return false, nil
	}

	// 有活跃记录 → 严格白名单检查
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

// ResetMonthlySopRuns 重置用户的月度运行次数
func (c *customerStore) ResetMonthlySopRuns(ctx context.Context, userID uint) error {
	now := time.Now()
	return c.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"monthly_sop_runs": 0,
		"monthly_reset_at": now,
	}).Error
}

// HasFeaturePermission 检查用户是否有功能权限
// 功能权限采用显式授权模式：主用户始终有权限，子用户必须被明确授权才有权限
func (c *customerStore) HasFeaturePermission(ctx context.Context, userID uint, featureKey string) (bool, error) {
	// 先查询用户信息
	var user model.User
	if err := c.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, err
	}

	// 如果是直接客户(parent_user_id为NULL),有所有功能权限
	if user.ParentUserID == nil {
		return true, nil
	}

	// 子用户必须有明确的功能授权记录才允许
	var count int64
	err := c.db.WithContext(ctx).Model(&model.UserFeaturePermission{}).
		Where("sub_user_id = ? AND feature_key = ?", userID, featureKey).
		Count(&count).Error

	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// GrantFeatures 为子用户授权功能
func (c *customerStore) GrantFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range featureKeys {
			// 先查是否存在被软删除的记录（Unscoped 包含 deleted_at IS NOT NULL 的行）
			var existing model.UserFeaturePermission
			err := tx.Unscoped().Where("sub_user_id = ? AND feature_key = ?", subUserID, key).First(&existing).Error
			if err == nil {
				// 记录存在：如果是软删除状态则恢复，否则无需操作
				if existing.DeletedAt.Valid {
					return tx.Unscoped().Model(&existing).Updates(map[string]interface{}{
						"deleted_at":     nil,
						"parent_user_id": parentUserID,
					}).Error
				}
				continue
			}
			// 记录不存在，创建新记录
			permission := &model.UserFeaturePermission{
				ParentUserID: parentUserID,
				SubUserID:    subUserID,
				FeatureKey:   key,
			}
			if err := tx.Create(permission).Error; err != nil {
				return fmt.Errorf("failed to grant feature %s: %w", key, err)
			}
		}
		return nil
	})
}

// RevokeFeatures 撤销子用户的功能权限
func (c *customerStore) RevokeFeatures(ctx context.Context, parentUserID, subUserID uint, featureKeys []string) error {
	return c.db.WithContext(ctx).
		Where("sub_user_id = ? AND feature_key IN ?", subUserID, featureKeys).
		Delete(&model.UserFeaturePermission{}).Error
}

// ListUserFeatures 获取用户的所有已授权功能
func (c *customerStore) ListUserFeatures(ctx context.Context, subUserID uint) ([]string, error) {
	var features []string
	err := c.db.WithContext(ctx).Model(&model.UserFeaturePermission{}).
		Where("sub_user_id = ?", subUserID).
		Pluck("feature_key", &features).Error
	return features, err
}

// GrantTemplateToAllSubUsers 将模板授权给指定父用户的所有子用户（跳过已有授权）
func (c *customerStore) GrantTemplateToAllSubUsers(ctx context.Context, parentUserID uint, templateID uint) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 在事务内查询子用户，避免 TOCTOU 竞态
		var subUsers []model.User
		if err := tx.Where("parent_user_id = ?", parentUserID).Find(&subUsers).Error; err != nil {
			return fmt.Errorf("GrantTemplateToAllSubUsers: query sub users: %w", err)
		}

		if len(subUsers) == 0 {
			return nil
		}

		for _, u := range subUsers {
			permission := &model.UserTemplatePermission{
				ParentUserID: parentUserID,
				SubUserID:    u.ID,
				TemplateID:   templateID,
			}
			// FirstOrCreate 跳过已有授权
			if err := tx.Where(model.UserTemplatePermission{
				SubUserID:  u.ID,
				TemplateID: templateID,
			}).FirstOrCreate(permission).Error; err != nil {
				return fmt.Errorf("GrantTemplateToAllSubUsers: grant to user %d: %w", u.ID, err)
			}
		}
		return nil
	})
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

// ======================================================================
// Chatbot 权限管理（child-run-permission feature，spec §3.4）
//
// 对称于 Template 权限但**无软删除**：UserChatbotPermission 表用物理 DELETE。
// 语义：0 记录 → deny（default-deny 从起步就是，不像 Template 需要翻转 + backfill）。
// 父账号永远 bypass。
// ======================================================================

// HasChatbotPermission 检查用户是否有权运行指定 chatbot。
//
// 父账号（parent_user_id IS NULL） → true（bypass，不查表）
// 子账号 → 白名单查询 (sub_user_id, chatbot_id) 命中返回 true，否则 false
func (c *customerStore) HasChatbotPermission(ctx context.Context, userID, chatbotID uint) (bool, error) {
	var user model.User
	if err := c.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, fmt.Errorf("HasChatbotPermission: get user %d: %w", userID, err)
	}

	// 父账号 bypass
	if user.ParentUserID == nil {
		return true, nil
	}

	// 子账号：必须有白名单记录（default-deny）
	var count int64
	if err := c.db.WithContext(ctx).Model(&model.UserChatbotPermission{}).
		Where("sub_user_id = ? AND chatbot_id = ?", userID, chatbotID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("HasChatbotPermission: count whitelist: %w", err)
	}
	return count > 0, nil
}

// ListSubUserChatbotIDs 返回子账号已授权的 chatbot ID 列表。
// 供 biz 层做 ListVisibleChatbots 的白名单过滤。父账号也可调用，只是返回的是显式
// 白名单（父账号没白名单表行，会返回空 —— 调用方应先根据 parent_user_id 判断是否
// 需要 bypass）。
func (c *customerStore) ListSubUserChatbotIDs(ctx context.Context, subUserID uint) ([]uint, error) {
	var ids []uint
	err := c.db.WithContext(ctx).Model(&model.UserChatbotPermission{}).
		Where("sub_user_id = ?", subUserID).
		Pluck("chatbot_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("ListSubUserChatbotIDs: %w", err)
	}
	return ids, nil
}

// GrantChatbotPermissions 为子账号批量授权 chatbot（幂等）。
// UNIQUE KEY (sub_user_id, chatbot_id) + ON CONFLICT DO NOTHING → 重复 grant
// 不报错、不新增行。空数组提前返回 nil，避免无意义 SQL。
//
// 风格说明：不用 transaction，也不用 FirstOrCreate（参考 GrantTemplates 的风格）。
// 单条 `INSERT ... ON CONFLICT DO NOTHING` 是 MySQL/PG 原生原子操作，天然幂等；
// 相比 "查询已存在 → 逐条 insert" 模式少 N 次 RTT，并发写也安全（DB 级唯一约束兜底）。
func (c *customerStore) GrantChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error {
	if len(chatbotIDs) == 0 {
		return nil
	}
	rows := make([]model.UserChatbotPermission, 0, len(chatbotIDs))
	now := time.Now()
	for _, id := range chatbotIDs {
		rows = append(rows, model.UserChatbotPermission{
			SubUserID: subUserID,
			ChatbotID: id,
			CreatedAt: now,
		})
	}
	if err := c.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error; err != nil {
		return fmt.Errorf("GrantChatbotPermissions: insert %d rows: %w", len(rows), err)
	}
	return nil
}

// RevokeChatbotPermissions 批量撤销子账号的 chatbot 权限。
// UserChatbotPermission 表**无软删除**，这里是物理 DELETE。
// 传入不存在的 chatbot_id 不会报错，DELETE ... WHERE IN (...) 会简单匹配 0 行返回。
// 空数组提前返回 nil。
func (c *customerStore) RevokeChatbotPermissions(ctx context.Context, subUserID uint, chatbotIDs []uint) error {
	if len(chatbotIDs) == 0 {
		return nil
	}
	if err := c.db.WithContext(ctx).
		Where("sub_user_id = ? AND chatbot_id IN ?", subUserID, chatbotIDs).
		Delete(&model.UserChatbotPermission{}).Error; err != nil {
		return fmt.Errorf("RevokeChatbotPermissions: delete %d rows: %w", len(chatbotIDs), err)
	}
	return nil
}
