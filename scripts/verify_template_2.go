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
	// 加载配置
	viper.SetConfigName("config_dev")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./configs")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Failed to read config file: %v", err)
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

	// 验证template 2
	fmt.Println("验证Template 2...")
	template2, err := sopBiz.GetTemplate(ctx, 2)
	if err != nil {
		log.Fatalf("Failed to get template 2: %v", err)
	}

	fmt.Printf("\n✓ Template 2信息:\n")
	fmt.Printf("  ID: %d\n", template2.ID)
	fmt.Printf("  名称: %s\n", template2.Name)
	fmt.Printf("  描述: %s\n", template2.Description)
	fmt.Printf("  状态: %s\n", template2.Status)
	fmt.Printf("  系统提示词: %s (应为空)\n", template2.Prompt)
	if template2.Prompt == "" {
		fmt.Printf("  ✓ 系统提示词为空（符合要求）\n")
	} else {
		fmt.Printf("  ✗ 系统提示词不为空（不符合要求）\n")
	}

	// 验证节点
	nodes2, err := sopBiz.ListNodesByTemplate(ctx, 2)
	if err != nil {
		log.Fatalf("Failed to get template 2 nodes: %v", err)
	}

	fmt.Printf("\n✓ Template 2节点信息:\n")
	expectedNames := []string{"拆解产品", "拆解爆款朋友圈", "拆解语言风格", "仿写朋友圈"}
	
	if len(nodes2) != 4 {
		fmt.Printf("  ✗ 节点数量不正确: 期望4个，实际%d个\n", len(nodes2))
	} else {
		fmt.Printf("  ✓ 节点数量正确: 4个\n")
	}

	for i, node := range nodes2 {
		fmt.Printf("\n  节点 %d (sort=%d):\n", i+1, node.Sort)
		fmt.Printf("    ID: %d\n", node.ID)
		fmt.Printf("    名称: %s\n", node.Name)
		if i < len(expectedNames) && node.Name == expectedNames[i] {
			fmt.Printf("    ✓ 名称正确\n")
		} else {
			fmt.Printf("    ✗ 名称不正确: 期望 %s，实际 %s\n", expectedNames[i], node.Name)
		}
		fmt.Printf("    BaseURL: %s\n", node.BaseURL)
		fmt.Printf("    ModelName: %s\n", node.ModelName)
		fmt.Printf("    TimeoutSeconds: %d\n", node.TimeoutSeconds)
		fmt.Printf("    HasPrompt: %v\n", node.Prompt != "")
	}

	fmt.Println("\n✅ 验证完成！")
}




