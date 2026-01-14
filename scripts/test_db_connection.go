package main

import (
	"fmt"
	"log"

	"numind-server/pkg/db"

	"github.com/spf13/viper"
)

func main() {
	// 尝试加载不同的配置文件（优先尝试dev和qa，因为它们可能连接远程数据库）
	configFiles := []string{"config_dev", "config_qa", "config_local", "config_prod"}
	var configLoaded bool
	var loadedConfig string

	for _, configName := range configFiles {
		viper.SetConfigName(configName)
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("./configs")

		if err := viper.ReadInConfig(); err == nil {
			fmt.Printf("✓ Loaded config: %s\n", configName)
			configLoaded = true
			loadedConfig = configName

			// 测试这个配置的数据库连接
			testOptions := &db.MySQLOptions{
				Host:                  viper.GetString("db.host"),
				Username:              viper.GetString("db.username"),
				Password:              viper.GetString("db.password"),
				Database:              viper.GetString("db.database"),
				MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
				MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
				MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
				LogLevel:              viper.GetInt("db.log-level"),
			}

			// 快速测试连接
			testDB, err := db.NewMySQL(testOptions)
			if err == nil {
				sqlDB, _ := testDB.DB()
				if sqlDB != nil {
					if err := sqlDB.Ping(); err == nil {
						fmt.Printf("  → This config can connect to database!\n")
						break
					}
				}
			}
		}
	}

	if !configLoaded {
		log.Fatalf("Failed to load any config file. Tried: %v", configFiles)
	}

	// 初始化数据库
	dbOptions := &db.MySQLOptions{
		Host:                  viper.GetString("db.host"),
		Username:              viper.GetString("db.username"),
		Password:              viper.GetString("db.password"),
		Database:              viper.GetString("db.database"),
		MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
		MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
		MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
		LogLevel:              viper.GetInt("db.log-level"),
	}

	fmt.Printf("\nDatabase connection info:\n")
	fmt.Printf("  Config: %s\n", loadedConfig)
	fmt.Printf("  Host: %s\n", dbOptions.Host)
	fmt.Printf("  Database: %s\n", dbOptions.Database)
	fmt.Printf("  Username: %s\n", dbOptions.Username)
	fmt.Printf("  Password: %s\n", maskPassword(dbOptions.Password))

	fmt.Printf("\nAttempting to connect...\n")
	gormDB, err := db.NewMySQL(dbOptions)
	if err != nil {
		fmt.Printf("✗ Failed to connect: %v\n", err)
		return
	}

	// 测试查询
	sqlDB, err := gormDB.DB()
	if err != nil {
		fmt.Printf("✗ Failed to get underlying SQL DB: %v\n", err)
		return
	}

	if err := sqlDB.Ping(); err != nil {
		fmt.Printf("✗ Failed to ping database: %v\n", err)
		return
	}

	fmt.Printf("✓ Successfully connected to database!\n")

	// 测试查询template 1
	var count int64
	if err := gormDB.Table("sop_template").Where("id = ?", 1).Count(&count).Error; err != nil {
		fmt.Printf("✗ Failed to query template 1: %v\n", err)
		return
	}

	if count > 0 {
		fmt.Printf("✓ Template 1 exists in database\n")
	} else {
		fmt.Printf("⚠ Template 1 does not exist in database\n")
	}

	// 测试查询nodes
	var nodeCount int64
	if err := gormDB.Table("sop_node").Where("template_id = ?", 1).Count(&nodeCount).Error; err != nil {
		fmt.Printf("✗ Failed to query template 1 nodes: %v\n", err)
		return
	}

	fmt.Printf("✓ Template 1 has %d nodes\n", nodeCount)
}

func maskPassword(pwd string) string {
	if len(pwd) == 0 {
		return "(empty)"
	}
	if len(pwd) <= 4 {
		return "****"
	}
	return pwd[:2] + "****" + pwd[len(pwd)-2:]
}
