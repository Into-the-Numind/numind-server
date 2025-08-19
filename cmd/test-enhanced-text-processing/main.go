package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"numind-server/internal/numind/biz/book"
)

// 测试增强文本处理器
func main() {
	fmt.Println("🧪 增强文本处理器测试")
	fmt.Println(strings.Repeat("=", 60))

	// 创建增强文本处理器
	processor := book.NewEnhancedTextProcessor()
	
	// 显示处理器配置
	stats := processor.GetProcessorStats()
	fmt.Printf("📊 处理器配置:\n")
	for key, value := range stats {
		fmt.Printf("  - %s: %v\n", key, value)
	}
	fmt.Println()

	// 测试用例
	testCases := []struct {
		name        string
		description string
		text        string
		expectChunk bool
	}{
		{
			name:        "短文本测试",
			description: "测试小于3000字符的文本，应该单次处理",
			text:        generateShortText(),
			expectChunk: false,
		},
		{
			name:        "长文本测试",
			description: "测试超过3000字符的长文本，应该分块处理",
			text:        generateLongText(),
			expectChunk: true,
		},
		{
			name:        "魅力要素长文本",
			description: "模拟用户遇到的包含18个魅力要素的长文本",
			text:        generateCharmText(),
			expectChunk: true,
		},
		{
			name:        "极长文本测试",
			description: "测试极长文本的处理能力",
			text:        generateVeryLongText(),
			expectChunk: true,
		},
	}

	// 执行测试
	ctx := context.Background()
	
	for i, tc := range testCases {
		fmt.Printf("🧪 测试 %d: %s\n", i+1, tc.name)
		fmt.Printf("📝 描述: %s\n", tc.description)
		fmt.Printf("📏 文本长度: %d 字符\n", len(tc.text))
		
		// 模拟API调用函数
		apiCaller := createMockAPICall(tc.name)
		
		// 执行处理
		startTime := time.Now()
		result, err := processor.ProcessLongText(ctx, tc.text, apiCaller, uint(i+1))
		duration := time.Since(startTime)
		
		if err != nil {
			fmt.Printf("❌ 测试失败: %v\n", err)
		} else {
			fmt.Printf("✅ 测试成功:\n")
			fmt.Printf("  - 总耗时: %v\n", duration)
			fmt.Printf("  - 总块数: %d\n", result.MergeStats.TotalChunks)
			fmt.Printf("  - 成功块数: %d\n", result.MergeStats.SuccessChunks)
			fmt.Printf("  - 失败块数: %d\n", result.MergeStats.FailedChunks)
			fmt.Printf("  - 总重试次数: %d\n", result.MergeStats.TotalRetries)
			fmt.Printf("  - 需要合并: %v\n", result.MergeStats.RequiredMerge)
			fmt.Printf("  - 最终JSON长度: %d\n", len(result.FinalJSON))
			
			// 验证期望
			if tc.expectChunk && result.MergeStats.TotalChunks == 1 {
				fmt.Printf("⚠️ 警告: 期望分块但实际单次处理\n")
			} else if !tc.expectChunk && result.MergeStats.TotalChunks > 1 {
				fmt.Printf("⚠️ 警告: 期望单次处理但实际分块\n")
			} else {
				fmt.Printf("✅ 符合期望的处理方式\n")
			}
			
			// 显示块处理详情
			if len(result.ChunkResults) > 1 {
				fmt.Printf("  📦 块处理详情:\n")
				for j, chunkResult := range result.ChunkResults {
					fmt.Printf("    块%d: 成功=%v, 重试=%d, 耗时=%v\n", 
						j+1, chunkResult.Success, chunkResult.RetryCount, chunkResult.ProcessTime)
				}
			}
		}
		
		fmt.Println(strings.Repeat("-", 40))
		fmt.Println()
	}

	// 性能基准测试
	fmt.Println("🚀 性能基准测试")
	fmt.Println(strings.Repeat("=", 60))
	
	performanceBenchmark(processor)
	
	fmt.Println("\n🎉 所有测试完成!")
}

// generateShortText 生成短文本
func generateShortText() string {
	return `魅力的本质

魅力不是外在的装饰，而是内在的力量。真正有魅力的人，往往具备以下几个特质：

1. 自信但不自负
2. 温和但有原则  
3. 倾听但有观点
4. 幽默但不失分寸

这些特质让人在与他人交往中，既能保持自己的个性，又能给他人留下深刻印象。
魅力是一种可以培养的能力，通过不断的自我完善和实践，每个人都能散发出独特的魅力。`
}

