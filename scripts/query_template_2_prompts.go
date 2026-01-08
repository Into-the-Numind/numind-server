package main

import (
	"context"
	"fmt"
	"log"
	"strings"

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

	// 查询template 2
	fmt.Println("=" + strings.Repeat("=", 80))
	fmt.Println("Template 2 提示词查询结果")
	fmt.Println("=" + strings.Repeat("=", 80))
	
	template2, err := sopBiz.GetTemplate(ctx, 2)
	if err != nil {
		log.Fatalf("Failed to get template 2: %v", err)
	}

	fmt.Printf("\n【Template 2 基本信息】\n")
	fmt.Printf("  ID: %d\n", template2.ID)
	fmt.Printf("  名称: %s\n", template2.Name)
	fmt.Printf("  描述: %s\n", template2.Description)
	fmt.Printf("  状态: %s\n", template2.Status)
	fmt.Printf("\n【Template 级别的提示词】\n")
	if template2.Prompt == "" {
		fmt.Printf("  来源: sop_template 表的 prompt 字段\n")
		fmt.Printf("  内容: (空)\n")
		fmt.Printf("  说明: Template 2 没有配置系统级提示词\n")
	} else {
		fmt.Printf("  来源: sop_template 表的 prompt 字段\n")
		fmt.Printf("  内容:\n")
		fmt.Printf("  %s\n", template2.Prompt)
	}

	// 查询所有节点
	nodes2, err := sopBiz.ListNodesByTemplate(ctx, 2)
	if err != nil {
		log.Fatalf("Failed to get template 2 nodes: %v", err)
	}

	fmt.Printf("\n【节点信息】\n")
	fmt.Printf("  总节点数: %d\n", len(nodes2))
	
	for i, node := range nodes2 {
		fmt.Printf("\n  【步骤 %d】 (sort=%d)\n", i+1, node.Sort)
		fmt.Printf("    节点ID: %d\n", node.ID)
		fmt.Printf("    节点名称: %s\n", node.Name)
		fmt.Printf("    提示词来源: sop_node 表的 prompt 字段\n")
		if node.Prompt == "" {
			fmt.Printf("    提示词内容: (空)\n")
			fmt.Printf("    说明: 该节点没有配置提示词，将直接使用输入内容\n")
		} else {
			fmt.Printf("    提示词内容:\n")
			// 按行显示，每行前面加缩进
			lines := strings.Split(node.Prompt, "\n")
			for _, line := range lines {
				fmt.Printf("      %s\n", line)
			}
			fmt.Printf("\n    使用方式: 提示词 + \"\\n\\n\" + 节点输入内容\n")
		}
		fmt.Printf("    其他配置:\n")
		fmt.Printf("      BaseURL: %s\n", node.BaseURL)
		fmt.Printf("      ModelName: %s\n", node.ModelName)
		fmt.Printf("      TimeoutSeconds: %d\n", node.TimeoutSeconds)
	}

	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Println("查询完成！")
}

