#!/bin/bash

# 无头浏览器渲染器调试脚本

set -e

echo "=== 无头浏览器渲染器调试测试 ==="

# 创建测试目录
TEST_DIR="./test_headless_debug"
mkdir -p "$TEST_DIR"

echo "1. 创建调试测试程序..."

# 创建一个简单的Go程序来测试无头浏览器渲染器
cat > "$TEST_DIR/test_debug.go" << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"gorm.io/gorm"
	"os"
)

func main() {
	fmt.Println("=== 无头浏览器渲染器调试测试 ===")

	// 创建测试配置
	config := &pagination.PaginationConfig{
		Card: pagination.CardConfig{
			Width:  800,
			Height: 600,
			Padding: struct {
				Top    int `json:"top"`
				Right  int `json:"right"`
				Bottom int `json:"bottom"`
				Left   int `json:"left"`
			}{
				Top:    40,
				Right:  40,
				Bottom: 40,
				Left:   40,
			},
		},
	}

	// 创建渲染器
	renderer := card.NewHeadlessRenderer(config)

	// 创建测试卡片数据 - 使用简单的测试数据
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "测试标题",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "这是一个测试内容，用于验证无头浏览器渲染器是否正常工作。",
		},
	}

	// 将元素转换为JSON
	processedText, err := json.Marshal(testElements)
	if err != nil {
		log.Fatalf("Failed to marshal elements: %v", err)
	}

	fmt.Printf("生成的JSON数据: %s\n", string(processedText))

	// 创建测试卡片
	testCard := &model.CardM{
		Model:         gorm.Model{ID: 999},
		ProcessedText: string(processedText),
		SortOrder:     1,
	}

	fmt.Println("开始渲染卡片...")
	
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

	// 检查生成的图片文件
	imagePath := fmt.Sprintf("./images/upload/card/%d/card_%d.png", renderedCard.CardID, renderedCard.CardID)
	if _, err := os.Stat(imagePath); err == nil {
		fmt.Printf("✅ 图片文件存在: %s\n", imagePath)
		// 获取文件大小
		if info, err := os.Stat(imagePath); err == nil {
			fmt.Printf("   文件大小: %d bytes\n", info.Size())
		}
	} else {
		fmt.Printf("❌ 图片文件不存在: %s\n", imagePath)
	}
}
EOF

echo "2. 编译并运行调试程序..."
cd "$TEST_DIR"
go mod init test_debug
go mod edit -replace numind-server=../..
go run test_debug.go

echo ""
echo "3. 检查生成的调试文件..."
if [ -f "debug_card_*.html" ]; then
	echo "✅ 调试HTML文件已生成"
	echo "HTML文件内容:"
	cat debug_card_*.html
else
	echo "❌ 没有找到调试HTML文件"
fi

echo ""
echo "4. 清理测试文件..."
cd ..
rm -rf "$TEST_DIR"

echo "✅ 调试测试完成" 