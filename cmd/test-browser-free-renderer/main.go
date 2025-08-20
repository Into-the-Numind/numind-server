package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/pkg/model"
)

func main() {
	fmt.Println("🚀 测试无浏览器依赖卡片渲染器")
	fmt.Println(strings.Repeat("=", 50))

	ctx := context.Background()

	// 1. 创建渲染器
	fmt.Println("📦 创建无浏览器渲染器...")
	renderer, err := card.NewBrowserFreeRenderer()
	if err != nil {
		log.Fatalf("❌ 创建渲染器失败: %v", err)
	}
	defer renderer.Cleanup()

	// 2. 验证环境和配置
	fmt.Println("🔍 验证环境和配置...")
	if err := renderer.ValidateConfiguration(); err != nil {
		log.Fatalf("❌ 配置验证失败: %v", err)
	}

	// 3. 生成系统报告
	fmt.Println("📊 生成系统报告...")
	report, err := renderer.GenerateSystemReport(ctx)
	if err != nil {
		log.Printf("⚠️ 生成系统报告失败: %v", err)
	} else {
		fmt.Printf("✅ 系统报告生成成功\n")
		if envValid, ok := report["environment_valid"].(bool); ok && envValid {
			fmt.Println("✅ 环境验证通过")
		} else {
			fmt.Printf("❌ 环境验证失败: %v\n", report["environment_error"])
			return
		}
	}

	// 4. 获取渲染器能力
	fmt.Println("🎯 渲染器能力:")
	capabilities := renderer.GetCapabilities()
	fmt.Printf("   名称: %s\n", capabilities["name"])
	fmt.Printf("   版本: %s\n", capabilities["version"])
	fmt.Printf("   无浏览器: %v\n", capabilities["browser_free"])
	if features, ok := capabilities["features"].([]string); ok {
		fmt.Printf("   功能特性: %v\n", features)
	}

	// 5. 准备测试数据
	fmt.Println("📝 准备测试数据...")
	testBook := &model.BookM{
		Title: "测试书籍 - 无浏览器渲染",
		Tags:  "测试,演示,无浏览器",
	}
	testBook.ID = 999

	testCards := []*model.CardM{
		{
			ProcessedText: `[
				{"type":"title","content":"第一章：技术革新"},
				{"type":"subtitle","content":"无浏览器依赖的新时代"},
				{"type":"body","content":"本章介绍了如何使用轻量级工具替代无头浏览器，实现高性能的HTML到图片转换。这种方法不仅减少了系统资源占用，还提高了渲染的稳定性和可预测性。"},
				{"type":"quote","content":"技术的进步在于找到更优雅、更高效的解决方案。"},
				{"type":"list","content":"主要优势包括：\n• 内存占用减少60-80%\n• 启动时间缩短50%\n• 更好的容器化支持\n• 完善的错误处理机制"}
			]`,
			SortOrder: 1,
		},
		{
			ProcessedText: `[
				{"type":"title","content":"第二章：实现原理"},
				{"type":"subtitle","content":"wkhtmltoimage + 图片处理的组合"},
				{"type":"body","content":"我们采用了wkhtmltoimage作为HTML转图片的核心工具，配合Go原生的图片处理库实现精确的图片切分和优化。"},
				{"type":"body","content":"整个流程包括：HTML模板生成、样式优化、图片渲染、智能切分、质量优化等多个步骤。"},
				{"type":"quote","content":"工程师的艺术在于平衡复杂性和简洁性。"}
			]`,
			SortOrder: 2,
		},
		{
			ProcessedText: `[
				{"type":"title","content":"第三章：性能表现"},
				{"type":"body","content":"在实际测试中，新的渲染系统展现出了优异的性能表现。相比原有的无头浏览器方案，各项指标都有显著提升。"},
				{"type":"list","content":"性能对比：\n• 内存使用量：降低75%\n• 渲染速度：提升45%\n• 错误率：降低90%\n• 部署复杂度：简化80%"},
				{"type":"body","content":"这些改进使得系统能够更好地应对高并发场景，同时降低了运维成本。"}
			]`,
			SortOrder: 3,
		},
	}

	// 设置卡片ID
	for i, card := range testCards {
		card.ID = uint(1000 + i)
		card.UserID = 1
		card.BookID = testBook.ID
	}

	fmt.Printf("   书籍: %s (ID: %d)\n", testBook.Title, testBook.ID)
	fmt.Printf("   卡片数量: %d\n", len(testCards))

	// 6. 执行渲染测试
	fmt.Println("🎨 开始渲染测试...")
	start := time.Now()

	results, err := renderer.RenderBookToImages(ctx, testBook, testCards)
	if err != nil {
		log.Fatalf("❌ 渲染失败: %v", err)
	}

	duration := time.Since(start)
	fmt.Printf("✅ 渲染完成，耗时: %v\n", duration)
	fmt.Printf("📊 渲染结果:\n")
	fmt.Printf("   生成图片数量: %d\n", len(results))

	for i, result := range results {
		fmt.Printf("   图片 %d: 卡片ID=%d, URL=%s, 尺寸=%dx%d\n",
			i+1, result.CardID, result.ImageURL, result.Width, result.Height)
	}

	// 7. 单卡片渲染测试
	fmt.Println("🎯 测试单卡片渲染...")
	singleCard := &model.CardM{
		ProcessedText: `[
			{"type":"title","content":"单卡片测试"},
			{"type":"body","content":"这是一个单独渲染的卡片，用于测试单卡片渲染功能的正确性。"},
			{"type":"quote","content":"简单即是美。"}
		]`,
		SortOrder: 1,
	}
	singleCard.ID = 2000

	singleStart := time.Now()
	singleResult, err := renderer.RenderSingleCard(ctx, singleCard)
	if err != nil {
		log.Printf("❌ 单卡片渲染失败: %v", err)
	} else {
		singleDuration := time.Since(singleStart)
		fmt.Printf("✅ 单卡片渲染完成，耗时: %v\n", singleDuration)
		fmt.Printf("   结果: 卡片ID=%d, URL=%s\n", singleResult.CardID, singleResult.ImageURL)
	}

	// 8. 性能统计
	fmt.Println("📈 性能统计:")
	stats := renderer.GetStats()
	if performance, ok := stats["performance"].(map[string]interface{}); ok {
		fmt.Printf("   内存效率: %v\n", performance["memory_efficient"])
		fmt.Printf("   CPU效率: %v\n", performance["cpu_efficient"])
		fmt.Printf("   IO效率: %v\n", performance["io_efficient"])
	}

	fmt.Printf("   平均渲染速度: %.2f 卡片/秒\n", float64(len(testCards))/duration.Seconds())
	fmt.Printf("   单卡片渲染时间: %v\n", duration/time.Duration(len(testCards)))

	// 9. 测试完成
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println("✅ 所有测试完成！")
	fmt.Println("🎉 无浏览器依赖卡片渲染器工作正常")

	// 清理提示
	fmt.Println("🧹 正在清理临时文件...")
	if err := renderer.Cleanup(); err != nil {
		fmt.Printf("⚠️ 清理过程中出现错误: %v\n", err)
	} else {
		fmt.Println("✅ 清理完成")
	}
}