// generateLongText 生成长文本
func generateLongText() string {
	var text strings.Builder
	
	text.WriteString("魅力的深度解析\n\n")
	
	for i := 1; i <= 15; i++ {
		text.WriteString(fmt.Sprintf("第%d个魅力要素：\n\n", i))
		text.WriteString(fmt.Sprintf("这是关于第%d个魅力要素的详细描述。", i))
		text.WriteString("每个要素都包含了深层的心理学原理和实践方法。")
		text.WriteString("通过对这些要素的深入理解，我们能够更好地提升自己的个人魅力。")
		text.WriteString("这不仅仅是表面的技巧，更是内在品质的体现。")
		text.WriteString("真正的魅力来自于对自己和他人的深度理解。")
		text.WriteString("它需要时间的积累和不断的实践。")
		text.WriteString("每一个细节都可能影响到我们给他人留下的印象。")
		text.WriteString("因此，培养魅力是一个全方位的提升过程。\n\n")
		
		// 添加一些具体的例子
		text.WriteString("实际案例：\n")
		text.WriteString(fmt.Sprintf("在第%d个要素的实践中，我们可以通过以下方式来提升：", i))
		text.WriteString("1. 观察成功人士的行为模式；")
		text.WriteString("2. 在日常交往中有意识地练习；")
		text.WriteString("3. 接受他人的反馈并持续改进；")
		text.WriteString("4. 保持学习的心态和开放的思维。\n\n")
	}
	
	text.WriteString("总结：\n")
	text.WriteString("魅力是一个综合性的概念，它涵盖了个人的各个方面。")
	text.WriteString("通过系统性的学习和实践，我们都能够显著提升自己的个人魅力。")
	text.WriteString("这不仅对个人发展有益，也会对我们的人际关系产生积极影响。")
	
	return text.String()
}

// generateCharmText 生成魅力要素文本（模拟用户实际场景）
func generateCharmText() string {
	elements := []string{
		"深度的自我接纳", "清晰的边界意识", "真诚的好奇心", "恰到好处的神秘感",
		"温暖的眼神交流", "自然的身体语言", "独特的个人风格", "丰富的内在世界",
		"优雅的沟通技巧", "适度的脆弱展现", "坚定的价值观念", "灵活的思维方式",
		"诚实的情感表达", "持续的自我成长", "包容的心态", "创造性的思考",
		"稳定的情绪管理", "积极的生活态度"
	}

	var text strings.Builder
	text.WriteString("我好像发现了魅力的本质！\n\n")
	text.WriteString("经过深入思考和观察，我发现真正的魅力包含以下18个要素：\n\n")

	for i, element := range elements {
		text.WriteString(fmt.Sprintf("第%d个要素：%s\n\n", i+1, element))
		
		// 为每个要素添加详细描述
		switch element {
		case "深度的自我接纳":
			text.WriteString("魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。比如一个人坦然承认自己内向不善社交，却能在独处时展现出专注的思考力，这种真实感比刻意的外向表演更有吸引力。自我接纳的人散发出一种安定的气场，让他人感到舒适和信任。")
		case "清晰的边界意识":
			text.WriteString("有魅力的人懂得守住自己的底线，尊重他人的空间。比如面对不合理的请求，能温和而坚定地说'这个我可能帮不了你'，既不委屈自己，也不指责对方；与人相处时，不过度打探隐私，也不轻易透露过多个人信息。这种边界感让人际关系更加健康，也让自己显得更有原则和魅力。")
		case "真诚的好奇心":
			text.WriteString("对世界和他人保持真诚的兴趣和好奇，是魅力的重要源泉。当你真心想了解对方的想法、经历或专业领域时，那种专注的倾听和恰当的提问会让对方感到被重视。这种好奇心不是为了社交而装出来的，而是发自内心的求知欲和对人性的探索。")
		default:
			text.WriteString(fmt.Sprintf("%s是构成个人魅力的重要因素。它体现在日常的言行举止中，影响着我们与他人的互动质量。通过有意识的培养和练习，每个人都能在这个方面有所提升，从而增强自己的整体魅力。这不是表面的技巧，而是内在品质的自然流露。", element))
		}
		
		text.WriteString("\n\n")
		
		// 添加实践建议
		text.WriteString("实践建议：\n")
		text.WriteString("1. 在日常生活中有意识地关注这个要素\n")
		text.WriteString("2. 观察身边有魅力的人如何体现这个特质\n") 
		text.WriteString("3. 从小事开始练习，循序渐进地提升\n")
		text.WriteString("4. 定期反思和调整自己的行为模式\n\n")
	}

	text.WriteString("总结思考：\n\n")
	text.WriteString("魅力不是天生的，而是可以通过有意识的培养来提升的。以上18个要素相互关联，共同构成了一个人的整体魅力。")
	text.WriteString("重要的是，真正的魅力来自于内在的修养和品格，外在的技巧只是锦上添花。")
	text.WriteString("当我们专注于成为更好的自己时，魅力自然会散发出来，吸引那些与我们价值观相符的人。")
	text.WriteString("这个过程需要时间和耐心，但每一点进步都会让我们的生活变得更加丰富和有意义。")

	return text.String()
}

