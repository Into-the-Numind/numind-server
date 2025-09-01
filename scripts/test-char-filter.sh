#!/bin/bash

# 字符过滤测试脚本
# 验证字符过滤逻辑是否正确

echo "=== 字符过滤测试 ==="

cat > test_char_filter.go << 'EOF'
package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Println("=== 字符过滤测试 ===")

	// 测试字符串，包含各种字符
	testStr := "Hello世界!@#$%^&*()_+{}|:<>?[]\\;'\",./<>?{}|"
	
	fmt.Printf("测试字符串: %s\n", testStr)
	fmt.Printf("字符串长度: %d 字符\n", len(testStr))
	
	// 测试字符过滤
	var result strings.Builder
	removedCount := 0
	
	for i, char := range testStr {
		fmt.Printf("位置 %d: '%c' (0x%02x, %d)\n", i, char, char, char)
		
		// 检查是否是扩展ASCII字符（128-255）
		if char >= 128 && char <= 255 {
			fmt.Printf("  -> 移除扩展ASCII字符: 0x%02x\n", char)
			removedCount++
			continue
		}
		
		// 保留字符
		result.WriteRune(char)
	}
	
	cleaned := result.String()
	fmt.Printf("\n过滤结果:\n")
	fmt.Printf("原始长度: %d\n", len(testStr))
	fmt.Printf("清理后长度: %d\n", len(cleaned))
	fmt.Printf("移除字符数: %d\n", removedCount)
	fmt.Printf("清理后的字符串: %s\n", cleaned)
	
	// 测试具体的扩展ASCII字符
	fmt.Printf("\n=== 扩展ASCII字符测试 ===\n")
	extendedChars := []rune{0x80, 0x8D, 0x86, 0x9A, 0x84, 0xE5, 0x88, 0x86, 0xE7, 0x9A, 0x84, 0x84, 0xE8, 0xAF, 0xB7, 0xE6, 0xB1, 0x82, 0xE8, 0x83, 0xBD, 0xE6, 0xB8, 0xA9, 0xE5, 0x92, 0x8C, 0xE8, 0x80, 0x8C, 0xE5, 0x9A, 0x9A, 0xE5, 0xAE, 0x9A, 0xE5, 0x9C, 0xB0, 0xE8, 0xAF, 0xB4}
	
	for i, char := range extendedChars {
		fmt.Printf("字符 %d: 0x%02x (%d) - ", i, char, char)
		if char >= 128 && char <= 255 {
			fmt.Printf("扩展ASCII - 应该移除\n")
		} else {
			fmt.Printf("非扩展ASCII - 应该保留\n")
		}
	}
}
EOF

echo "运行字符过滤测试..."
go run test_char_filter.go

echo "清理测试文件..."
rm -f test_char_filter.go

echo "=== 测试完成 ==="
