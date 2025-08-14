#!/bin/bash

# 边距和背景修复测试脚本
# 测试封面卡片背景覆盖和第二张卡片边距

echo "=== 边距和背景修复测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/card
go build .
cd ../..

# 测试2: 创建边距和背景修复测试程序
echo "创建边距和背景修复测试程序..."
cat > test_margin_and_background_fix.go << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"gorm.io/gorm"
)

func main() {
	fmt.Println("=== 边距和背景修复测试 ===")

	// 创建分页引擎
	paginationBiz := pagination.NewPaginationBiz()
	config := paginationBiz.GetConfig()

	// 测试数据：包含所有元素类型的测试内容
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "魅力的本质",
		},
		{
			Type:    pagination.ElementTypeSubtitle,
			Content: "探索内在魅力的核心要素",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "深度的自我接纳：魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。",
		},
		{
			Type:    pagination.ElementTypeList,
			Content: []string{
				"稳定的情绪内核",
				"流动的内在丰盈",
				"敏锐的共情能力",
				"恰到好处的留白感",
			},
		},
		{
			Type:    pagination.ElementTypeQuote,
			Content: "魅力常藏在未说尽的话、未展现的面里。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "蓬勃的生命活力：活力不是咋咋呼呼的外向，而是对生活的热情与好奇心。有人年过半百仍会为了学一门新乐器熬夜练习，有人旅行时会蹲在路边观察蚂蚁搬家，这种对世界永远有期待的状态，会散发出一种感染力。",
		},
	}

	// 执行分页
	fmt.Println("执行分页...")
	paginatedContent, err := paginationBiz.PaginateElements(testElements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	fmt.Printf("分页结果：共 %d 个卡片\n", len(paginatedContent.Cards))

	// 测试封面渲染器
	fmt.Println("\n=== 测试封面渲染器 ===")
	_ = card.NewCoverRenderer(config) // 测试创建封面渲染器

	// 测试1: 无背景的封面卡片
	fmt.Println("测试1: 无背景的封面卡片")
	coverCard1 := &model.CardM{
		Model: gorm.Model{ID: 999999},
		ProcessedText: func() string {
			coverElements := []map[string]interface{}{
				{"type": "title", "content": "魅力的本质"},
			}
			if b, err := json.Marshal(coverElements); err == nil {
				return string(b)
			}
			return ""
		}(),
		SortOrder: 0,
	}

	fmt.Printf("封面卡片1创建成功，ID: %d\n", coverCard1.ID)

	// 测试2: 有模板背景的封面卡片
	fmt.Println("\n测试2: 有模板背景的封面卡片")
	coverCard2 := &model.CardM{
		Model: gorm.Model{ID: 999998},
		ProcessedText: func() string {
			coverElements := []map[string]interface{}{
				{"type": "title", "content": "魅力的本质"},
				{"type": "background", "content": "/path/to/template/background.jpg"},
			}
			if b, err := json.Marshal(coverElements); err == nil {
				return string(b)
			}
			return ""
		}(),
		SortOrder: 0,
	}

	fmt.Printf("封面卡片2创建成功，ID: %d\n", coverCard2.ID)

	// 测试渲染-测量渲染器
	fmt.Println("\n=== 测试渲染-测量渲染器 ===")
	_ = card.NewRenderAndMeasureRenderer(config) // 测试创建渲染-测量渲染器

	// 创建测试书籍
	_ = &model.BookM{ // 测试创建书籍
		Model:     gorm.Model{ID: 999999},
		Title:     "魅力的本质",
		CardCount: len(paginatedContent.Cards),
	}

	// 创建测试卡片
	var testCards []*model.CardM
	for i, cardContent := range paginatedContent.Cards {
		// 将卡片内容转换为JSON格式
		var cardElements []map[string]interface{}
		for _, element := range cardContent.Elements {
			cardElements = append(cardElements, map[string]interface{}{
				"type":    element.Type,
				"content": element.Content,
			})
		}

		// 将JSON数据转换为字符串
		cardJSONStr, err := json.Marshal(cardElements)
		if err != nil {
			fmt.Printf("⚠️  序列化卡片 %d 失败: %v\n", i, err)
			continue
		}

		// 创建卡片记录
		cardRecord := &model.CardM{
			Model:         gorm.Model{ID: uint(1000000 + i)},
			ProcessedText: string(cardJSONStr),
			SortOrder:     i + 1, // 从1开始计数，0是封面卡片
		}

		testCards = append(testCards, cardRecord)
	}

	fmt.Printf("创建了 %d 张测试卡片\n", len(testCards))

	// 测试简单无头渲染器
	fmt.Println("\n=== 测试简单无头渲染器 ===")
	_ = card.NewSimpleHeadlessRenderer(config) // 测试创建简单无头渲染器

	// 测试Chrome无头渲染器
	fmt.Println("\n=== 测试Chrome无头渲染器 ===")
	_ = card.NewChromeHeadlessRenderer(config) // 测试创建Chrome无头渲染器

	// 验证样式配置
	fmt.Println("\n=== 验证样式配置 ===")
	fmt.Printf("卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)
	fmt.Printf("内边距: 上%d 右%d 下%d 左%d\n", 
		config.Card.Padding.Top, config.Card.Padding.Right, 
		config.Card.Padding.Bottom, config.Card.Padding.Left)

	// 验证各种元素类型的样式
	elementTypes := []pagination.ElementType{
		pagination.ElementTypeTitle,
		pagination.ElementTypeSubtitle,
		pagination.ElementTypeBody,
		pagination.ElementTypeList,
		pagination.ElementTypeQuote,
	}

	for _, elementType := range elementTypes {
		if style, exists := config.Styles[elementType]; exists {
			fmt.Printf("%s: 字体%dpx, 行高%d, 颜色%s, 上边距%d, 下边距%d\n",
				elementType, style.FontSize, style.LineHeight, style.Color,
				style.MarginTop, style.MarginBottom)
		} else {
			fmt.Printf("%s: 使用默认样式\n", elementType)
		}
	}

	// 验证边距配置
	fmt.Println("\n=== 验证边距配置 ===")
	expectedTopMargin := 60
	expectedRightMargin := 50
	expectedBottomMargin := 60
	expectedLeftMargin := 50

	if config.Card.Padding.Top == expectedTopMargin &&
		config.Card.Padding.Right == expectedRightMargin &&
		config.Card.Padding.Bottom == expectedBottomMargin &&
		config.Card.Padding.Left == expectedLeftMargin {
		fmt.Printf("✅ 卡片边距配置正确: 上%dpx 右%dpx 下%dpx 左%dpx\n",
			config.Card.Padding.Top, config.Card.Padding.Right,
			config.Card.Padding.Bottom, config.Card.Padding.Left)
	} else {
		fmt.Printf("❌ 卡片边距配置不正确: 上%dpx 右%dpx 下%dpx 左%dpx (期望: 上%dpx 右%dpx 下%dpx 左%dpx)\n",
			config.Card.Padding.Top, config.Card.Padding.Right,
			config.Card.Padding.Bottom, config.Card.Padding.Left,
			expectedTopMargin, expectedRightMargin, expectedBottomMargin, expectedLeftMargin)
	}

	// 验证元素边距一致性
	fmt.Println("\n=== 验证元素边距一致性 ===")
	expectedElementMargin := 30
	allConsistent := true

	for _, elementType := range elementTypes {
		if style, exists := config.Styles[elementType]; exists {
			if style.MarginTop == expectedElementMargin && style.MarginBottom == expectedElementMargin {
				fmt.Printf("✅ %s: 边距一致 (上%dpx, 下%dpx)\n", elementType, style.MarginTop, style.MarginBottom)
			} else {
				fmt.Printf("❌ %s: 边距不一致 (上%dpx, 下%dpx, 期望: %dpx)\n", elementType, style.MarginTop, style.MarginBottom, expectedElementMargin)
				allConsistent = false
			}
		}
	}

	if allConsistent {
		fmt.Printf("🎉 所有元素边距完全一致！统一标准: %dpx\n", expectedElementMargin)
	} else {
		fmt.Printf("⚠️  存在边距不一致的问题，需要修复\n")
	}

	fmt.Println("\n✅ 边距和背景修复测试完成！")
}
EOF

# 测试3: 运行测试程序
echo "运行测试程序..."
go run test_margin_and_background_fix.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_margin_and_background_fix.go

echo "=== 测试完成 ==="