// generateVeryLongText 生成极长文本
func generateVeryLongText() string {
	var text strings.Builder
	
	// 重复使用长文本生成更长的内容
	baseText := generateLongText()
	
	for i := 1; i <= 3; i++ {
		text.WriteString(fmt.Sprintf("=== 第%d部分 ===\n\n", i))
		text.WriteString(baseText)
		text.WriteString("\n\n")
	}
	
	return text.String()
}

// createMockAPICall 创建模拟API调用函数
func createMockAPICall(testName string) func(context.Context, []map[string]string, int, float64, uint) (string, error) {
	return func(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
		// 模拟API处理时间
		time.Sleep(100 * time.Millisecond)
		
		// 构建模拟响应
		response := fmt.Sprintf(`{
  "structured_text_array": [
    {
      "type": "title",
      "content": "模拟处理结果 - %s"
    },
    {
      "type": "body",
      "content": "这是模拟的API响应内容。实际处理中，这里会包含真实的文本处理结果。当前配置: maxTokens=%d, temperature=%.1f"
    },
    {
      "type": "list",
      "content": ["要点1", "要点2", "要点3"]
    }
  ],
  "image_prompt": "基于内容生成的模拟图片提示词"
}`, testName, maxTokens, temperature)
		
		return response, nil
	}
}

// performanceBenchmark 性能基准测试
func performanceBenchmark(processor *book.EnhancedTextProcessor) {
	ctx := context.Background()
	
	// 测试不同长度文本的处理性能
	testSizes := []struct {
		name string
		size int
	}{
		{"小文本 (1KB)", 1000},
		{"中文本 (5KB)", 5000},
		{"大文本 (10KB)", 10000},
		{"超大文本 (20KB)", 20000},
	}
	
	for _, test := range testSizes {
		fmt.Printf("📊 测试 %s:\n", test.name)
		
		// 生成指定大小的文本
		text := generateTextOfSize(test.size)
		
		// 模拟API调用
		apiCaller := func(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
			time.Sleep(50 * time.Millisecond) // 模拟网络延迟
			return `{"structured_text_array": [{"type": "body", "content": "测试内容"}], "image_prompt": "测试"}`, nil
		}
		
		// 执行性能测试
		startTime := time.Now()
		result, err := processor.ProcessLongText(ctx, text, apiCaller, 999)
		duration := time.Since(startTime)
		
		if err != nil {
			fmt.Printf("  ❌ 失败: %v\n", err)
		} else {
			fmt.Printf("  ⏱️ 耗时: %v\n", duration)
			fmt.Printf("  📦 块数: %d\n", result.MergeStats.TotalChunks)
			fmt.Printf("  🔄 重试: %d\n", result.MergeStats.TotalRetries)
			fmt.Printf("  💾 效率: %.2f KB/s\n", float64(test.size)/duration.Seconds()/1024)
		}
		
		fmt.Println()
	}
}

// generateTextOfSize 生成指定大小的文本
func generateTextOfSize(targetSize int) string {
	baseText := "这是一段用于性能测试的文本内容。它会被重复多次以达到目标大小。"
	
	var text strings.Builder
	for text.Len() < targetSize {
		text.WriteString(baseText)
		text.WriteString(" ")
	}
	
	result := text.String()
	if len(result) > targetSize {
		result = result[:targetSize]
	}
	
	return result
}
