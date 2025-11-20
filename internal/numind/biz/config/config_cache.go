package config

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/redis"
)

const (
	// Redis配置变更通知频道
	configChangeChannel = "config:change"
	// 配置缓存过期时间（0表示永久有效）
	configCacheExpiration = 0
)

// ConfigCache 配置缓存接口
type ConfigCache interface {
	Get(ctx context.Context, key string) (*model.SystemConfigM, error)
	Set(ctx context.Context, config *model.SystemConfigM) error
	Delete(ctx context.Context, key string) error
	PublishChange(ctx context.Context, key string) error
	SubscribeChanges(ctx context.Context, handler func(key string)) error
}

type configCache struct {
	store store.IStore
}

// NewConfigCache 创建配置缓存实例
func NewConfigCache(store store.IStore) ConfigCache {
	return &configCache{store: store}
}

// Get 获取配置（优先从Redis读取value，未命中则查数据库并缓存）
func (c *configCache) Get(ctx context.Context, key string) (*model.SystemConfigM, error) {
	redisClient := redis.GetClient()
	if redisClient == nil {
		// Redis未初始化，直接查数据库
		log.Debugw("Redis not available, querying database directly", "key", key)
		return c.store.Configs().GetByKey(ctx, key)
	}

	// 1. 尝试从Redis读取value（直接使用key作为Redis的key）
	val, err := redisClient.Get(ctx, key).Result()
	if err == nil {
		// 缓存命中，从数据库获取完整配置信息，然后更新value
		config, err := c.store.Configs().GetByKey(ctx, key)
		if err != nil {
			// 数据库中没有，但Redis有，删除Redis中的无效数据
			redisClient.Del(ctx, key)
			return nil, err
		}
		// 使用Redis中的value更新配置
		config.Value = val
		log.Debugw("Config cache hit", "key", key)
		return config, nil
	}

	// 2. 缓存未命中，查数据库
	log.Debugw("Config cache miss, querying database", "key", key)
	config, err := c.store.Configs().GetByKey(ctx, key)
	if err != nil {
		return nil, err
	}

	// 3. 写入Redis缓存（只存储value）
	if err := c.Set(ctx, config); err != nil {
		log.Warnw("Failed to cache config", "key", key, "error", err)
		// 缓存失败不影响返回结果
	}

	return config, nil
}

// Set 设置配置到Redis缓存（直接使用key作为Redis的key，value作为Redis的value）
func (c *configCache) Set(ctx context.Context, config *model.SystemConfigM) error {
	redisClient := redis.GetClient()
	if redisClient == nil {
		log.Warnw("Redis client not initialized, skipping cache set", "key", config.Key)
		return nil // Redis未初始化，静默失败
	}

	// 直接使用config.Key作为Redis的key，config.Value作为Redis的value
	// 如果过期时间为0，则设置为永久有效（不设置过期时间）
	// 在 go-redis 中，传递 0 或 time.Duration(0) 表示永久有效
	err := redisClient.Set(ctx, config.Key, config.Value, configCacheExpiration).Err()
	if err != nil {
		log.Errorw("Failed to set config to Redis", "key", config.Key, "error", err)
		return err
	}
	log.Debugw("Config set to Redis successfully", "key", config.Key, "value_length", len(config.Value))
	return nil
}

// Delete 删除Redis缓存（直接使用key作为Redis的key）
func (c *configCache) Delete(ctx context.Context, key string) error {
	redisClient := redis.GetClient()
	if redisClient == nil {
		return nil // Redis未初始化，静默失败
	}

	// 直接使用key作为Redis的key
	return redisClient.Del(ctx, key).Err()
}

// PublishChange 发布配置变更通知
func (c *configCache) PublishChange(ctx context.Context, key string) error {
	return redis.Publish(ctx, configChangeChannel, key)
}

// SubscribeChanges 订阅配置变更通知
func (c *configCache) SubscribeChanges(ctx context.Context, handler func(key string)) error {
	ch, err := redis.Subscribe(ctx, configChangeChannel)
	if err != nil {
		return fmt.Errorf("failed to subscribe to config changes: %w", err)
	}

	// 在goroutine中处理消息
	go func() {
		for msg := range ch {
			if msg != nil {
				key := msg.Payload
				log.Infow("Received config change notification", "key", key)
				handler(key)
			}
		}
	}()

	log.Infow("Subscribed to config change notifications", "channel", configChangeChannel)
	return nil
}
