#!/bin/bash

# 分页算法修复测试脚本
# 测试智能分页和内容平衡性

echo "=== 分页算法修复测试 ==="

# 测试1: 编译检查
echo "检查分页算法编译..."
cd internal/numind/biz/pagination
go build .
cd ../../..

# 测试2: 创建测试程序
echo "创建分页测试程序..."
cat > test_pagination_fix.go << 'EOF'
package main

import (
	"fmt"
	"log"

	"numind-server/internal/numind/biz/pagination"
)

func main() {
	fmt.Println("=== 分页算法修复测试 ===")

	// 创建分页引擎
	engine := pagination.NewPaginationEngine(pagination.GetDefaultConfig())

	// 测试数据：模拟你遇到的11个要点问题
	elements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "魅力的11个核心要素",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "深度的自我接纳：魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "稳定的情绪内核：情绪稳定并非毫无波澜，而是在面对突发状况、负面评价或生活起伏时，能快速调整状态，不被情绪牵着走。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "流动的内在丰盈：内在丰盈不是死记硬背的知识堆砌，而是将经历、思考、兴趣内化成一种感知力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "敏锐的共情能力：共情不是简单的我理解你，而是能精准捕捉对方未说出口的情绪。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "恰到好处的留白感：过度暴露自己的人往往会失去神秘感，而魅力常藏在未说尽的话、未展现的面里。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "蓬勃的生命活力：活力不是咋咋呼呼的外向，而是对生活的热情与好奇心。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "清晰的边界意识：有魅力的人懂得守住自己的底线，尊重他人的空间。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "高级的幽默感：幽默不是低俗的玩笑或嘲讽，而是用智慧化解尴尬、传递善意。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "沉浸的专注感：做事时全神贯注的状态自带吸引力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "纯粹的真诚感：真诚不是口无遮拦，而是言行一致、不套路。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "独特的审美力：审美不是穿名牌、赶潮流，而是对美有自己的理解和表达。",
		},
	}

	// 执行分页
	fmt.Println("\n开始执行分页...")
	result, err := engine.Paginate(elements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	// 输出结果
	fmt.Printf("\n分页结果：共 %d 个卡片\n", len(result.Cards))
	
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		fmt.Printf("元素数量: %d\n", len(card.Elements))
		
		// 显示内容预览
		for j, element := range card.Elements {
			fmt.Printf("  %d. [%s]\n", j+1, element.Type)
			
			// 显示内容预览
			var content string
			switch v := element.Content.(type) {
			case string:
				if len(v) > 60 {
					content = v[:60] + "..."
				} else {
					content = v
				}
			case []string:
				content = fmt.Sprintf("列表项数量: %d", len(v))
			default:
				content = fmt.Sprintf("%v", v)
			}
			fmt.Printf("     内容: %s\n", content)
		}
	}

	// 输出配置信息
	fmt.Println("\n=== 配置信息 ===")
	config := pagination.GetDefaultConfig()
	fmt.Printf("卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)
	fmt.Printf("内边距: 上%d 右%d 下%d 左%d\n",
		config.Card.Padding.Top,
		config.Card.Padding.Right,
		config.Card.Padding.Bottom,
		config.Card.Padding.Left)

	fmt.Printf("\n样式配置:\n")
	for elementType, style := range config.Styles {
		fmt.Printf("  %s: 字体%dpx, 行高%.1f倍, 上边距%d, 下边距%d, 颜色%s\n",
			elementType, style.FontSize, float64(style.LineHeight)/float64(style.FontSize), 
			style.MarginTop, style.MarginBottom, style.Color)
	}

	fmt.Println("\n=== 测试完成 ===")
}
EOF

# 测试3: 运行测试程序
echo "运行分页测试程序..."
go run test_pagination_fix.go

# 清理
rm test_pagination_fix.go

echo ""
echo "测试完成！"
echo ""
echo "主要修复："
echo "1. 统一了所有元素的上下边距为30rpx"
echo "2. 增强了智能分页逻辑，考虑内容平衡性"
echo "3. 当剩余空间少于20%时，智能判断是否开始新卡片"
echo "4. 优化了长文本的智能切分算法"
echo "5. 确保内容分布更均匀，避免卡片内容过多或过少"
