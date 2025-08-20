package main

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/book"
)

func main() {
	fmt.Println("🚀 API调用和JSON处理修复验证")
	fmt.Println(strings.Repeat("=", 50))

	ctx := context.Background()

	// 1. 测试API参数优化器
	fmt.Println("📊 测试API参数优化器...")
	testAPIParametersOptimizer(ctx)

	// 2. 测试增强响应处理器
	fmt.Println("\n🔄 测试增强响应处理器...")
	testEnhancedResponseProcessor(ctx)

	// 3. 测试问题场景修复
	fmt.Println("\n🚨 测试问题场景修复...")
	testProblemScenarios(ctx)

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("🎉 修复验证完成")
}

func testAPIParametersOptimizer(ctx context.Context) {
	optimizer := book.NewAPIParametersOptimizer()

	// 测试不同场景的参数优化
	testCases := []struct {
		apiType     string
		inputLength int
		attempt     int
		description string
	}{
		{"ali", 5000, 1, "阿里API首次尝试"},
		{"ali", 5000, 2, "阿里API第二次重试"},
		{"ali", 5000, 3, "阿里API第三次重试"},
		{"volc", 3000, 1, "火山引擎API首次尝试"},
		{"volc", 10000, 2, "火山引擎API处理长文本"},
	}

	for _, tc := range testCases {
		maxTokens, temperature, err := optimizer.OptimizeParametersForAPI(
			ctx, tc.apiType, tc.inputLength, tc.attempt, 999)

		if err != nil {
			fmt.Printf("❌ %s: 参数优化失败 - %v\n", tc.description, err)
		} else {
			fmt.Printf("✅ %s: maxTokens=%d, temperature=%.2f\n",
				tc.description, maxTokens, temperature)
		}
	}

	// 测试响应验证
	fmt.Println("\n📋 测试响应验证:")

	validationTests := []struct {
		response    string
		description string
	}{
		{"", "空响应"},
		{"error occurred", "错误响应"},
		{`{"structured_text_array": []}`, "有效JSON响应"},
		{"这是一个正常的长文本响应，包含了足够的内容来通过基本验证，但没有JSON结构", "纯文本响应"},
	}

	for _, vt := range validationTests {
		isValid, reason := optimizer.ValidateAPIResponse(ctx, vt.response, "ali", 999)
		status := "❌"
		if isValid {
			status = "✅"
		}
		fmt.Printf("%s %s: %s\n", status, vt.description, reason)
	}
}

func testEnhancedResponseProcessor(ctx context.Context) {
	processor := book.NewEnhancedResponseProcessor()

	// 测试各种响应处理场景
	testResponses := []struct {
		response    string
		description string
	}{
		{
			"",
			"空响应",
		},
		{
			`好的，这是整理后的内容：{"structured_text_array": [{"type": "title", "content": "测试标题"}]}`,
			"包含前缀的JSON响应",
		},
		{
			"```json\n{\"structured_text_array\": []}\n```",
			"Markdown代码块包装的JSON",
		},
		{
			`{"structured_text_array": [{"type": "title", "content": "测试"}`,
			"不完整的JSON（缺少闭合括号）",
		},
		{
			`错误信息：API调用失败`,
			"纯错误文本",
		},
		{
			`正常的JSON响应：{"structured_text_array": [{"type": "body", "content": "这是正文内容"}]}以上就是结果。`,
			"包含前后缀的有效JSON",
		},
	}

	for _, tr := range testResponses {
		fmt.Printf("\n🔍 处理: %s\n", tr.description)
		fmt.Printf("   输入: %s\n", truncateString(tr.response, 80))

		result, err := processor.ProcessAPIResponse(ctx, tr.response, "ali", 999, 0)

		if err != nil {
			fmt.Printf("   结果: ❌ 处理失败 - %v\n", err)
		} else {
			fmt.Printf("   结果: ✅ 处理成功\n")
			fmt.Printf("   输出: %s\n", truncateString(result, 80))
		}
	}
}

func testProblemScenarios(ctx context.Context) {
	fmt.Println("🚨 模拟日志中出现的问题场景:")

	// 模拟原始日志中的问题场景
	problemScenarios := []struct {
		name        string
		response    string
		expectFix   bool
		description string
	}{
		{
			"空响应场景",
			"",
			false,
			"API返回response_length=0的情况",
		},
		{
			"JSON截断场景",
			`{"structured_text_array": [{"type": "title", "content": "标题"}, {"type": "body", "content": "内容"},`,
			true,
			"JSON被截断，缺少闭合括号",
		},
		{
			"包含错误关键词",
			"Error: Rate limit exceeded",
			false,
			"响应包含错误信息",
		},
		{
			"修复后有效JSON",
			`好的，根据您的要求整理如下：
{
  "structured_text_array": [
    {"type": "title", "content": "修复测试"},
    {"type": "body", "content": "这是修复后的内容"}
  ]
}
希望对您有帮助。`,
			true,
			"包含前后缀但核心JSON有效",
		},
	}

	processor := book.NewEnhancedResponseProcessor()
	optimizer := book.NewAPIParametersOptimizer()

	for _, scenario := range problemScenarios {
		fmt.Printf("\n🎯 场景: %s\n", scenario.name)
		fmt.Printf("   描述: %s\n", scenario.description)
		fmt.Printf("   原始: %s\n", truncateString(scenario.response, 100))

		// 先验证响应
		isValid, reason := optimizer.ValidateAPIResponse(ctx, scenario.response, "ali", 999)
		fmt.Printf("   验证: %s (%s)\n", boolToEmoji(isValid), reason)

		// 然后尝试处理
		if isValid || scenario.expectFix {
			result, err := processor.ProcessAPIResponse(ctx, scenario.response, "ali", 999, 0)

			if err != nil {
				fmt.Printf("   处理: ❌ %v\n", err)
			} else {
				fmt.Printf("   处理: ✅ 成功\n")
				fmt.Printf("   结果: %s\n", truncateString(result, 100))
			}
		} else {
			fmt.Printf("   处理: ⏭️  跳过（响应无效）\n")
		}
	}

	// 测试重试延迟计算
	fmt.Println("\n⏱️ 测试重试延迟策略:")
	for attempt := 1; attempt <= 5; attempt++ {
		aliDelay := optimizer.GetRecommendedRetryDelay(attempt, "ali")
		volcDelay := optimizer.GetRecommendedRetryDelay(attempt, "volc")
		fmt.Printf("   第%d次重试: 阿里=%ds, 火山引擎=%ds\n", attempt, aliDelay, volcDelay)
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func boolToEmoji(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}
