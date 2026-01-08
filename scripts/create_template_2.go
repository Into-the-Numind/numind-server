package main

import (
	"context"
	"fmt"
	"log"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/pkg/db"

	"github.com/spf13/viper"
)

func main() {
	// 尝试加载不同的配置文件（优先尝试dev和qa，因为它们连接远程数据库）
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
		log.Fatalf(`
Failed to connect to database: %v

Database connection info:
  Host: %s
  Database: %s
  Username: %s

Please ensure:
  1. MySQL server is running
  2. Database credentials are correct
  3. Network connectivity is available

Alternative: You can use the SQL script instead:
  scripts/create_template_2.sql

Or run this script when the database is available.
`, err, dbOptions.Host, dbOptions.Database, dbOptions.Username)
	}

	// 创建store和biz
	storeInstance := store.NewStore(gormDB)
	sopExecutor := sop.NewSopExecutor(storeInstance)
	sopBiz := sop.NewSopBiz(storeInstance, sopExecutor)

	ctx := context.Background()

	// 步骤1: 查询template 1的配置
	fmt.Println("步骤1: 查询template 1的配置...")
	template1, err := sopBiz.GetTemplate(ctx, 1)
	if err != nil {
		log.Fatalf("Failed to get template 1: %v", err)
	}
	fmt.Printf("Template 1: ID=%d, Name=%s, Description=%s, Prompt=%s\n", 
		template1.ID, template1.Name, template1.Description, template1.Prompt)

	// 查询template 1的所有节点
	nodes1, err := sopBiz.ListNodesByTemplate(ctx, 1)
	if err != nil {
		log.Fatalf("Failed to get template 1 nodes: %v", err)
	}
	fmt.Printf("Template 1 has %d nodes\n", len(nodes1))

	if len(nodes1) != 4 {
		log.Fatalf("Template 1 should have 4 nodes, but got %d", len(nodes1))
	}

	// 打印节点信息
	for i, node := range nodes1 {
		fmt.Printf("Node %d (sort=%d): Name=%s, BaseURL=%s, ModelName=%s, Timeout=%d, HasPrompt=%v\n",
			i+1, node.Sort, node.Name, node.BaseURL, node.ModelName, node.TimeoutSeconds, node.Prompt != "")
	}

	// 步骤2: 创建template 2
	fmt.Println("\n步骤2: 创建template 2...")
	template2, err := sopBiz.CreateTemplate(ctx, "感悟型朋友圈创作", "", "")
	if err != nil {
		log.Fatalf("Failed to create template 2: %v", err)
	}
	fmt.Printf("Template 2 created: ID=%d, Name=%s\n", template2.ID, template2.Name)

	// 步骤3: 创建4个节点
	nodeNames := []string{"拆解产品", "拆解爆款朋友圈", "拆解语言风格", "仿写朋友圈"}

	for i, node1 := range nodes1 {
		fmt.Printf("\n步骤3.%d: 创建节点 %d (sort=%d, name=%s)...\n", i+1, i+1, node1.Sort, nodeNames[i])
		
		newNode := &model.SopNode{
			TemplateID:     template2.ID,
			ParentID:       node1.ParentID,
			Name:           nodeNames[i],
			BaseURL:        node1.BaseURL,
			ModelName:      node1.ModelName,
			APIKey:         node1.APIKey,
			TimeoutSeconds: node1.TimeoutSeconds,
			Sort:           node1.Sort,
			Status:         node1.Status,
			Prompt:         node1.Prompt,
			IsRoot:         node1.IsRoot,
		}

		createdNode, err := sopBiz.CreateNode(ctx, newNode)
		if err != nil {
			log.Fatalf("Failed to create node %d: %v", i+1, err)
		}
		fmt.Printf("Node %d created: ID=%d, Name=%s, Sort=%d\n", i+1, createdNode.ID, createdNode.Name, createdNode.Sort)
	}

	// 步骤4: 验证template 2
	fmt.Println("\n步骤4: 验证template 2...")
	nodes2, err := sopBiz.ListNodesByTemplate(ctx, template2.ID)
	if err != nil {
		log.Fatalf("Failed to get template 2 nodes: %v", err)
	}
	fmt.Printf("Template 2 has %d nodes\n", len(nodes2))

	if len(nodes2) != 4 {
		log.Fatalf("Template 2 should have 4 nodes, but got %d", len(nodes2))
	}

	for i, node := range nodes2 {
		fmt.Printf("Node %d (sort=%d): Name=%s, BaseURL=%s, ModelName=%s\n",
			i+1, node.Sort, node.Name, node.BaseURL, node.ModelName)
	}

	fmt.Println("\n✅ 所有步骤完成！Template 2创建成功。")
}

