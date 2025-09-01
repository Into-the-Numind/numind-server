#!/bin/bash

# 卡片尺寸一致性测试脚本
# 验证所有渲染器的卡片尺寸是否与右边图片（第二张）保持一致

echo "=== 卡片尺寸一致性测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/card
go build .
cd ../..

# 测试2: 创建尺寸测试程序
echo "创建尺寸测试程序..."
cat > test_size_consistency.go << 'EOF'
package main

import (
	"fmt"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
)

func main() {
	fmt.Println("=== 卡片尺寸一致性测试 ===")

	// 获取默认配置
	config := pagination.GetDefaultConfig()
	
	fmt.Printf("默认配置 - 卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)
	fmt.Printf("默认配置 - 内边距: 上%dpx 右%dpx 下%dpx 左%dpx\n", 
		config.Card.Padding.Top, config.Card.Padding.Right, 
		config.Card.Padding.Bottom, config.Card.Padding.Left)

	// 测试封面渲染器
	fmt.Println("\n=== 测试封面渲染器 ===")
	_ = card.NewCoverRenderer(config) // 测试创建封面渲染器
	coverConfig := card.GetCoverConfig()
	fmt.Printf("封面配置 - 卡片尺寸: %dx%d\n", coverConfig.Card.Width, coverConfig.Card.Height)
	
	// 验证封面尺寸是否与默认配置一致
	if coverConfig.Card.Width == config.Card.Width && coverConfig.Card.Height == config.Card.Height {
		fmt.Println("✅ 封面渲染器尺寸与默认配置一致")
	} else {
		fmt.Printf("❌ 封面渲染器尺寸与默认配置不一致: 封面=%dx%d, 默认=%dx%d\n", 
			coverConfig.Card.Width, coverConfig.Card.Height, config.Card.Width, config.Card.Height)
	}

	// 测试渲染-测量渲染器
	fmt.Println("\n=== 测试渲染-测量渲染器 ===")
	renderAndMeasureRenderer := card.NewRenderAndMeasureRenderer(config)
	if renderAndMeasureRenderer != nil {
		fmt.Println("✅ 渲染-测量渲染器创建成功")
	} else {
		fmt.Println("❌ 渲染-测量渲染器创建失败")
	}

	// 测试Chrome无头渲染器
	fmt.Println("\n=== 测试Chrome无头渲染器 ===")
	chromeRenderer := card.NewChromeHeadlessRenderer(config)
	if chromeRenderer != nil {
		fmt.Println("✅ Chrome无头渲染器创建成功")
	} else {
		fmt.Println("❌ Chrome无头渲染器创建失败")
	}

	// 测试简单无头渲染器
	fmt.Println("\n=== 测试简单无头渲染器 ===")
	simpleRenderer := card.NewSimpleHeadlessRenderer(config)
	if simpleRenderer != nil {
		fmt.Println("✅ 简单无头渲染器创建成功")
	} else {
		fmt.Println("❌ 简单无头渲染器创建失败")
	}

	// 测试高级渲染器
	fmt.Println("\n=== 测试高级渲染器 ===")
	advancedRenderer := card.NewAdvancedRenderer(config)
	if advancedRenderer != nil {
		fmt.Println("✅ 高级渲染器创建成功")
	} else {
		fmt.Println("❌ 高级渲染器创建失败")
	}

	// 验证所有渲染器的尺寸配置
	fmt.Println("\n=== 验证尺寸配置 ===")
	expectedWidth := 1080
	expectedHeight := 1440
	
	if config.Card.Width == expectedWidth && config.Card.Height == expectedHeight {
		fmt.Printf("✅ 卡片尺寸配置正确: %dx%d (期望: %dx%d)\n", 
			config.Card.Width, config.Card.Height, expectedWidth, expectedHeight)
	} else {
		fmt.Printf("❌ 卡片尺寸配置不正确: %dx%d (期望: %dx%d)\n", 
			config.Card.Width, config.Card.Height, expectedWidth, expectedHeight)
	}

	// 验证内边距配置
	fmt.Println("\n=== 验证内边距配置 ===")
	expectedTopMargin := 60
	expectedRightMargin := 50
	expectedBottomMargin := 60
	expectedLeftMargin := 50

	if config.Card.Padding.Top == expectedTopMargin &&
		config.Card.Padding.Right == expectedRightMargin &&
		config.Card.Padding.Bottom == expectedBottomMargin &&
		config.Card.Padding.Left == expectedLeftMargin {
		fmt.Printf("✅ 内边距配置正确: 上%dpx 右%dpx 下%dpx 左%dpx\n",
			config.Card.Padding.Top, config.Card.Padding.Right,
			config.Card.Padding.Bottom, config.Card.Padding.Left)
	} else {
		fmt.Printf("❌ 内边距配置不正确: 上%dpx 右%dpx 下%dpx 左%dpx (期望: 上%dpx 右%dpx 下%dpx 左%dpx)\n",
			config.Card.Padding.Top, config.Card.Padding.Right,
			config.Card.Padding.Bottom, config.Card.Padding.Left,
			expectedTopMargin, expectedRightMargin, expectedBottomMargin, expectedLeftMargin)
	}

	// 验证样式配置
	fmt.Println("\n=== 验证样式配置 ===")
	elementTypes := []pagination.ElementType{
		pagination.ElementTypeTitle,
		pagination.ElementTypeSubtitle,
		pagination.ElementTypeBody,
		pagination.ElementTypeList,
		pagination.ElementTypeQuote,
		pagination.ElementTypeTag,
		pagination.ElementTypeNumber,
	}

	for _, elementType := range elementTypes {
		if style, exists := config.Styles[elementType]; exists {
			fmt.Printf("%s: 字体%dpx, 行高%d, 颜色%s, 上边距%dpx, 下边距%dpx\n",
				elementType, style.FontSize, style.LineHeight, style.Color,
				style.MarginTop, style.MarginBottom)
		} else {
			fmt.Printf("%s: 使用默认样式\n", elementType)
		}
	}

	// 验证字体大小单位一致性
	fmt.Println("\n=== 验证字体大小单位一致性 ===")
	allConsistent := true
	expectedFontSizes := map[pagination.ElementType]int{
		pagination.ElementTypeTitle:    64,
		pagination.ElementTypeSubtitle: 48,
		pagination.ElementTypeBody:     36,
		pagination.ElementTypeList:     36,
		pagination.ElementTypeQuote:    36,
		pagination.ElementTypeTag:      28,
		pagination.ElementTypeNumber:   28,
	}

	for elementType, expectedSize := range expectedFontSizes {
		if style, exists := config.Styles[elementType]; exists {
			if style.FontSize == expectedSize {
				fmt.Printf("✅ %s: 字体大小%dpx (期望: %dpx)\n", elementType, style.FontSize, expectedSize)
			} else {
				fmt.Printf("❌ %s: 字体大小%dpx (期望: %dpx)\n", elementType, style.FontSize, expectedSize)
				allConsistent = false
			}
		}
	}

	if allConsistent {
		fmt.Println("🎉 所有字体大小配置完全一致！")
	} else {
		fmt.Println("⚠️  存在字体大小配置不一致的问题")
	}

	// 验证边距单位一致性
	fmt.Println("\n=== 验证边距单位一致性 ===")
	allMarginsConsistent := true
	expectedMargins := map[pagination.ElementType]int{
		pagination.ElementTypeTitle:    30,
		pagination.ElementTypeSubtitle: 30, // 上边距
		pagination.ElementTypeBody:     30,
		pagination.ElementTypeList:     30,
		pagination.ElementTypeQuote:    30,
		pagination.ElementTypeTag:      30,
		pagination.ElementTypeNumber:   30,
	}

	for elementType, expectedMargin := range expectedMargins {
		if style, exists := config.Styles[elementType]; exists {
			if style.MarginTop == expectedMargin {
				fmt.Printf("✅ %s: 上边距%dpx (期望: %dpx)\n", elementType, style.MarginTop, expectedMargin)
			} else {
				fmt.Printf("❌ %s: 上边距%dpx (期望: %dpx)\n", elementType, style.MarginTop, expectedMargin)
				allMarginsConsistent = false
			}
		}
	}

	if allMarginsConsistent {
		fmt.Println("🎉 所有上边距配置完全一致！")
	} else {
		fmt.Println("⚠️  存在上边距配置不一致的问题")
	}

	// 验证副标题下边距特殊规则
	fmt.Println("\n=== 验证副标题下边距特殊规则 ===")
	if subtitleStyle, exists := config.Styles[pagination.ElementTypeSubtitle]; exists {
		if subtitleStyle.MarginBottom == 25 {
			fmt.Printf("✅ 副标题下边距: %dpx (特殊规则: 25px)\n", subtitleStyle.MarginBottom)
		} else {
			fmt.Printf("❌ 副标题下边距: %dpx (期望: 25px)\n", subtitleStyle.MarginBottom)
		}
	}

	fmt.Println("\n✅ 尺寸一致性测试完成！")
}
EOF

# 测试3: 运行测试程序
echo "运行测试程序..."
go run test_size_consistency.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_size_consistency.go

echo "=== 测试完成 ==="
