package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"numind-server/internal/pkg/httpclient"
)

// 测试JSON修复引擎
func main() {
	fmt.Println("🧪 JSON修复引擎测试")
	fmt.Println(strings.Repeat("=", 50))

	// 创建JSON修复引擎
	engine := httpclient.NewJSONRepairEngine()
	extractor := httpclient.NewAdvancedJSONExtractor()

	// 测试用例
	testCases := []struct {
		name        string
		input       string
		expectValid bool
		description string
	}{
		{
			name:        "截断的structured_text_array",
			description: "模拟用户遇到的实际问题",
			expectValid: true,
			input: `{
  "structured_text_array": [
    {
      "type": "title",
      "content": "我好像发现了魅力的本质!"
    },
    {
      "type": "subtitle", 
      "content": "深度的自我接纳"
    },
    {
      "type": "body",
      "content": "魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。比如一个人坦然承认自己内向不善社交，却能在独处时展现出专注的思考力。"
    },
    {
      "type": "subtitle",
      "content": "清晰的边界意识"
    },
    {
      "type": "body",
      "content": "有魅力的人懂得守住自己的底线，尊重他人的空间。比如面对不合理的请求，能温和而坚定地说这个我可能帮不了你，既不委屈自己，也不指责对方；与人相处时，不过度打探隐私，也不` + "\x9c\x9f",
		},
		{
			name:        "UTF-8编码截断",
			description: "包含UTF-8编码问题的JSON",
			expectValid: true,
			input: `{
  "structured_text_array": [
    {
      "type": "title",
      "content": "测试标题"
    },
    {
      "type": "body", 
      "content": "正常内容，但是后面有编码问` + "\xFF\xFE" + `题"
    }
  ]
}`,
		},
		{
			name:        "不完整的JSON对象",
			description: "缺少结尾括号的JSON",
			expectValid: true,
			input: `{
  "structured_text_array": [
    {
      "type": "title",
      "content": "不完整的JSON"
    },
    {
      "type": "body",
      "content": "这个JSON没有正确结尾"
    }
  `,
		},
		{
			name:        "字符串截断",
			description: "在字符串中间截断的JSON",
			expectValid: true,
			input: `{
  "structured_text_array": [
    {
      "type": "title",
      "content": "字符串截断测试"
    },
    {
      "type": "body",
      "content": "这个字符串在中间被截`,
		},
		{
			name:        "复杂嵌套结构",
			description: "包含列表的复杂结构",
			expectValid: true,
			input: `{
  "structured_text_array": [
    {
      "type": "title",
      "content": "复杂结构测试"
    },
    {
      "type": "list",
      "content": [
        "第一项",
        "第二项",
        "第三项但是被截断了`,
		},
	}

	// 执行测试
	passedTests := 0
	totalTests := len(testCases)

	for i, tc := range testCases {
		fmt.Printf("\n🧪 测试 %d: %s\n", i+1, tc.name)
		fmt.Printf("📝 描述: %s\n", tc.description)
		fmt.Printf("📏 输入长度: %d 字符\n", len(tc.input))

		// 显示输入的末尾部分
		endPreview := tc.input
		if len(endPreview) > 100 {
			endPreview = "..." + endPreview[len(endPreview)-100:]
		}
		fmt.Printf("📄 输入末尾: %q\n", endPreview)

		// 测试引擎修复
		repaired, err := engine.RepairTruncatedJSON(tc.input)
		if err != nil {
			fmt.Printf("❌ 引擎修复失败: %v\n", err)
			continue
		}

		fmt.Printf("🔧 修复后长度: %d 字符\n", len(repaired))

		// 测试提取器
		extracted, err := extractor.ExtractValidJSON([]byte(tc.input))
		if err != nil {
			fmt.Printf("❌ 提取器失败: %v\n", err)
			continue
		}

		fmt.Printf("📤 提取后长度: %d 字符\n", len(extracted))

		// 验证结果
		if tc.expectValid {
			fmt.Printf("✅ 测试 %d 通过: 成功修复和提取JSON\n", i+1)
			passedTests++
		} else {
			fmt.Printf("❌ 测试 %d 失败: 期望无效但却成功了\n", i+1)
		}

		// 显示修复后的结构
		if len(extracted) < 500 {
			fmt.Printf("📋 修复结果: %s\n", string(extracted))
		} else {
			fmt.Printf("📋 修复结果(前200字符): %s...\n", string(extracted[:200]))
		}
	}

	// 测试结果总结
	fmt.Printf("\n%s\n", strings.Repeat("=", 50))
	fmt.Printf("🎯 测试总结: %d/%d 测试通过\n", passedTests, totalTests)
	
	if passedTests == totalTests {
		fmt.Println("🎉 所有测试通过！JSON修复引擎工作正常")
	} else {
		log.Printf("⚠️ 有 %d 个测试失败", totalTests-passedTests)
	}

	// 性能测试
	fmt.Printf("\n🚀 性能测试\n")
	performanceTest(engine, extractor)
}

// performanceTest 性能测试
func performanceTest(engine *httpclient.JSONRepairEngine, extractor *httpclient.AdvancedJSONExtractor) {
	// 生成大量数据进行性能测试
	largeJSON := generateLargeJSON(1000) // 1000个元素
	
	// 截断JSON来模拟问题
	truncatedJSON := largeJSON[:len(largeJSON)-500] // 删除最后500个字符
	
	fmt.Printf("📊 大数据测试: 原始长度=%d, 截断后长度=%d\n", len(largeJSON), len(truncatedJSON))
	
	// 性能测试
	start := time.Now()
	repaired, err := engine.RepairTruncatedJSON(truncatedJSON)
	repairTime := time.Since(start)
	
	if err != nil {
		fmt.Printf("❌ 大数据修复失败: %v\n", err)
		return
	}
	
	start = time.Now()
	extracted, err := extractor.ExtractValidJSON([]byte(truncatedJSON))
	extractTime := time.Since(start)
	
	if err != nil {
		fmt.Printf("❌ 大数据提取失败: %v\n", err)
		return
	}
	
	fmt.Printf("⏱️ 修复耗时: %v\n", repairTime)
	fmt.Printf("⏱️ 提取耗时: %v\n", extractTime)
	fmt.Printf("📏 修复后长度: %d\n", len(repaired))
	fmt.Printf("📏 提取后长度: %d\n", len(extracted))
	fmt.Printf("✅ 大数据性能测试完成\n")
}

// generateLargeJSON 生成大型JSON用于测试
func generateLargeJSON(elementCount int) string {
	var builder strings.Builder
	
	builder.WriteString(`{"structured_text_array": [`)
	
	for i := 0; i < elementCount; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		
		elementType := "body"
		if i%10 == 0 {
			elementType = "title"
		} else if i%5 == 0 {
			elementType = "subtitle"
		}
		
		builder.WriteString(fmt.Sprintf(`{
      "type": "%s",
      "content": "这是第 %d 个测试元素的内容。这个内容包含了足够的文字来测试系统的处理能力。我们需要确保即使在大量数据的情况下，JSON修复引擎也能正常工作。"
    }`, elementType, i+1))
	}
	
	builder.WriteString(`]}`)
	return builder.String()
}
