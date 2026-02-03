package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"numind-server/internal/numind/biz/wecom"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Config 服务配置
type Config struct {
	CorpID         string        // 企业ID
	Secret         string        // 会话存档 Secret
	PrivateKeyPath string        // RSA 私钥路径
	MySQLDSN       string        // MySQL 连接字符串
	PollInterval   time.Duration // 拉取间隔
}

func main() {
	log.Println("🚀 WeCom Agent Service Starting...")

	// 加载配置
	cfg := loadConfig()

	// 初始化数据库连接
	db, err := initDB(cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("❌ Failed to connect to database: %v", err)
	}
	log.Println("✅ Database connected")

	// 自动迁移表结构 (可选，生产环境建议关闭)
	if err := db.AutoMigrate(&wecom.WecomUser{}, &wecom.WecomMessage{}, &wecom.WecomCursor{}); err != nil {
		log.Fatalf("❌ Failed to migrate database: %v", err)
	}

	// 初始化 SDK 客户端
	var client *wecom.Client
	if cfg.CorpID != "" && cfg.Secret != "" {
		var err error
		client, err = wecom.NewClient(cfg.CorpID, cfg.Secret, cfg.PrivateKeyPath)
		if err != nil {
			log.Printf("❌ Failed to init WeWork SDK: %v", err)
			// 注意：如果 SDK 初始化失败，client 为 nil，Poller 会处理这种情况
		} else {
			log.Println("✅ WeWork SDK initialized")
			defer client.Close()
		}
	}

	// 上下文控制
	ctx, cancel := context.WithCancel(context.Background())

	// 检查是否启用 Poller (默认 true)
	pollerEnabled := getEnv("WECOM_POLLER_ENABLED", "true") == "true"
	if pollerEnabled {
		// 创建 Poller 服务
		poller := wecom.NewPoller(db, client, cfg.PrivateKeyPath, cfg.PollInterval)

		// 启动轮询（在单独的 goroutine 中）
		go poller.Start(ctx)
		log.Printf("✅ Poller started with interval: %v", cfg.PollInterval)
	} else {
		log.Println("⏸️ Poller is disabled by WECOM_POLLER_ENABLED=false")
	}

	// 优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down...")
	cancel()
	time.Sleep(time.Second) // 等待 goroutine 退出
	log.Println("👋 Goodbye!")
}

// loadConfig 从环境变量加载配置
func loadConfig() *Config {
	cfg := &Config{
		CorpID:         getEnv("WECOM_CORP_ID", ""),
		Secret:         getEnv("WECOM_SECRET", ""),
		PrivateKeyPath: getEnv("WECOM_PRIVATE_KEY_PATH", "/app/private.pem"),
		MySQLDSN:       getEnv("MYSQL_DSN", "root:Numind2025@tcp(numind-mysql-dev:3306)/numind?charset=utf8mb4&parseTime=True&loc=Local"),
		PollInterval:   3 * time.Second,
	}

	// 校验必要配置
	if cfg.CorpID == "" {
		log.Println("⚠️  WECOM_CORP_ID not set, SDK will not be initialized")
	}
	if cfg.Secret == "" {
		log.Println("⚠️  WECOM_SECRET not set, SDK will not be initialized")
	}

	return cfg
}

// initDB 初始化 MySQL 连接
func initDB(dsn string) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
}

// getEnv 获取环境变量，带默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
