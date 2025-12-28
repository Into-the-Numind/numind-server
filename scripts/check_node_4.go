package main

import (
	"context"
	"fmt"
	"log"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/pkg/db"

	"github.com/spf13/viper"
)

func main() {
	// 尝试加载不同的配置文件
	configFiles := []string{"config_dev", "config_qa", "config_local", "config_prod"}
	var configLoaded bool
	
	for _, configName := range configFiles {
		viper.SetConfigName(configName)
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("./configs")
		
		if err := viper.ReadInConfig(); err == nil {
			fmt.Printf("Loaded config: %s\n", configName)
			configLoaded = true
			break
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

	gormDB, err := db.NewMySQL(dbOptions)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 创建store和biz
	storeInstance := store.NewStore(gormDB)
	sopExecutor := sop.NewSopExecutor(storeInstance)
	sopBiz := sop.NewSopBiz(storeInstance, sopExecutor)

	ctx := context.Background()

	// 查询 node_id=4
	node, err := sopBiz.GetNode(ctx, 4)
	if err != nil {
		log.Fatalf("Failed to get node 4: %v", err)
	}

	fmt.Printf("\n【Node ID=4 信息】\n")
	fmt.Printf("  ID: %d\n", node.ID)
	fmt.Printf("  名称: %s\n", node.Name)
	fmt.Printf("  Template ID: %d\n", node.TemplateID)
	fmt.Printf("  Sort: %d\n", node.Sort)
	fmt.Printf("  是否有提示词: %v\n", node.Prompt != "")
	fmt.Printf("  提示词长度: %d\n", len(node.Prompt))
	if node.Prompt != "" {
		fmt.Printf("  提示词内容（前200字符）:\n")
		preview := node.Prompt
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Printf("    %s\n", preview)
	}
}

