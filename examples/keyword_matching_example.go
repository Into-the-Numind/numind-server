package main

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/book"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

func main() {
	fmt.Println("=== 关键词匹配功能演示 ===\n")

	// 1. 创建关键词匹配器
	matcher := util.NewKeywordMatcher()
	defer matcher.Close()

	// 2. 模拟用户查询
	userQuery := "旅行照片卡册"
	fmt.Printf("用户查询: %s\n", userQuery)

	// 3. 提取用户关键词
	userKeywords := matcher.GetKeywords(userQuery)
	fmt.Printf("提取的关键词: %v\n\n", userKeywords)

	// 4. 模拟书籍数据
	books := []*model.BookM{
		{
			Model: gorm.Model{ID: 1},
			Title: "旅行照片卡册",
			Tags:  "旅行,摄影,回忆",
		},
		{
			Model: gorm.Model{ID: 2},
			Title: "美食烹饪菜谱",
			Tags:  "美食,家常菜,烹饪",
		},
		{
			Model: gorm.Model{ID: 3},
			Title: "人工智能技术",
			Tags:  "技术,编程,AI",
		},
		{
			Model: gorm.Model{ID: 4},
			Title: "旅行日记",
			Tags:  "旅行,日记,生活",
		},
		{
			Model: gorm.Model{ID: 5},
			Title: "摄影技巧大全",
			Tags:  "摄影,技巧,相机",
		},
	}

	// 5. 计算每本书的匹配分数
	fmt.Println("书籍匹配分数:")
	fmt.Println("ID\t标题\t\t\t标签\t\t\t匹配分数")
	fmt.Println("--\t----\t\t\t----\t\t\t------")

	for _, book := range books {
		score := matcher.MatchScore(userKeywords, book.Title, book.Tags)
		fmt.Printf("%d\t%-15s\t%-15s\t%d\n",
			book.ID, book.Title, book.Tags, score)
	}

	// 6. 使用搜索服务进行排序
	searchService := book.NewSearchService()
	defer searchService.Close()

	// 模拟搜索
	searchResults := searchService.SearchBooks(context.Background(), userQuery, books, 3)

	fmt.Printf("\n搜索结果 (前3本):\n")
	for i, book := range searchResults {
		fmt.Printf("%d. %s (ID: %d, 标签: %s)\n",
			i+1, book.Title, book.ID, book.Tags)
	}

	fmt.Println("\n=== 演示完成 ===")
}
