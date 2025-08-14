package main

import (
	"fmt"
	"log"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

func main() {
	fmt.Println("🚀 渲染-测量方案示例")
	fmt.Println("==================")

	// 1. 创建分页配置
	config := pagination.GetDefaultConfig()
	fmt.Printf("📏 分页配置: 卡片尺寸 %dx%d, 边距 上%d下%d左%d右%d\n",
		config.Card.Width, config.Card.Height,
		config.Card.Padding.Top, config.Card.Padding.Bottom,
		config.Card.Padding.Left, config.Card.Padding.Right)

	// 2. 创建示例书籍数据
	book := &model.BookM{
		Title:     "示例书籍：渲染-测量方案演示",
		ImageUrl:  "https://example.com/cover.jpg",
		CardCount: 5,
	}

	// 3. 创建示例卡片数据
	cards := createSampleCards()

	// 4. 使用渲染-测量渲染器
	fmt.Println("\n🔧 使用渲染-测量渲染器...")
	renderer := card.NewRenderAndMeasureRenderer(config)

	renderedCards, err := renderer.RenderBookToImages(book, cards)
	if err != nil {
		log.Fatalf("渲染失败: %v", err)
	}

	fmt.Printf("✅ 渲染完成！生成了 %d 张图片\n", len(renderedCards))

	// 5. 显示渲染结果
	for i, renderedCard := range renderedCards {
		fmt.Printf("📄 页面 %d: 卡片ID=%d, 图片URL=%s, 尺寸=%dx%d\n",
			i+1, renderedCard.CardID, renderedCard.ImageURL,
			renderedCard.Width, renderedCard.Height)
	}

	// 6. 使用Chrome无头浏览器渲染器（可选）
	fmt.Println("\n🌐 使用Chrome无头浏览器渲染器...")
	chromeRenderer := card.NewChromeHeadlessRenderer(config)

	chromeRenderedCards, err := chromeRenderer.RenderBookToImages(book, cards)
	if err != nil {
		fmt.Printf("⚠️  Chrome渲染失败: %v\n", err)
	} else {
		fmt.Printf("✅ Chrome渲染完成！生成了 %d 张图片\n", len(chromeRenderedCards))
	}
}

// createSampleCards 创建示例卡片数据
func createSampleCards() []*model.CardM {
	cards := []*model.CardM{
		{
			ProcessedText: `[
				{"type": "title", "content": "第一章：渲染-测量方案介绍"},
				{"type": "body", "content": "渲染-测量方案是一种革命性的卡片渲染方法，它解决了传统分页算法中的核心问题。"},
				{"type": "subtitle", "content": "核心优势"},
				{"type": "list", "content": ["完美保真：100%准确的布局计算", "关注点分离：后端专注业务逻辑", "易于维护：样式变化不影响分页"]}
			]`,
			SortOrder: 1,
		},
		{
			ProcessedText: `[
				{"type": "title", "content": "第二章：技术实现细节"},
				{"type": "body", "content": "该方案使用无头浏览器进行渲染，通过JavaScript测量元素的实际高度和位置，确保分页的准确性。"},
				{"type": "quote", "content": "放弃预测，采用渲染后测量，这是解决布局问题的根本性方案。"},
				{"type": "body", "content": "通过Chrome DevTools Protocol，我们可以在浏览器环境中执行复杂的布局计算。"}
			]`,
			SortOrder: 2,
		},
		{
			ProcessedText: `[
				{"type": "title", "content": "第三章：性能优化"},
				{"type": "body", "content": "虽然渲染-测量方案需要启动浏览器，但通过合理的缓存策略和批量处理，可以显著提升整体性能。"},
				{"type": "subtitle", "content": "优化策略"},
				{"type": "list", "content": ["批量渲染：一次处理多张卡片", "资源缓存：复用浏览器实例", "异步处理：不阻塞用户操作"]}
			]`,
			SortOrder: 3,
		},
		{
			ProcessedText: `[
				{"type": "title", "content": "第四章：部署和运维"},
				{"type": "body", "content": "在生产环境中部署Chrome无头浏览器需要考虑资源管理、进程监控和错误恢复等关键因素。"},
				{"type": "subtitle", "content": "部署要点"},
				{"type": "list", "content": ["批量渲染：一次处理多张卡片", "资源缓存：复用浏览器实例", "自动重启：处理崩溃和异常"]}
			]`,
			SortOrder: 4,
		},
		{
			ProcessedText: `[
				{"type": "title", "content": "第五章：总结和展望"},
				{"type": "body", "content": "渲染-测量方案代表了卡片渲染技术的新方向，它通过统一执行环境，从根本上解决了分页和布局的难题。"},
				{"type": "quote", "content": "技术创新的本质不是修补旧问题，而是用新方法重新定义问题。"},
				{"type": "body", "content": "未来，随着Web技术的不断发展，这种基于浏览器的渲染方案将变得更加成熟和高效。"}
			]`,
			SortOrder: 5,
		},
	}

	return cards
}
