package config

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

type ConfigBiz interface {
	Create(ctx context.Context, key, title, value, description string) (*model.SystemConfigM, error)
	GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error)
	GetAll(ctx context.Context) ([]*model.SystemConfigM, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) // 分页查询，用于后台管理系统
	Update(ctx context.Context, key, value, description string) (*model.SystemConfigM, error)
	Delete(ctx context.Context, key string) error
	InitDefaultConfigs(ctx context.Context) error
	StartConfigChangeListener(ctx context.Context) error // 启动配置变更监听
}

type configBiz struct {
	ds    store.IStore
	cache ConfigCache
}

var _ ConfigBiz = (*configBiz)(nil)

func New(ds store.IStore) *configBiz {
	return &configBiz{
		ds:    ds,
		cache: NewConfigCache(ds),
	}
}

func (b *configBiz) Create(ctx context.Context, key, title, value, description string) (*model.SystemConfigM, error) {
	// 验证：只允许创建硬编码配置项
	if !isManagedConfig(key) {
		return nil, fmt.Errorf("配置键 '%s' 不在管理列表中，只能创建硬编码定义的配置项", key)
	}

	config := &model.SystemConfigM{
		Key:         key,
		Title:       title,
		Value:       value,
		Description: description,
	}

	if err := b.ds.Configs().Create(ctx, config); err != nil {
		return nil, err
	}

	// 写入Redis缓存
	if err := b.cache.Set(ctx, config); err != nil {
		log.Warnw("Failed to cache config after create", "key", key, "error", err)
	}

	// 发布配置变更通知
	if err := b.cache.PublishChange(ctx, key); err != nil {
		log.Warnw("Failed to publish config change notification", "key", key, "error", err)
	}

	return config, nil
}

// GetByKey 获取配置（优先从Redis缓存读取）
func (b *configBiz) GetByKey(ctx context.Context, key string) (*model.SystemConfigM, error) {
	return b.cache.Get(ctx, key)
}

func (b *configBiz) GetAll(ctx context.Context) ([]*model.SystemConfigM, error) {
	return b.ds.Configs().GetAll(ctx)
}

// List 分页查询系统配置（用于后台管理系统）
func (b *configBiz) List(ctx context.Context, offset, limit int) (int64, []*model.SystemConfigM, error) {
	return b.ds.Configs().List(ctx, offset, limit)
}

func (b *configBiz) Update(ctx context.Context, key, value, description string) (*model.SystemConfigM, error) {
	// 验证：只允许更新硬编码配置项
	if !isManagedConfig(key) {
		return nil, fmt.Errorf("配置键 '%s' 不在管理列表中，只能更新硬编码定义的配置项", key)
	}

	// 先获取现有配置
	config, err := b.ds.Configs().GetByKey(ctx, key)
	if err != nil {
		// 如果不存在，创建新配置（这种情况不应该发生，但为了安全还是处理）
		return b.Create(ctx, key, "", value, description)
	}

	// 更新配置
	config.Value = value
	if description != "" {
		config.Description = description
	}
	// UpdatedAt 由 gorm.Model 自动管理，无需手动设置

	if err := b.ds.Configs().Update(ctx, config); err != nil {
		return nil, err
	}

	// 更新Redis缓存
	if err := b.cache.Set(ctx, config); err != nil {
		log.Warnw("Failed to cache config after update", "key", key, "error", err)
	}

	// 发布配置变更通知
	if err := b.cache.PublishChange(ctx, key); err != nil {
		log.Warnw("Failed to publish config change notification", "key", key, "error", err)
	}

	log.Infow("Config updated and notification sent", "key", key)
	return config, nil
}

func (b *configBiz) Delete(ctx context.Context, key string) error {
	// 验证：只允许删除硬编码配置项
	if !isManagedConfig(key) {
		return fmt.Errorf("配置键 '%s' 不在管理列表中，只能删除硬编码定义的配置项", key)
	}

	if err := b.ds.Configs().Delete(ctx, key); err != nil {
		return err
	}

	// 删除Redis缓存
	if err := b.cache.Delete(ctx, key); err != nil {
		log.Warnw("Failed to delete config from cache", "key", key, "error", err)
	}

	// 发布配置变更通知
	if err := b.cache.PublishChange(ctx, key); err != nil {
		log.Warnw("Failed to publish config change notification", "key", key, "error", err)
	}

	return nil
}

