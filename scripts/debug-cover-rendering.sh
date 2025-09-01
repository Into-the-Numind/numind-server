#!/bin/bash

# 调试封面卡片渲染脚本
# 检查封面卡片的实际HTML输出和渲染过程

echo "=== 调试封面卡片渲染 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/card
go build .
cd ../..

# 测试2: 创建调试程序
echo "创建调试程序..."
cat > debug_cover_rendering.go << 'EOF'
package main

import (
	"fmt"
	"os"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
)

func main() {
	fmt.Println("=== 调试封面卡片渲染 ===")

	// 获取默认配置
	config := pagination.GetDefaultConfig()
	
	fmt.Printf("默认配置 - 卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)

	// 测试封面渲染器
	coverRenderer := card.NewCoverRenderer(config)
	coverConfig := card.GetCoverConfig()
	fmt.Printf("封面配置 - 卡片尺寸: %dx%d\n", coverConfig.Card.Width, coverConfig.Card.Height)
	
	// 测试不同的封面数据
	testCases := []struct {
		name        string
		coverData   card.CoverCardData
		description string
	}{
		{
			name: "无背景封面",
			coverData: card.CoverCardData{
				Title: "魅力的本质",
			},
			description: "测试无背景时的封面渲染",
		},
		{
			name: "有背景封面",
			coverData: card.CoverCardData{
				Title:      "魅力的本质",
				Background: "/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/res/template/t3.png",
			},
			description: "测试有背景时的封面渲染",
		},
		{
			name: "有图片封面",
			coverData: card.CoverCardData{
				Title:    "魅力的本质",
				ImageURL: "/path/to/image.jpg",
			},
			description: "测试有图片时的封面渲染",
		},
	}

	for i, testCase := range testCases {
		fmt.Printf("\n=== 测试用例 %d: %s ===\n", i+1, testCase.name)
		fmt.Printf("描述: %s\n", testCase.description)
		fmt.Printf("数据: Title='%s', Background='%s', ImageURL='%s'\n", 
			testCase.coverData.Title, testCase.coverData.Background, testCase.coverData.ImageURL)
		
		// 生成HTML内容
		htmlContent := coverRenderer.GenerateCoverHTML(testCase.coverData, coverConfig)
		
		// 保存HTML文件
		filename := fmt.Sprintf("debug_cover_%d_%s.html", i+1, testCase.name)
		if err := os.WriteFile(filename, []byte(htmlContent), 0644); err != nil {
			fmt.Printf("❌ 保存HTML文件失败: %v\n", err)
		} else {
			fmt.Printf("✅ HTML文件已保存到: %s\n", filename)
			
			// 分析HTML内容
			fmt.Println("\n--- HTML内容分析 ---")
			
			// 检查尺寸设置
			checkSizeSetting(htmlContent, "html", coverConfig.Card.Width, coverConfig.Card.Height)
			checkSizeSetting(htmlContent, "body", coverConfig.Card.Width, coverConfig.Card.Height)
			
			// 检查CSS样式
			checkCSSStyle(htmlContent, ".cover-container")
			checkCSSStyle(htmlContent, ".image-section")
			checkCSSStyle(htmlContent, ".title-section")
			
			// 检查背景设置
			if testCase.coverData.Background != "" {
				checkBackgroundSetting(htmlContent, testCase.coverData.Background)
			}
			
			// 检查布局设置
			checkLayoutSetting(htmlContent)
		}
	}

	fmt.Println("\n✅ 调试完成！")
}

// checkSizeSetting 检查尺寸设置
func checkSizeSetting(htmlContent, element string, expectedWidth, expectedHeight int) {
	widthPattern := fmt.Sprintf("width: %dpx", expectedWidth)
	heightPattern := fmt.Sprintf("height: %dpx", expectedHeight)
	
	if contains(htmlContent, widthPattern) {
		fmt.Printf("✅ %s 宽度设置正确: %dpx\n", element, expectedWidth)
	} else {
		fmt.Printf("❌ %s 宽度设置不正确，期望: %dpx\n", element, expectedWidth)
	}
	
	if contains(htmlContent, heightPattern) {
		fmt.Printf("✅ %s 高度设置正确: %dpx\n", element, expectedHeight)
	} else {
		fmt.Printf("❌ %s 高度设置不正确，期望: %dpx\n", element, expectedHeight)
	}
}

// checkCSSStyle 检查CSS样式
func checkCSSStyle(htmlContent, selector string) {
	if contains(htmlContent, selector+" {") {
		fmt.Printf("✅ CSS样式 %s 存在\n", selector)
	} else {
		fmt.Printf("❌ CSS样式 %s 缺失\n", selector)
	}
}

// checkBackgroundSetting 检查背景设置
func checkBackgroundSetting(htmlContent, background string) {
	if contains(htmlContent, "background: url('") {
		fmt.Println("✅ 背景URL设置存在")
	} else {
		fmt.Println("❌ 背景URL设置缺失")
	}
	
	if contains(htmlContent, "background-size: cover") {
		fmt.Println("✅ background-size设置正确")
	} else {
		fmt.Println("❌ background-size设置缺失")
	}
	
	if contains(htmlContent, "background-position: center") {
		fmt.Println("✅ background-position设置正确")
	} else {
		fmt.Println("❌ background-position设置缺失")
	}
}

// checkLayoutSetting 检查布局设置
func checkLayoutSetting(htmlContent string) {
	if contains(htmlContent, "display: flex") {
		fmt.Println("✅ flex布局设置正确")
	} else {
		fmt.Println("❌ flex布局设置缺失")
	}
	
	if contains(htmlContent, "flex-direction: column") {
		fmt.Println("✅ flex-direction设置正确")
	} else {
		fmt.Println("❌ flex-direction设置缺失")
	}
	
	if contains(htmlContent, "flex: 1") {
		fmt.Println("✅ flex: 1设置正确")
	} else {
		fmt.Println("❌ flex: 1设置缺失")
	}
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

# 测试3: 运行调试程序
echo "运行调试程序..."
go run debug_cover_rendering.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f debug_cover_rendering.go

echo "=== 调试完成 ==="
