package main

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
	"numind-server/internal/numind/biz/pagination"
)

func main() {
	// 初始化 viper 配置
	viper.SetConfigName("config_local")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./configs")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	fmt.Println("=== 按行分页逻辑测试 ===")
	fmt.Println()

	// 创建分页配置
	config := pagination.GetDefaultConfig()
	
	// 创建按行分页引擎
	lineEngine := pagination.NewLineBasedPaginationEngine(config)
	
	// 创建标准分页引擎进行对比
	standardEngine := pagination.NewPaginationEngine(config)

	// 测试数据 - 模拟长文本内容
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "阅读的多样性与选择",
		},
		{
			Type: pagination.ElementTypeBody,
			Content: "阅读的多样性体现在不同类型的书籍能够满足不同读者的需求和兴趣。小说类书籍能够提供丰富的故事情节和人物塑造，让读者在阅读过程中获得情感体验和想象力的激发。科普类书籍则能够传递科学知识，帮助读者了解自然规律和科技发展。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"小说类：提供丰富的故事情节和人物塑造",
				"科普类：传递科学知识，了解自然规律",
				"历史类：了解过去事件，获得经验教训",
			},
		},
	}

	fmt.Printf("📝 测试数据：%d 个元素\n", len(testElements))
	for i, element := range testElements {
		fmt.Printf("  %d. [%s] %s\n", i+1, element.Type, truncateString(fmt.Sprintf("%v", element.Content), 50))
	}
	fmt.Println()

	// 使用标准分页引擎
	fmt.Println("=== 标准分页引擎结果 ===")
	standardResult, err := standardEngine.PaginateElements(testElements)
	if err != nil {
		log.Fatalf("标准分页失败: %v", err)
	}

	fmt.Printf("📊 标准分页结果：%d 张卡片\n", len(standardResult.Cards))
	for i, card := range standardResult.Cards {
		fmt.Printf("  卡片 %d: %d 个元素\n", i+1, len(card.Elements))
	}
	fmt.Println()

	// 使用按行分页引擎
	fmt.Println("=== 按行分页引擎结果 ===")
	lineResult, err := lineEngine.PaginateByLines(testElements)
	if err != nil {
		log.Fatalf("按行分页失败: %v", err)
	}

	fmt.Printf("📊 按行分页结果：%d 张卡片\n", len(lineResult.Cards))
	for i, card := range lineResult.Cards {
		fmt.Printf("  卡片 %d: %d 个元素\n", i+1, len(card.Elements))
	}
	fmt.Println()

	// 对比分析
	fmt.Println("=== 对比分析 ===")
	fmt.Printf("标准分页：%d 张卡片\n", len(standardResult.Cards))
	fmt.Printf("按行分页：%d 张卡片\n", len(lineResult.Cards))

	if len(lineResult.Cards) < len(standardResult.Cards) {
		fmt.Println("✅ 按行分页减少了卡片数量，提高了空间利用率")
	} else if len(lineResult.Cards) > len(standardResult.Cards) {
		fmt.Println("⚠️ 按行分页增加了卡片数量，可能需要进一步优化")
	} else {
		fmt.Println("📊 两种分页方式产生相同数量的卡片")
	}

	fmt.Println("\n=== 测试完成 ===")
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
