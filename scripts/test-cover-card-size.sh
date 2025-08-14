#!/bin/bash

# 封面卡片尺寸测试脚本
# 专门测试封面卡片的实际渲染尺寸

echo "=== 封面卡片尺寸测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/card
go build .
cd ../..

# 测试2: 创建封面尺寸测试程序
echo "创建封面尺寸测试程序..."
cat > test_cover_card_size.go << 'EOF'
package main

import (
	"fmt"
	"os"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
)

func main() {
	fmt.Println("=== 封面卡片尺寸测试 ===")

	// 获取默认配置
	config := pagination.GetDefaultConfig()
	
	fmt.Printf("默认配置 - 卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)
	fmt.Printf("默认配置 - 内边距: 上%dpx 右%dpx 下%dpx 左%dpx\n", 
		config.Card.Padding.Top, config.Card.Padding.Right, 
		config.Card.Padding.Bottom, config.Card.Padding.Left)

	// 测试封面渲染器
	fmt.Println("\n=== 测试封面渲染器 ===")
	coverRenderer := card.NewCoverRenderer(config)
	coverConfig := card.GetCoverConfig()
	fmt.Printf("封面配置 - 卡片尺寸: %dx%d\n", coverConfig.Card.Width, coverConfig.Card.Height)
	
	// 验证封面尺寸是否与默认配置一致
	if coverConfig.Card.Width == config.Card.Width && coverConfig.Card.Height == config.Card.Height {
		fmt.Println("✅ 封面渲染器尺寸与默认配置一致")
	} else {
		fmt.Printf("❌ 封面渲染器尺寸与默认配置不一致: 封面=%dx%d, 默认=%dx%d\n", 
			coverConfig.Card.Width, coverConfig.Card.Height, config.Card.Width, config.Card.Height)
	}

	// 测试封面HTML生成
	fmt.Println("\n=== 测试封面HTML生成 ===")
	
	// 创建测试封面数据
	coverData := card.CoverCardData{
		Title:      "魅力的本质",
		Background: "/Users/neozhang/go/src/github.com/Into-the-NumPath/numind-server/res/template/t3.png",
	}
	
	// 生成HTML内容
	htmlContent := coverRenderer.GenerateCoverHTML(coverData, coverConfig)
	
	// 保存HTML文件用于检查
	debugFile := "debug_cover_test.html"
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		fmt.Printf("❌ 保存HTML文件失败: %v\n", err)
	} else {
		fmt.Printf("✅ HTML文件已保存到: %s\n", debugFile)
		
		// 检查HTML中的尺寸设置
		fmt.Println("\n=== 检查HTML中的尺寸设置 ===")
		
		// 检查html标签的尺寸
		if contains(htmlContent, fmt.Sprintf("width: %dpx", coverConfig.Card.Width)) {
			fmt.Printf("✅ HTML中html标签宽度设置正确: %dpx\n", coverConfig.Card.Width)
		} else {
			fmt.Printf("❌ HTML中html标签宽度设置不正确，期望: %dpx\n", coverConfig.Card.Width)
		}
		
		if contains(htmlContent, fmt.Sprintf("height: %dpx", coverConfig.Card.Height)) {
			fmt.Printf("✅ HTML中html标签高度设置正确: %dpx\n", coverConfig.Card.Height)
		} else {
			fmt.Printf("❌ HTML中html标签高度设置不正确，期望: %dpx\n", coverConfig.Card.Height)
		}
		
		// 检查body标签的尺寸
		if contains(htmlContent, fmt.Sprintf("width: %dpx", coverConfig.Card.Width)) {
			fmt.Printf("✅ HTML中body标签宽度设置正确: %dpx\n", coverConfig.Card.Width)
		} else {
			fmt.Printf("❌ HTML中body标签宽度设置不正确，期望: %dpx\n", coverConfig.Card.Width)
		}
		
		if contains(htmlContent, fmt.Sprintf("height: %dpx", coverConfig.Card.Height)) {
			fmt.Printf("✅ HTML中body标签高度设置正确: %dpx\n", coverConfig.Card.Height)
		} else {
			fmt.Printf("❌ HTML中body标签高度设置不正确，期望: %dpx\n", coverConfig.Card.Height)
		}
		
		// 检查CSS中的尺寸设置
		fmt.Println("\n=== 检查CSS中的尺寸设置 ===")
		
		// 检查cover-container的尺寸
		if contains(htmlContent, ".cover-container {") {
			fmt.Println("✅ CSS中cover-container样式存在")
		} else {
			fmt.Println("❌ CSS中cover-container样式缺失")
		}
		
		// 检查image-section的尺寸
		if contains(htmlContent, ".image-section {") {
			fmt.Println("✅ CSS中image-section样式存在")
		} else {
			fmt.Println("❌ CSS中image-section样式缺失")
		}
		
		// 检查title-section的尺寸
		if contains(htmlContent, ".title-section {") {
			fmt.Println("✅ CSS中title-section样式存在")
		} else {
			fmt.Println("❌ CSS中title-section样式缺失")
		}
		
		// 检查flex布局设置
		if contains(htmlContent, "display: flex") {
			fmt.Println("✅ CSS中flex布局设置正确")
		} else {
			fmt.Println("❌ CSS中flex布局设置缺失")
		}
		
		if contains(htmlContent, "flex-direction: column") {
			fmt.Println("✅ CSS中flex-direction设置正确")
		} else {
			fmt.Println("❌ CSS中flex-direction设置缺失")
		}
		
		// 检查背景设置
		fmt.Println("\n=== 检查背景设置 ===")
		if contains(htmlContent, "background: url('file://") {
			fmt.Println("✅ HTML中背景URL设置正确")
		} else {
			fmt.Println("❌ HTML中背景URL设置不正确")
		}
		
		if contains(htmlContent, "background-size: cover") {
			fmt.Println("✅ CSS中background-size设置正确")
		} else {
			fmt.Println("❌ CSS中background-size设置缺失")
		}
		
		if contains(htmlContent, "background-position: center") {
			fmt.Println("✅ CSS中background-position设置正确")
		} else {
			fmt.Println("❌ CSS中background-position设置缺失")
		}
	}

	// 验证Chrome渲染参数
	fmt.Println("\n=== 验证Chrome渲染参数 ===")
	fmt.Printf("Chrome窗口大小参数: --window-size=%d,%d\n", coverConfig.Card.Width, coverConfig.Card.Height)
	
	// 计算宽高比
	aspectRatio := float64(coverConfig.Card.Width) / float64(coverConfig.Card.Height)
	fmt.Printf("宽高比: %.2f (期望: 0.75, 即3:4比例)\n", aspectRatio)
	
	if aspectRatio > 0.74 && aspectRatio < 0.76 {
		fmt.Println("✅ 宽高比正确 (3:4比例)")
	} else {
		fmt.Printf("❌ 宽高比不正确: %.2f (期望: 0.75)\n", aspectRatio)
	}

	fmt.Println("\n✅ 封面卡片尺寸测试完成！")
}

// contains 检查字符串是否包含子字符串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || 
		(len(s) > len(substr) && (s[:len(substr)] == substr || 
		s[len(s)-len(substr):] == substr || 
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())))
}
EOF

# 测试3: 运行测试程序
echo "运行测试程序..."
go run test_cover_card_size.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_cover_card_size.go
rm -f debug_cover_test.html

echo "=== 测试完成 ==="
