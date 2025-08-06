#!/bin/bash

# 无头浏览器渲染器测试脚本

set -e

echo "=== 无头浏览器渲染器测试 ==="

# 创建测试目录
TEST_DIR="./test_headless_renderer"
mkdir -p "$TEST_DIR"

echo "1. 创建测试程序..."

# 创建一个简单的Go程序来测试无头浏览器渲染器
cat > "$TEST_DIR/test_headless.go" << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

func main() {
	// 创建测试配置
	config := &pagination.PaginationConfig{
		Card: pagination.CardConfig{
			Width:  800,
			Height: 600,
			Padding: pagination.Padding{
				Top:    40,
				Right:  40,
				Bottom: 40,
				Left:   40,
			},
		},
	}

	// 创建渲染器
	renderer := card.NewHeadlessRenderer(config)

	// 创建测试卡片数据
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "联机时代的独立思考者",
		},
		{
			Type:    pagination.ElementTypeSubtitle,
			Content: "未来竞争力的进化之路",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "在这个信息爆炸的时代，独立思考能力变得越来越重要。我们需要学会在纷繁复杂的信息中筛选出有价值的内容，形成自己的判断和观点。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"培养批判性思维",
				"保持开放的心态",
				"持续学习和更新知识",
				"实践和反思相结合",
			},
		},
		{
			Type:    pagination.ElementTypeQuote,
			Content: "真正的智慧不在于知道答案，而在于知道如何提问。",
		},
	}

	// 将元素转换为JSON
	processedText, err := json.Marshal(testElements)
	if err != nil {
		log.Fatalf("Failed to marshal elements: %v", err)
	}

	// 创建测试卡片
	testCard := &model.CardM{
		ID:            1,
		ProcessedText: string(processedText),
		SortOrder:     1,
	}

	// 渲染卡片
	renderedCard, err := renderer.RenderCardToImage(testCard)
	if err != nil {
		log.Fatalf("Failed to render card: %v", err)
	}

	fmt.Printf("✅ 卡片渲染成功!\n")
	fmt.Printf("卡片ID: %d\n", renderedCard.CardID)
	fmt.Printf("图片URL: %s\n", renderedCard.ImageURL)
	fmt.Printf("图片尺寸: %dx%d\n", renderedCard.Width, renderedCard.Height)
	fmt.Printf("排序: %d\n", renderedCard.SortOrder)
}
EOF

echo "2. 编译并运行测试程序..."
cd "$TEST_DIR"
go mod init test_headless
go mod edit -replace numind-server=../..
go run test_headless.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "../images/upload/card/1/card_1.png" ]; then
	echo "✅ 无头浏览器渲染器测试图片生成成功"
	echo "图片路径: ../images/upload/card/1/card_1.png"
	echo "请打开图片查看渲染效果"
else
	echo "❌ 图片生成失败"
fi

echo ""
echo "4. 清理测试文件..."
cd ..
rm -rf "$TEST_DIR"

echo "✅ 无头浏览器渲染器测试完成" 