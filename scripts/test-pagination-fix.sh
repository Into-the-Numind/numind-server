#!/bin/bash

# 分页修复测试脚本
echo "=== 分页修复测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/book
go build .
cd ../pagination
go build .
cd ../..

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
	fmt.Println("=== 分页修复测试 ===")

	// 创建分页引擎
	engine := pagination.NewPaginationEngine(pagination.GetDefaultConfig())
	config := pagination.GetDefaultConfig()

	fmt.Printf("分页配置 - 卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)

	// 测试数据：模拟用户的长文本输入
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeBody,
			Content: "我好像发现了魅力的本质! 1.深度的自我接纳 魅力的起点往往是对自我的全然接纳。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "2.稳定的情绪内核 情绪稳定并非毫无波澜，而是在面对突发状况时能快速调整状态。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "3.流动的内在丰盈 内在丰盈不是死记硬背的知识堆砌，而是将经历、思考、兴趣内化成一种感知力。",
		},
	}

	fmt.Printf("测试数据 - 元素数量: %d\n", len(testElements))

	// 执行分页
	fmt.Println("\n开始执行分页...")
	result, err := engine.PaginateElements(testElements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	// 输出结果
	fmt.Printf("\n分页结果：共 %d 个卡片\n", len(result.Cards))
	
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		fmt.Printf("元素数量: %d\n", len(card.Elements))
		
		for j, element := range card.Elements {
			fmt.Printf("  %d. [%s] %s\n", j+1, element.Type, 
				func() string {
					if str, ok := element.Content.(string); ok {
						if len(str) > 50 {
							return str[:50] + "..."
						}
						return str
					}
					return fmt.Sprintf("%v", element.Content)
				}())
		}
	}

	fmt.Println("\n✅ 分页修复测试完成！")
}
EOF

# 测试3: 运行测试程序
echo "运行分页测试程序..."
go run test_pagination_fix.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_pagination_fix.go

echo "=== 测试完成 ==="
