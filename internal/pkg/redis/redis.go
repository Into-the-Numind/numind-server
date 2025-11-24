package redis

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

var (
	client *redis.Client
)

// Init 初始化Redis客户端
func Init() error {
	host := viper.GetString("redis.host")
	if host == "" {
		host = "localhost"
	}
	port := viper.GetInt("redis.port")
	if port == 0 {
		port = 6379
	}
	password := viper.GetString("redis.password")
	db := viper.GetInt("redis.db")
	poolSize := viper.GetInt("redis.pool_size")
	if poolSize == 0 {
		poolSize = 10
	}
	minIdleConns := viper.GetInt("redis.min_idle_conns")
	if minIdleConns == 0 {
		minIdleConns = 5
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client = redis.NewClient(&redis.Options{
		Addr:            addr,
		Password:        password,
		DB:              db,
		PoolSize:        poolSize,
		MinIdleConns:    minIdleConns,
		DisableIdentity: true, // 禁用身份标识功能，避免 maint_notifications 错误
	})

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		log.Warnw("Failed to connect to Redis", "error", err, "addr", addr)
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	// 注意：如果看到 "maint_notifications" 相关的警告，这是 go-redis 库内部的自动降级机制
	// 当 Redis 服务器不支持 maint_notifications 命令时，库会自动禁用该功能
	// 这个警告不影响 Redis 连接和功能使用，可以安全忽略
	log.Infow("Redis connected successfully", "addr", addr, "db", db)
	return nil
}

// GetClient 获取Redis客户端
func GetClient() *redis.Client {
	return client
}

// Close 关闭Redis连接
func Close() error {
	if client != nil {
		if err := client.Close(); err != nil {
			log.Warnw("Failed to close Redis client", "error", err)
			return err
		}
	}
	return nil
}

// Subscribe 订阅Redis频道
func Subscribe(ctx context.Context, channel string) (<-chan *redis.Message, error) {
	if client == nil {
		return nil, fmt.Errorf("Redis client not initialized")
	}

	pubsub := client.Subscribe(ctx, channel)
	ch := pubsub.Channel()
	return ch, nil
}

// Publish 发布消息到Redis频道
func Publish(ctx context.Context, channel string, message string) error {
	if client == nil {
		return fmt.Errorf("Redis client not initialized")
	}

	return client.Publish(ctx, channel, message).Err()
}
