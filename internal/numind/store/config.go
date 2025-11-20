package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type ConfigStore interface {
	Create(ctx context.Context, config *model.SystemConfigM) error
	GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error)
	GetAll(ctx context.Context) ([]*model.SystemConfigM, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) // 分页查询，用于后台管理系统
	Update(ctx context.Context, config *model.SystemConfigM) error
	Delete(ctx context.Context, key string) error
	InitDefaultConfigs(ctx context.Context) error
}

type configs struct {
	db *gorm.DB
}

var _ ConfigStore = (*configs)(nil)

func NewConfigStore(db *gorm.DB) ConfigStore {
	return &configs{db}
}

func (s *configs) Create(ctx context.Context, config *model.SystemConfigM) error {
	return s.db.WithContext(ctx).Create(config).Error
}

func (s *configs) GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error) {
	var config model.SystemConfigM
	err := s.db.WithContext(ctx).Where("`key` = ?", key).First(&config).Error
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *configs) GetAll(ctx context.Context) ([]*model.SystemConfigM, error) {
	var configs []*model.SystemConfigM
	err := s.db.WithContext(ctx).Find(&configs).Error
	return configs, err
}

// List 分页查询系统配置（用于后台管理系统）
func (s *configs) List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) {
	query := s.db.WithContext(ctx).Model(&model.SystemConfigM{})

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}

	// 分页查询，返回所有字段
	var configs []*model.SystemConfigM
	if err := query.Offset(offset).Limit(defaultLimit(limit)).Order("id DESC").Find(&configs).Error; err != nil {
		return 0, nil, err
	}

	return total, configs, nil
}

func (s *configs) Update(ctx context.Context, config *model.SystemConfigM) error {
	// UpdatedAt 由 gorm.Model 自动管理，无需手动设置
	return s.db.WithContext(ctx).Save(config).Error
}

func (s *configs) Delete(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Where("`key` = ?", key).Delete(&model.SystemConfigM{}).Error
}

func (s *configs) InitDefaultConfigs(ctx context.Context) error {
	// 这个方法现在由 biz 层实现，这里保留空实现以保持接口兼容
	// 实际的配置同步逻辑在 config.InitDefaultConfigs 中
	return nil
}
