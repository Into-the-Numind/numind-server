package main

import (
	"fmt"

	"numind-server/internal/numind/biz/book"
	"numind-server/internal/pkg/model"
)

func main() {
	fmt.Println("🔖 关键词生成功能示例")
	fmt.Println("=======================")
	fmt.Println("")

	// 创建关键词生成器
	keywordGen := book.NewKeywordGenerator()
	defer keywordGen.Close()

	// 示例书籍数据
	books := []*model.BookM{
		{
			Title: "建议所有人积极夺回线下生活",
			Tags:  "生活,建议,线下",
		},
		{
			Title: "高手的暗箱",
			Tags:  "技术,高手,暗箱",
		},
		{
			Title: "我好像发现了魅力的本质!",
			Tags:  "魅力,发现,本质",
		},
		{
			Title: "美食摄影技巧大全",
			Tags:  "美食,摄影,技巧",
		},
		{
			Title: "旅行回忆录：日本京都之旅",
			Tags:  "旅行,回忆,日本,京都",
		},
	}

	fmt.Println("📚 原始书籍数据:")
	for i, book := range books {
		fmt.Printf("%d. 标题: %s\n   标签: %s\n", i+1, book.Title, book.Tags)
	}

	fmt.Println("\n🔍 开始生成关键词...")

	// 为每本书生成关键词
	for i, book := range books {
		keywords := keywordGen.GenerateBookKeywords(book)
		book.Keywords = keywords

		fmt.Printf("\n%d. 标题: %s\n", i+1, book.Title)
		fmt.Printf("   标签: %s\n", book.Tags)
		fmt.Printf("   关键词: %v\n", keywords)
	}

	fmt.Println("\n📊 关键词生成完成！")
	fmt.Println("")

	// 演示关键词匹配功能
	fmt.Println("🎯 关键词匹配示例:")

	// 创建搜索服务
	searchService := book.NewSearchService()
	defer searchService.Close()

	// 模拟用户搜索
	userQueries := []string{
		"美食",
		"旅行",
		"技术",
		"生活建议",
		"摄影",
	}

	for _, query := range userQueries {
		fmt.Printf("\n用户查询: \"%s\"\n", query)

		// 搜索相关书籍
		results := searchService.SearchBooks(nil, query, books, 3)

		fmt.Printf("找到 %d 本相关书籍:\n", len(results))
		for j, result := range results {
			fmt.Printf("  %d. %s (关键词: %v)\n", j+1, result.Title, result.Keywords)
		}
	}

	fmt.Println("\n✨ 示例程序执行完成！")
	fmt.Println("")
	fmt.Println("💡 功能说明:")
	fmt.Println("• 自动从标题和标签中提取关键词")
	fmt.Println("• 支持中文分词和停用词过滤")
	fmt.Println("• 关键词去重和清理")
	fmt.Println("• 智能搜索匹配")
	fmt.Println("• JSON字段存储支持")
}
