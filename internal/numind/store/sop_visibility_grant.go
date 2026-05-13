package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ISopVisibilityGrantStore SOP 可见范围 grant store.
// 详见 spec §4.1 / §5.3 / §9 EC-6.
type ISopVisibilityGrantStore interface {
	// ListSubUserIDsBySopID 返回某 SOP 的白名单子用户 ID (未软删).
	// 用于 GET /v1/sop/templates/:id/visibility 端点.
	ListSubUserIDsBySopID(ctx context.Context, sopID uint) ([]uint, error)

	// ListVisibleSopIDsBySubUser 返回某子用户被授权可见的 SOP ID set (未软删).
	// 用于列表查询的批量过滤 (ListVisibleTemplatesWithPermission).
	ListVisibleSopIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error)

	// CountBySubUserAndSop 返回 (sub_user_id, sop_template_id) 未软删的记录数 (0 或 1).
	// 用于 IsSopVisibleToUser 判断.
	CountBySubUserAndSop(ctx context.Context, subUserID, sopID uint) (int64, error)

	// ReplaceGrantsTx 物理删全部该 SOP 的现有 grant (含软删) 后插入新 grant.
	// 用于 UpdateSopVisibility restricted=true 路径; 配合 §2.2 不含 deleted_at 的唯一索引.
	ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, sopID, parentUserID uint, subUserIDs []uint) error

	// CleanupBySubUser 软删某子用户的所有 SOP grant (DeleteSubUser 级联清理路径).
	// 保留软删记录用于审计; 下次 ReplaceGrantsTx 通过 Unscoped() 物理清理.
	CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error

	// CleanupByEntity 软删某 SOP 的所有 grant (DeleteSopTemplate 路径, EC-6).
	CleanupByEntity(ctx context.Context, tx *gorm.DB, sopID uint) error
}

// sopVisibilityGrantStore ISopVisibilityGrantStore 的实现.
type sopVisibilityGrantStore struct {
	db *gorm.DB
}

// 确保 sopVisibilityGrantStore 实现了 ISopVisibilityGrantStore 接口.
var _ ISopVisibilityGrantStore = (*sopVisibilityGrantStore)(nil)

// NewSopVisibilityGrantStore 创建 SopVisibilityGrant store 实例.
func NewSopVisibilityGrantStore(db *gorm.DB) *sopVisibilityGrantStore {
	return &sopVisibilityGrantStore{db: db}
}

// ListSubUserIDsBySopID 返回某 SOP 的白名单子用户 ID.
func (s *sopVisibilityGrantStore) ListSubUserIDsBySopID(ctx context.Context, sopID uint) ([]uint, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).
		Model(&model.SopVisibilityGrant{}).
		Where("sop_template_id = ?", sopID).
		Pluck("sub_user_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("ListSubUserIDsBySopID: %w", err)
	}
	return ids, nil
}

// ListVisibleSopIDsBySubUser 返回某子用户能看到的 SOP ID set.
func (s *sopVisibilityGrantStore) ListVisibleSopIDsBySubUser(ctx context.Context, subUserID uint) (map[uint]struct{}, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).
		Model(&model.SopVisibilityGrant{}).
		Where("sub_user_id = ?", subUserID).
		Pluck("sop_template_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("ListVisibleSopIDsBySubUser: %w", err)
	}
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}

// CountBySubUserAndSop 返回 (sub_user_id, sop_template_id) 未软删记录数.
func (s *sopVisibilityGrantStore) CountBySubUserAndSop(ctx context.Context, subUserID, sopID uint) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.SopVisibilityGrant{}).
		Where("sub_user_id = ? AND sop_template_id = ?", subUserID, sopID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("CountBySubUserAndSop: %w", err)
	}
	return count, nil
}

// ReplaceGrantsTx 物理删 + 重插, 用于 UpdateSopVisibility restricted=true 路径.
// Unscoped() 关键: 跳过 GORM 软删 scope, 物理删包括软删记录, 避免 (sub_user_id, sop_template_id) 唯一索引冲突.
func (s *sopVisibilityGrantStore) ReplaceGrantsTx(ctx context.Context, tx *gorm.DB, sopID, parentUserID uint, subUserIDs []uint) error {
	if err := tx.WithContext(ctx).Unscoped().
		Where("sop_template_id = ?", sopID).
		Delete(&model.SopVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("ReplaceGrantsTx: physical delete: %w", err)
	}
	if len(subUserIDs) == 0 {
		return nil
	}
	records := make([]model.SopVisibilityGrant, 0, len(subUserIDs))
	for _, uid := range subUserIDs {
		records = append(records, model.SopVisibilityGrant{
			ParentUserID:  parentUserID,
			SubUserID:     uid,
			SopTemplateID: sopID,
		})
	}
	if err := tx.WithContext(ctx).Create(&records).Error; err != nil {
		return fmt.Errorf("ReplaceGrantsTx: insert new grants: %w", err)
	}
	return nil
}

// CleanupBySubUser 软删该子用户的所有 SOP grant.
func (s *sopVisibilityGrantStore) CleanupBySubUser(ctx context.Context, tx *gorm.DB, subUserID uint) error {
	if err := tx.WithContext(ctx).
		Where("sub_user_id = ?", subUserID).
		Delete(&model.SopVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("CleanupBySubUser: %w", err)
	}
	return nil
}

// CleanupByEntity 软删该 SOP 的所有 grant (EC-6).
func (s *sopVisibilityGrantStore) CleanupByEntity(ctx context.Context, tx *gorm.DB, sopID uint) error {
	if err := tx.WithContext(ctx).
		Where("sop_template_id = ?", sopID).
		Delete(&model.SopVisibilityGrant{}).Error; err != nil {
		return fmt.Errorf("CleanupByEntity: %w", err)
	}
	return nil
}
