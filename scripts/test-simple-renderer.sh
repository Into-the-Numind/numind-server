#!/bin/bash

# 简单渲染器测试脚本

set -e

echo "=== 简单渲染器测试 ==="

# 创建测试目录
TEST_DIR="./test_simple_renderer"
mkdir -p "$TEST_DIR"

echo "1. 创建测试程序..."

# 创建一个简单的Go程序来测试简单渲染器
cat > "$TEST_DIR/test_simple.go" << 'EOF'
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// SimpleRenderer 简单文本渲染器
type SimpleRenderer struct {
	cardWidth  int
	cardHeight int
	paddingTop int
	paddingLeft int
}

// NewSimpleRenderer 创建新的简单渲染器
func NewSimpleRenderer() *SimpleRenderer {
	return &SimpleRenderer{
		cardWidth:   400,
		cardHeight:  600,
		paddingTop:  60,
		paddingLeft: 50,
	}
}

func main() {
	// 创建渲染器
	renderer := NewSimpleRenderer()

	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, renderer.cardWidth, renderer.cardHeight))
	
	// 设置背景色
	for y := 0; y < renderer.cardHeight; y++ {
		for x := 0; x < renderer.cardWidth; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	// 测试文本
	testTexts := []string{
		"Hello World",
		"你好世界",
		"联机时代的独立思考者",
		"未来竞争力的进化之路",
		"• 列表项目1",
		"• 列表项目2",
	}

	// 渲染文本
	y := renderer.paddingTop
	for _, text := range testTexts {
		renderer.renderText(img, text, renderer.paddingLeft, y, 24, "#333333", 1.5)
		y += 40
	}

	// 保存图片
	file, err := os.Create("test_simple_output.png")
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		fmt.Printf("编码图片失败: %v\n", err)
		return
	}

	fmt.Println("✅ 简单渲染器测试图片创建成功: test_simple_output.png")
	fmt.Println("请查看图片文件，检查中文字符是否正确显示")
}

// renderText 渲染文本
func (r *SimpleRenderer) renderText(img *image.RGBA, text string, x, y int, fontSize int, colorStr string, lineHeight float64) int {
	// 解析颜色
	textColor := r.parseColor(colorStr)
	
	// 绘制文本
	r.drawTextLine(img, text, x, y, fontSize, textColor)
	
	return int(float64(fontSize) * lineHeight)
}

// drawTextLine 绘制单行文本
func (r *SimpleRenderer) drawTextLine(img *image.RGBA, text string, x, y int, fontSize int, textColor color.Color) {
	// 使用简单的像素绘制方法
	charWidth := fontSize / 2
	charHeight := fontSize
	
	for i, char := range text {
		charX := x + i*charWidth
		charY := y
		
		// 绘制字符的简单表示
		if char == '•' {
			// 绘制项目符号
			r.drawBullet(img, charX, charY, charWidth, charHeight, textColor)
		} else if char >= 0x4E00 && char <= 0x9FFF {
			// 中文字符，绘制一个填充的矩形
			r.drawChineseChar(img, charX, charY, charWidth, charHeight, textColor)
		} else {
			// 英文字符，绘制简单的字符表示
			r.drawEnglishChar(img, char, charX, charY, charWidth, charHeight, textColor)
		}
	}
}

// drawBullet 绘制项目符号
func (r *SimpleRenderer) drawBullet(img *image.RGBA, x, y, width, height int, color color.Color) {
	// 绘制一个圆形项目符号
	centerX := x + width/2
	centerY := y + height/2
	radius := width / 4
	
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= radius*radius {
				img.Set(centerX+dx, centerY+dy, color)
			}
		}
	}
}

// drawChineseChar 绘制中文字符
func (r *SimpleRenderer) drawChineseChar(img *image.RGBA, x, y, width, height int, color color.Color) {
	// 绘制一个填充的矩形来表示中文字符
	rect := image.Rect(x, y, x+width-1, y+height-1)
	for py := rect.Min.Y; py <= rect.Max.Y; py++ {
		for px := rect.Min.X; px <= rect.Max.X; px++ {
			img.Set(px, py, color)
		}
	}
}

// drawEnglishChar 绘制英文字符
func (r *SimpleRenderer) drawEnglishChar(img *image.RGBA, char rune, x, y, width, height int, color color.Color) {
	// 简单的英文字符绘制
	rect := image.Rect(x, y, x+width/2-1, y+height-1)
	for py := rect.Min.Y; py <= rect.Max.Y; py++ {
		for px := rect.Min.X; px <= rect.Max.X; px++ {
			img.Set(px, py, color)
		}
	}
}

// parseColor 解析颜色字符串
func (r *SimpleRenderer) parseColor(colorStr string) color.Color {
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
go mod init test_simple
go run test_simple.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "test_simple_output.png" ]; then
	echo "✅ 简单渲染器测试图片生成成功"
	echo "图片大小: $(ls -lh test_simple_output.png | awk '{print $5}')"
	echo "请打开 test_simple_output.png 查看渲染效果"
	echo ""
	echo "中文字符应该显示为填充的矩形，而不是问号"
else
	echo "❌ 简单渲染器测试图片生成失败"
fi

echo ""
echo "=== 测试完成 ==="
echo "如果中文字符显示为矩形而不是问号，说明简单渲染器工作正常" 