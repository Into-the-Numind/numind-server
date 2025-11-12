package store

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// AdminAccountStore 管理员账户存储接口
type AdminAccountStore interface {
	Create(ctx context.Context, admin *model.Admin) error
	GetByUsername(ctx context.Context, username string) (*model.Admin, error)
	GetByID(ctx context.Context, id uint) (*model.Admin, error)
	Update(ctx context.Context, admin *model.Admin) error
	UpdateLastLogin(ctx context.Context, id uint) error
	List(ctx context.Context, offset, limit int) (int64, []*model.Admin, error)
	Delete(ctx context.Context, id uint) error
}

// adminAccountStore 管理员账户存储实现
type adminAccountStore struct {
	db *gorm.DB
}

var _ AdminAccountStore = (*adminAccountStore)(nil)

// NewAdminAccountStore 创建管理员账户存储实例
func NewAdminAccountStore(db *gorm.DB) AdminAccountStore {
	return &adminAccountStore{db: db}
}

// Create 创建管理员账户
func (s *adminAccountStore) Create(ctx context.Context, admin *model.Admin) error {
	return s.db.WithContext(ctx).Create(admin).Error
}

// GetByUsername 根据用户名获取管理员账户
func (s *adminAccountStore) GetByUsername(ctx context.Context, username string) (*model.Admin, error) {
	var admin model.Admin
	err := s.db.WithContext(ctx).Where("username = ?", username).First(&admin).Error
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// GetByID 根据ID获取管理员账户
func (s *adminAccountStore) GetByID(ctx context.Context, id uint) (*model.Admin, error) {
	var admin model.Admin
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&admin).Error
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// Update 更新管理员账户
func (s *adminAccountStore) Update(ctx context.Context, admin *model.Admin) error {
	return s.db.WithContext(ctx).Save(admin).Error
}

// UpdateLastLogin 更新最后登录时间
func (s *adminAccountStore) UpdateLastLogin(ctx context.Context, id uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.Admin{}).
		Where("id = ?", id).
		Update("last_login", now).Error
}

// List 获取管理员账户列表
func (s *adminAccountStore) List(ctx context.Context, offset, limit int) (int64, []*model.Admin, error) {
	var count int64
	var admins []*model.Admin

	err := s.db.WithContext(ctx).Model(&model.Admin{}).Count(&count).Error
	if err != nil {
		return 0, nil, err
	}

	err = s.db.WithContext(ctx).
		Offset(offset).
		Limit(limit).
		Order("id DESC").
		Find(&admins).Error

	return count, admins, err
}

// Delete 删除管理员账户
func (s *adminAccountStore) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.Admin{}, id).Error
}
