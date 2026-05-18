package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ISalesAgentOwnerStore 销售智能体归属表数据访问接口
type ISalesAgentOwnerStore interface {
	// Exists 检查指定父账户是否拥有销售智能体。
	// 返回 (true, nil) 表示存在; (false, nil) 表示不存在 (不返回 ErrRecordNotFound);
	// (false, err) 表示查询失败。
	Exists(ctx context.Context, parentUserID uint) (bool, error)
}

type salesAgentOwnerStore struct {
	db *gorm.DB
}

// NewSalesAgentOwnerStore 构造销售智能体归属表 store
func NewSalesAgentOwnerStore(db *gorm.DB) ISalesAgentOwnerStore {
	return &salesAgentOwnerStore{db: db}
}

var _ ISalesAgentOwnerStore = (*salesAgentOwnerStore)(nil)

// Exists 检查父账户是否在 owner 表中
func (s *salesAgentOwnerStore) Exists(ctx context.Context, parentUserID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&model.SalesAgentOwner{}).
		Where("parent_user_id = ?", parentUserID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("SalesAgentOwners.Exists: %w", err)
	}
	return count > 0, nil
}
