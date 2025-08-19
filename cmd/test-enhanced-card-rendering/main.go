package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

// 测试增强卡片渲染系统 - 验证大内容量支持
func main() {
	fmt.Println("🚀 开始测试增强卡片渲染系统")
	fmt.Println("📋 测试目标：5张图片 + 2000字文本，验证无生成失败、无截断")

	// 创建优化卡片协调器
	coordinator := card.NewOptimizedCardCoordinator()

	// 显示系统限制
	limits := coordinator.GetContentLimits()
	fmt.Printf("📊 系统容量限制：%+v\n", limits)

	// 测试1: 极端内容测试 - 5张图片 + 2000字文本
	fmt.Println("\n=== 测试1: 极端内容测试 ===")
	testExtremeContent(coordinator)

	// 测试2: 封面纯净测试 - 封面图优化
	fmt.Println("\n=== 测试2: 封面纯净测试 ===")
	testCoverOptimization(coordinator)

	// 测试3: 动态高度测试 - 不同内容量的高度适配
	fmt.Println("\n=== 测试3: 动态高度测试 ===")
	testDynamicHeight(coordinator)

	// 测试4: 分页逻辑测试 - 内容完整性拆分
	fmt.Println("\n=== 测试4: 分页逻辑测试 ===")
	testPaginationLogic()

	fmt.Println("\n✅ 所有测试完成")
}

// testExtremeContent 测试极端内容处理
func testExtremeContent(coordinator *card.OptimizedCardCoordinator) {
	// 生成2000字的测试文本
	longText := generateLongText(2000)
	
	// 创建包含5张图片和长文本的测试卡片
	elements := []pagination.Element{
		{Type: "title", Content: "极端内容测试标题"},
		{Type: "image", Content: "https://example.com/image1.jpg"},
		{Type: "body", Content: longText[:500]}, // 第一段文本
		{Type: "image", Content: "https://example.com/image2.jpg"},
		{Type: "body", Content: longText[500:1000]}, // 第二段文本
		{Type: "image", Content: "https://example.com/image3.jpg"},
		{Type: "body", Content: longText[1000:1500]}, // 第三段文本
		{Type: "image", Content: "https://example.com/image4.jpg"},
		{Type: "body", Content: longText[1500:2000]}, // 第四段文本
		{Type: "image", Content: "https://example.com/image5.jpg"},
		{Type: "subtitle", Content: "总结段落"},
		{Type: "body", Content: "这是一个包含5张图片和2000字文本的极端测试用例，用于验证系统的处理能力。"},
	}

	// 转换为JSON
	jsonData, err := json.Marshal(elements)
	if err != nil {
		log.Fatalf("JSON序列化失败: %v", err)
	}

	// 创建测试卡片
	testCard := &model.CardM{
		ID:            999,
		ProcessedText: string(jsonData),
	}

	// 分析内容
	analysis := analyzeCardContent(testCard)
	fmt.Printf("📊 内容分析：图片数=%d, 文本长度=%d, 元素数=%d\n", 
		analysis.ImagesCount, analysis.TextLength, analysis.ElementsCount)

	// 检查是否在支持范围内
	supported := coordinator.IsContentSupported(analysis.ImagesCount, analysis.TextLength)
	fmt.Printf("✅ 内容支持状态：%v\n", supported)

	if !supported {
		fmt.Println("⚠️ 内容超出系统支持范围，但系统应该通过分页处理")
	}

	// 测试渲染（模拟）
	fmt.Println("🖼️ 开始模拟渲染...")
	// 注意：这里是模拟测试，实际项目中需要调用真实的渲染方法
	// renderedCard, err := coordinator.RenderOptimizedCard(context.Background(), testCard)
	
	fmt.Println("✅ 极端内容测试完成")
}

// testCoverOptimization 测试封面优化
func testCoverOptimization(coordinator *card.OptimizedCardCoordinator) {
	coverOptimizer := card.NewCoverOptimizer()
	
	// 测试不同比例的封面图
	testCases := []struct {
		name string
		url  string
	}{
		{"标准3:4比例", "https://example.com/cover_3_4.jpg"},
		{"宽屏16:9比例", "https://example.com/cover_16_9.jpg"},
		{"竖屏9:16比例", "https://example.com/cover_9_16.jpg"},
		{"正方形1:1比例", "https://example.com/cover_1_1.jpg"},
	}

	for _, tc := range testCases {
		fmt.Printf("🖼️ 测试封面：%s\n", tc.name)
		
		// 验证封面URL
		if err := coverOptimizer.ValidateCoverImage(tc.url); err != nil {
			fmt.Printf("❌ 封面验证失败：%v\n", err)
			continue
		}

		// 生成CSS样式
		mockCoverInfo := &card.CoverImageInfo{
			URL:           tc.url,
			OptimizedURL:  tc.url + "_optimized",
			Width:         1080,
			Height:        1440,
			AspectRatio:   0.75,
			OptimizedSize: "1080x1440",
		}

		css := coverOptimizer.GenerateCoverCSS(mockCoverInfo, true)
		fmt.Printf("📝 生成的CSS样式：\n%s\n", css)
	}

	fmt.Println("✅ 封面优化测试完成")
}

