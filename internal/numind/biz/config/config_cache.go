package config

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/redis"
)

const (
	// Redis配置缓存键前缀
	configCacheKeyPrefix = "config:"
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

// Get 获取配置（优先从Redis读取，未命中则查数据库并缓存）
func (c *configCache) Get(ctx context.Context, key string) (*model.SystemConfigM, error) {
	redisClient := redis.GetClient()
	if redisClient == nil {
		// Redis未初始化，直接查数据库
		log.Debugw("Redis not available, querying database directly", "key", key)
		return c.store.Configs().GetByKey(ctx, key)
	}

	cacheKey := configCacheKeyPrefix + key

	// 1. 尝试从Redis读取
	val, err := redisClient.Get(ctx, cacheKey).Result()
	if err == nil {
		// 缓存命中，反序列化
		var config model.SystemConfigM
		if err := json.Unmarshal([]byte(val), &config); err != nil {
			log.Warnw("Failed to unmarshal config from cache", "key", key, "error", err)
			// 缓存数据损坏，删除并继续查数据库
			redisClient.Del(ctx, cacheKey)
		} else {
			log.Debugw("Config cache hit", "key", key)
			return &config, nil
		}
	}

	// 2. 缓存未命中，查数据库
	log.Debugw("Config cache miss, querying database", "key", key)
	config, err := c.store.Configs().GetByKey(ctx, key)
	if err != nil {
		return nil, err
	}

	// 3. 写入Redis缓存
	if err := c.Set(ctx, config); err != nil {
		log.Warnw("Failed to cache config", "key", key, "error", err)
		// 缓存失败不影响返回结果
	}

	return config, nil
}

// Set 设置配置到Redis缓存
func (c *configCache) Set(ctx context.Context, config *model.SystemConfigM) error {
	redisClient := redis.GetClient()
	if redisClient == nil {
		return nil // Redis未初始化，静默失败
	}

	cacheKey := configCacheKeyPrefix + config.Key
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// 如果过期时间为0，则设置为永久有效（不设置过期时间）
	// 在 go-redis 中，传递 0 或 time.Duration(0) 表示永久有效
	return redisClient.Set(ctx, cacheKey, data, configCacheExpiration).Err()
}

// Delete 删除Redis缓存
func (c *configCache) Delete(ctx context.Context, key string) error {
	redisClient := redis.GetClient()
	if redisClient == nil {
		return nil // Redis未初始化，静默失败
	}

	cacheKey := configCacheKeyPrefix + key
	return redisClient.Del(ctx, cacheKey).Err()
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
