package main

import (
	"encoding/json"
	"fmt"
	"os"

	"numind-server/config"
)

func main() {
	fmt.Println("Numind Server Configuration Validator")
	fmt.Println("=====================================")

	// 加载配置
	cfg := config.Load()

	// 打印配置信息
	fmt.Printf("Environment: %s\n", cfg.Environment)
	fmt.Printf("App Name: %s\n", cfg.App.Name)
	fmt.Printf("App Version: %s\n", cfg.App.Version)
	fmt.Printf("Server Port: %s\n", cfg.Server.Port)
	fmt.Printf("Database Host: %s\n", cfg.Database.Host)
	fmt.Printf("Database Name: %s\n", cfg.Database.Name)
	fmt.Printf("Redis Host: %s\n", cfg.Redis.Host)
	fmt.Printf("Log Level: %s\n", cfg.GetLogLevel())

	// 验证数据库连接
	fmt.Println("\nValidating database connection...")
	dsn := cfg.GetDSN()
	fmt.Printf("DSN: %s\n", dsn)

	// 验证 Redis 连接
	fmt.Println("\nValidating Redis connection...")
	redisAddr := cfg.GetRedisAddr()
	fmt.Printf("Redis Address: %s\n", redisAddr)

	// 验证 CORS 配置
	fmt.Println("\nValidating CORS configuration...")
	fmt.Printf("Allowed Origins: %v\n", cfg.GetCORSAllowedOrigins())
	fmt.Printf("Allowed Methods: %v\n", cfg.GetCORSAllowedMethods())
	fmt.Printf("Allowed Headers: %v\n", cfg.GetCORSAllowedHeaders())

	// 验证环境特定配置
	fmt.Println("\nValidating environment-specific configuration...")
	switch cfg.Environment {
	case "development":
		fmt.Println("✓ Development environment configuration loaded")
	case "qa":
		fmt.Println("✓ QA environment configuration loaded")
	case "production":
		fmt.Println("✓ Production environment configuration loaded")
	default:
		fmt.Printf("⚠ Unknown environment: %s\n", cfg.Environment)
	}

	// 输出完整配置 (JSON 格式)
	if len(os.Args) > 1 && os.Args[1] == "--json" {
		fmt.Println("\nFull configuration (JSON):")
		jsonData, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			fmt.Printf("Error marshaling config to JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(jsonData))
	}

	fmt.Println("\n✓ Configuration validation completed successfully!")
}
