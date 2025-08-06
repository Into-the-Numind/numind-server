#!/bin/bash

# 简单卡片渲染测试脚本

set -e

echo "=== 简单卡片渲染测试 ==="

# 创建测试目录
TEST_DIR="./test_card_rendering_simple"
mkdir -p "$TEST_DIR"

echo "1. 创建测试程序..."

# 创建一个简单的Go程序来测试卡片渲染
cat > "$TEST_DIR/test_card_simple.go" << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// 模拟卡片数据
type CardData struct {
	ID           uint   `json:"id"`
	UserID       uint   `json:"user_id"`
	BookID       uint   `json:"book_id"`
	ProcessedText string `json:"processed_text"`
	SortOrder    int    `json:"sort_order"`
}

// 模拟元素数据
type Element struct {
	Content interface{} `json:"content"`
	Type    string      `json:"type"`
}

func main() {
	// 创建测试卡片数据
	cardData := CardData{
		ID:           21,
		UserID:       2,
		BookID:       11,
		SortOrder:    1,
	}

	// 创建测试元素
	elements := []Element{
		{
			Content: "联机时代的独立思考者",
			Type:    "title",
		},
		{
			Content: "未来竞争力的进化之路",
			Type:    "subtitle",
		},
		{
			Content: "人类记住知识的方式持续了两千多年，而近20年内，新的认知方式突然成为主流。",
			Type:    "body",
		},
		{
			Content: []string{
				"我今天做的事，机器能做吗？",
				"我今天做的事，会被外包吗？",
				"我今天做的事，明天会做得更好吗？",
			},
			Type: "list",
		},
	}

	// 序列化为JSON
	processedText, _ := json.Marshal(elements)
	cardData.ProcessedText = string(processedText)

	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	
	// 设置背景色
	for y := 0; y < 600; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	// 渲染元素
	currentY := 60 // padding top
	for _, element := range elements {
		height := renderElement(img, element, currentY)
		currentY += height
	}

	// 保存图片
	file, err := os.Create("test_card_simple_output.png")
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		fmt.Printf("编码图片失败: %v\n", err)
		return
	}

	fmt.Println("✅ 简单卡片渲染测试图片创建成功: test_card_simple_output.png")
	fmt.Println("请查看图片文件，检查文本是否正确显示")
}

func renderElement(img *image.RGBA, element Element, y int) int {
	switch element.Type {
	case "title":
		return renderText(img, fmt.Sprintf("%v", element.Content), 50, y, 24, "#333333", 1.4)
	case "subtitle":
		return renderText(img, fmt.Sprintf("%v", element.Content), 50, y, 20, "#666666", 1.5)
	case "body":
		return renderText(img, fmt.Sprintf("%v", element.Content), 50, y, 16, "#333333", 1.6)
	case "list":
		return renderList(img, element.Content, 50, y, 16, "#333333", 1.6)
	default:
		return renderText(img, fmt.Sprintf("%v", element.Content), 50, y, 16, "#333333", 1.6)
	}
}

func renderText(img *image.RGBA, text string, x, y int, fontSize int, colorStr string, lineHeight float64) int {
	textColor := parseColor(colorStr)
	
	// 简单的文本换行
	lines := wrapText(text, 300, fontSize)
	
	currentY := y
	for _, line := range lines {
		drawTextLine(img, line, x, currentY, fontSize, textColor)
		currentY += int(float64(fontSize) * lineHeight)
	}
	
	return currentY - y
}

func renderList(img *image.RGBA, content interface{}, x, y int, fontSize int, colorStr string, lineHeight float64) int {
	var items []string
	switch v := content.(type) {
	case []string:
		items = v
	case []interface{}:
		for _, item := range v {
			items = append(items, fmt.Sprintf("%v", item))
		}
	default:
		items = []string{fmt.Sprintf("%v", content)}
	}

	currentY := y
	for _, item := range items {
		text := fmt.Sprintf("• %s", item)
		height := renderText(img, text, x, currentY, fontSize, colorStr, lineHeight)
		currentY += height + 8
	}
	
	return currentY - y
}

func wrapText(text string, maxWidth int, fontSize int) []string {
	charsPerLine := maxWidth / (fontSize / 2)
	if charsPerLine <= 0 {
		charsPerLine = 30
	}
	
	var lines []string
	runes := []rune(text)
	
	for i := 0; i < len(runes); i += charsPerLine {
		end := i + charsPerLine
		if end > len(runes) {
			end = len(runes)
		}
		lines = append(lines, string(runes[i:end]))
	}
	
	return lines
}

func drawTextLine(img *image.RGBA, text string, x, y int, fontSize int, textColor color.Color) {
	point := fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6((y + fontSize) * 64)}
	
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13,
		Dot:  point,
	}
	d.DrawString(text)
}

func parseColor(colorStr string) color.Color {
	switch colorStr {
	case "#333333":
		return color.RGBA{51, 51, 51, 255}
	case "#666666":
		return color.RGBA{102, 102, 102, 255}
	case "#1E90FF":
		return color.RGBA{30, 144, 255, 255}
	default:
		return color.RGBA{51, 51, 51, 255}
	}
}
EOF

echo "2. 编译并运行测试程序..."
cd "$TEST_DIR"
go mod init test_card_simple
go run test_card_simple.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "test_card_simple_output.png" ]; then
	echo "✅ 简单卡片渲染测试图片生成成功"
	echo "图片大小: $(ls -lh test_card_simple_output.png | awk '{print $5}')"
	echo "请打开 test_card_simple_output.png 查看渲染效果"
else
	echo "❌ 简单卡片渲染测试图片生成失败"
fi

echo ""
echo "=== 测试完成 ==="
echo "这个测试模拟了实际的卡片渲染过程"
echo "如果文本显示正常，说明渲染器工作正常" 