func (b *configBiz) InitDefaultConfigs(ctx context.Context) error {
	// 获取需要管理的配置定义
	configDefs := GetManagedConfigDefinitions()

	// 获取数据库中所有现有的配置
	existingConfigs, err := b.ds.Configs().GetAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to get existing configs: %w", err)
	}

	// 创建现有配置的映射表，方便查找
	existingMap := make(map[string]*model.SystemConfigM)
	for i := range existingConfigs {
		existingMap[existingConfigs[i].Key] = existingConfigs[i]
	}

	// 创建需要管理的配置键集合
	managedKeys := make(map[string]bool)
	for _, def := range configDefs {
		managedKeys[def.Key] = true
	}

	// 1. 添加或更新需要管理的配置项
	for _, def := range configDefs {
		existing, exists := existingMap[def.Key]
		if !exists {
			// 数据库中没有，创建新配置
			newConfig := &model.SystemConfigM{
				Key:         def.Key,
				Title:       def.Title,
				Value:       def.DefaultValue,
				Description: def.Description,
			}
			if err := b.ds.Configs().Create(ctx, newConfig); err != nil {
				log.Warnw("Failed to create config", "key", def.Key, "error", err)
				continue
			}
			log.Infow("Created new config", "key", def.Key, "title", def.Title)

			// 写入Redis缓存
			if err := b.cache.Set(ctx, newConfig); err != nil {
				log.Warnw("Failed to cache new config", "key", def.Key, "error", err)
			}
		} else {
			// 数据库中存在，更新标题和描述（如果标题或描述有变化）
			// 注意：不覆盖用户已修改的Value值
			needsUpdate := false
			if existing.Title != def.Title {
				existing.Title = def.Title
				needsUpdate = true
			}
			if existing.Description != def.Description {
				existing.Description = def.Description
				needsUpdate = true
			}

			if needsUpdate {
				if err := b.ds.Configs().Update(ctx, existing); err != nil {
					log.Warnw("Failed to update config metadata", "key", def.Key, "error", err)
				} else {
					log.Infow("Updated config metadata", "key", def.Key, "title", def.Title)
				}
			}

			// 无论元数据是否变化，都确保写入Redis缓存（同步数据库中的value到Redis）
			if err := b.cache.Set(ctx, existing); err != nil {
				log.Warnw("Failed to sync config to Redis", "key", def.Key, "error", err)
			} else {
				log.Debugw("Synced config to Redis", "key", def.Key)
			}
		}
	}

	// 2. 删除不在管理列表中的配置（只删除硬编码定义之外的配置）
	// 注意：只删除那些不在硬编码管理列表中的配置项
	for key := range existingMap {
		if !managedKeys[key] {
			// 这个配置不在硬编码管理列表中，删除它
			if err := b.ds.Configs().Delete(ctx, key); err != nil {
				log.Warnw("Failed to delete unmanaged config", "key", key, "error", err)
			} else {
				log.Infow("Deleted unmanaged config", "key", key)
				// 删除Redis缓存
				if err := b.cache.Delete(ctx, key); err != nil {
					log.Warnw("Failed to delete cache", "key", key, "error", err)
				}
			}
		}
	}

	log.Infow("Config synchronization completed",
		"total_managed", len(configDefs),
		"total_existing", len(existingConfigs))
	return nil
}

// StartConfigChangeListener 启动配置变更监听器
func (b *configBiz) StartConfigChangeListener(ctx context.Context) error {
	return b.cache.SubscribeChanges(ctx, func(key string) {
		// 当收到配置变更通知时，刷新本地缓存
		log.Infow("Refreshing config cache due to change notification", "key", key)

		// 从数据库重新加载配置并更新缓存
		config, err := b.ds.Configs().GetByKey(ctx, key)
		if err != nil {
			log.Warnw("Failed to reload config after change notification", "key", key, "error", err)
			// 如果配置不存在，删除缓存
			_ = b.cache.Delete(ctx, key)
		} else {
			// 更新缓存
			if err := b.cache.Set(ctx, config); err != nil {
				log.Warnw("Failed to update cache after change notification", "key", key, "error", err)
			}
		}
	})
}

// isManagedConfig 检查配置键是否在硬编码管理列表中
func isManagedConfig(key string) bool {
	configDefs := GetManagedConfigDefinitions()
	for _, def := range configDefs {
		if def.Key == key {
			return true
		}
	}
	return false
}