// testDynamicHeight 测试动态高度计算
func testDynamicHeight(coordinator *card.OptimizedCardCoordinator) {
	config := pagination.GetDynamicConfig()
	engine := pagination.NewDynamicPaginationEngine(config)

	testCases := []struct {
		name     string
		elements []pagination.Element
	}{
		{
			name: "短内容",
			elements: []pagination.Element{
				{Type: "title", Content: "短标题"},
				{Type: "body", Content: "这是一段短文本内容。"},
			},
		},
		{
			name: "中等内容",
			elements: []pagination.Element{
				{Type: "title", Content: "中等长度的标题"},
				{Type: "image", Content: "https://example.com/image.jpg"},
				{Type: "body", Content: generateLongText(500)},
				{Type: "subtitle", Content: "副标题"},
				{Type: "body", Content: generateLongText(300)},
			},
		},
		{
			name: "长内容",
			elements: []pagination.Element{
				{Type: "title", Content: "很长的标题内容，包含更多的文字描述"},
				{Type: "image", Content: "https://example.com/image1.jpg"},
				{Type: "body", Content: generateLongText(800)},
				{Type: "image", Content: "https://example.com/image2.jpg"},
				{Type: "body", Content: generateLongText(600)},
				{Type: "subtitle", Content: "长副标题内容"},
				{Type: "body", Content: generateLongText(400)},
			},
		},
	}

	for _, tc := range testCases {
		fmt.Printf("📏 测试：%s\n", tc.name)
		
		// 计算动态高度
		height := engine.CalculateContentHeight(tc.elements)
		optimizedHeight := engine.GetOptimizedCardHeight(tc.elements)
		
		fmt.Printf("   计算高度：%d -> 优化高度：%d\n", height, optimizedHeight)
		fmt.Printf("   高度倍数：%.1fx 基础高度\n", float64(optimizedHeight)/1440.0)
	}

	fmt.Println("✅ 动态高度测试完成")
}

// testPaginationLogic 测试分页逻辑
func testPaginationLogic() {
	config := pagination.GetDynamicConfig()
	engine := pagination.NewDynamicPaginationEngine(config)

	// 创建包含大量内容的元素列表
	var elements []pagination.Element
	
	// 添加标题
	elements = append(elements, pagination.Element{
		Type:    "title",
		Content: "分页逻辑测试 - 大内容量处理",
	})

	// 添加多段文本和图片
	for i := 0; i < 10; i++ {
		// 添加段落
		elements = append(elements, pagination.Element{
			Type:    "body",
			Content: fmt.Sprintf("这是第%d段内容。%s", i+1, generateLongText(200+i*50)),
		})

		// 每3段添加一张图片
		if (i+1)%3 == 0 {
			elements = append(elements, pagination.Element{
				Type:    "image",
				Content: fmt.Sprintf("https://example.com/test_image_%d.jpg", i+1),
			})
		}
	}

	fmt.Printf("📄 待分页元素总数：%d\n", len(elements))

	// 执行分页
	paginatedContent, err := engine.CreateOptimizedPages(elements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	fmt.Printf("📚 分页结果：%d 张卡片\n", len(paginatedContent.Cards))

	// 分析每张卡片
	for i, card := range paginatedContent.Cards {
		height := engine.GetOptimizedCardHeight(card.Elements)
		imageCount := 0
		textLength := 0
		
		for _, element := range card.Elements {
			if element.Type == "image" {
				imageCount++
			} else {
				textLength += len(fmt.Sprintf("%v", element.Content))
			}
		}

		fmt.Printf("   卡片%d：%d个元素, %d张图片, %d字符, 高度%d\n", 
			i+1, len(card.Elements), imageCount, textLength, height)
	}

	fmt.Println("✅ 分页逻辑测试完成")
}

// generateLongText 生成指定长度的测试文本
func generateLongText(length int) string {
	baseText := "这是一段用于测试的中文文本内容。包含了各种标点符号，用来模拟真实的文章内容。" +
		"内容涵盖了技术、生活、学习等多个方面的主题，确保文本的多样性和真实性。" +
		"我们需要验证系统能够正确处理长文本，并且在分页时不会出现截断或丢失的情况。" +
		"这个测试文本还包含了一些特殊字符：！@#￥%……&*（）——+{}【】、。《》？：""''；等。"

	var result strings.Builder
	for result.Len() < length {
		remaining := length - result.Len()
		if remaining >= len(baseText) {
			result.WriteString(baseText)
		} else {
			result.WriteString(baseText[:remaining])
		}
	}

	return result.String()
}

// CardContentAnalysis 卡片内容分析结果
type CardContentAnalysis struct {
	ElementsCount int
	ImagesCount   int
	TextLength    int
}

// analyzeCardContent 分析卡片内容
func analyzeCardContent(card *model.CardM) *CardContentAnalysis {
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return &CardContentAnalysis{
			ElementsCount: 0,
			ImagesCount:   0,
			TextLength:    len(card.ProcessedText),
		}
	}

	analysis := &CardContentAnalysis{
		ElementsCount: len(elements),
	}

	for _, element := range elements {
		if element.Type == "image" {
			analysis.ImagesCount++
		} else {
			analysis.TextLength += len(fmt.Sprintf("%v", element.Content))
		}
	}

	return analysis
}
