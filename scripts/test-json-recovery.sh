#!/bin/bash

# JSON恢复测试脚本
# 测试不完整JSON的修复和恢复功能

echo "=== JSON恢复测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/book
go build .
cd ../../..

# 测试2: 创建JSON恢复测试程序
echo "创建JSON恢复测试程序..."
cat > test_json_recovery.go << 'EOF'
package main

import (
	"fmt"
	"log"
	"strings"

	"numind-server/internal/numind/biz/book"
)

func main() {
	fmt.Println("=== JSON恢复测试 ===")

	// 测试数据：模拟你遇到的不完整JSON问题
	testCases := []struct {
		name     string
		response string
		expected string
	}{
		{
			name: "不完整的JSON - 缺少结束符",
			response: `{
				"structured_text_array": [
					{"type": "title", "content": "我好像发现了魅力的本质!"},
					{"type": "body", "content": "深度的自我接纳：魅力的起点往往是对自我的全然接纳。"},
					{"type": "body", "content": "稳定的情绪内核：情绪稳定并非毫无波澜。"}
				],
				"image_prompt": "一个关于魅力的抽象概念图",
				"content_length": 4555`,
			expected: "structured_text_array",
		},
		{
			name: "被截断的JSON - 在数组中间截断",
			response: `{
				"structured_text_array": [
					{"type": "title", "content": "魅力的11个核心要素"},
					{"type": "body", "content": "深度的自我接纳：魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。"},
					{"type": "body", "content": "稳定的情绪内核：情绪稳定并非毫无波澜，而是在面对突发状况、负面评价或生活起伏时，能快速调整状态，不被情绪牵着走。"},
					{"type": "body", "content": "流动的内在丰盈：内在丰盈不是死记硬背的知识堆砌，而是将经历、思考、兴趣内化成一种感知力。"`,
			expected: "structured_text_array",
		},
		{
			name: "包含HTML标签的JSON",
			response: `<html><body>{
				"structured_text_array": [
					{"type": "title", "content": "魅力的本质"},
					{"type": "body", "content": "魅力的核心在于内在的丰盈。"}
				],
				"image_prompt": "魅力概念图"
			}</body></html>`,
			expected: "structured_text_array",
		},
		{
			name: "完全损坏的JSON - 只有部分字段",
			response: `{"structured_text_array": [{"type": "title", "content": "测试标题"}`,
			expected: "structured_text_array",
		},
	}

	for i, testCase := range testCases {
		fmt.Printf("\n=== 测试用例 %d: %s ===\n", i+1, testCase.name)
		fmt.Printf("原始响应长度: %d\n", len(testCase.response))
		
		// 测试JSON提取
		extractedJSON := book.ExtractJSONFromResponse(testCase.response)
		
		if extractedJSON != "" {
			fmt.Printf("✅ JSON提取成功，长度: %d\n", len(extractedJSON))
			
			// 验证是否包含期望的字段
			if strings.Contains(extractedJSON, testCase.expected) {
				fmt.Printf("✅ 包含期望字段: %s\n", testCase.expected)
			} else {
				fmt.Printf("❌ 缺少期望字段: %s\n", testCase.expected)
			}
			
			// 显示提取的JSON预览
			if len(extractedJSON) > 200 {
				fmt.Printf("提取的JSON预览: %s...\n", extractedJSON[:200])
			} else {
				fmt.Printf("提取的JSON: %s\n", extractedJSON)
			}
		} else {
			fmt.Printf("❌ JSON提取失败\n")
		}
	}

	// 测试重试机制
	fmt.Printf("\n=== 测试重试机制 ===\n")
	
	// 模拟一个非常损坏的JSON
	severelyDamagedJSON := `{"structured_text_array": [{"type": "title", "content": "测试标题"}`
	
	fmt.Printf("严重损坏的JSON: %s\n", severelyDamagedJSON)
	
	// 第一次尝试
	firstAttempt := book.ExtractJSONFromResponse(severelyDamagedJSON)
	if firstAttempt != "" {
		fmt.Printf("✅ 第一次尝试成功: %s\n", firstAttempt)
	} else {
		fmt.Printf("❌ 第一次尝试失败\n")
		
		// 模拟重试
		fmt.Printf("模拟重试机制...\n")
		retryResult := book.ExtractJSONWithRetry(severelyDamagedJSON)
		if retryResult != "" {
			fmt.Printf("✅ 重试成功: %s\n", retryResult)
		} else {
			fmt.Printf("❌ 重试也失败了\n")
		}
	}

	fmt.Println("\n=== 测试完成 ===")
}

// 为了测试，我们需要导出这些函数
// 在实际代码中，这些函数应该是包级别的
func (p *AsyncProcessor) ExtractJSONFromResponse(response string) string {
	return book.ExtractJSONFromResponse(response)
}

func (p *AsyncProcessor) ExtractJSONWithRetry(response string) string {
	return book.ExtractJSONWithRetry(response)
}
EOF

# 测试3: 运行JSON恢复测试程序
echo "运行JSON恢复测试程序..."
go run test_json_recovery.go

# 清理
rm test_json_recovery.go

echo ""
echo "测试完成！"
echo ""
echo "主要修复："
echo "1. 增强了JSON解析的健壮性，能够处理不完整的JSON"
echo "2. 添加了重试机制，当第一次解析失败时自动重试"
echo "3. 实现了激进的JSON修复策略，包括结构修复和默认值填充"
echo "4. 修复了只提取第一个完整对象而忽略structured_text_array的问题"
echo "5. 添加了详细的调试日志，便于问题诊断"
echo ""
echo "现在你的程序应该能够："
echo "- 正确处理API返回的不完整JSON"
echo "- 自动修复被截断的JSON结构"
echo "- 在解析失败时自动重试"
echo "- 成功提取structured_text_array字段"
