package config

import (
	"context"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type ConfigBiz interface {
	Create(ctx context.Context, key, value, description string) (*model.SystemConfigM, error)
	GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error)
	GetAll(ctx context.Context) ([]*model.SystemConfigM, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) // 分页查询，用于后台管理系统
	Update(ctx context.Context, key, value, description string) (*model.SystemConfigM, error)
	Delete(ctx context.Context, key string) error
	InitDefaultConfigs(ctx context.Context) error
}

type configBiz struct {
	ds store.IStore
}

var _ ConfigBiz = (*configBiz)(nil)

func New(ds store.IStore) *configBiz {
	return &configBiz{ds: ds}
}

func (b *configBiz) Create(ctx context.Context, key, value, description string) (*model.SystemConfigM, error) {
	config := &model.SystemConfigM{
		Key:         key,
		Value:       value,
		Description: description,
	}

	if err := b.ds.Configs().Create(ctx, config); err != nil {
		return nil, err
	}

	return config, nil
}

func (b *configBiz) GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error) {
	return b.ds.Configs().GetByKey(ctx, key)
}

func (b *configBiz) GetAll(ctx context.Context) ([]*model.SystemConfigM, error) {
	return b.ds.Configs().GetAll(ctx)
}

// List 分页查询系统配置（用于后台管理系统）
func (b *configBiz) List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) {
	return b.ds.Configs().List(ctx, offset, limit)
}

func (b *configBiz) Update(ctx context.Context, key, value, description string) (*model.SystemConfigM, error) {
	// 先获取现有配置
	config, err := b.ds.Configs().GetByKey(ctx, key)
	if err != nil {
		// 如果不存在，创建新配置
		return b.Create(ctx, key, value, description)
	}

	// 更新配置
	config.Value = value
	if description != "" {
		config.Description = description
	}
	config.UpdatedAt = time.Now()

	if err := b.ds.Configs().Update(ctx, config); err != nil {
		return nil, err
	}

	return config, nil
}

func (b *configBiz) Delete(ctx context.Context, key string) error {
	return b.ds.Configs().Delete(ctx, key)
}

func (b *configBiz) InitDefaultConfigs(ctx context.Context) error {
	return b.ds.Configs().InitDefaultConfigs(ctx)
}
