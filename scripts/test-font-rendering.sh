#!/bin/bash

# 字体渲染测试脚本

set -e

echo "=== 字体渲染测试 ==="

# 创建测试目录
TEST_DIR="./test_font_rendering"
mkdir -p "$TEST_DIR"

echo "1. 创建测试图片..."

# 创建一个简单的Go程序来测试字体渲染
cat > "$TEST_DIR/test_font.go" << 'EOF'
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/math/fixed"
)

func main() {
	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	
	// 设置背景色
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
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

	// 尝试使用支持中文的字体
	fontData, err := opentype.Parse(goregular.TTF)
	if err != nil {
		fmt.Printf("解析字体失败: %v\n", err)
		return
	}

	face, err := opentype.NewFace(fontData, &opentype.FaceOptions{
		Size:    16,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		fmt.Printf("创建字体失败: %v\n", err)
		face = basicfont.Face7x13
	}

	// 绘制文本
	y := 30
	for i, text := range testTexts {
		point := fixed.Point26_6{X: fixed.Int26_6(20 * 64), Y: fixed.Int26_6(y * 64)}
		
		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.RGBA{51, 51, 51, 255}),
			Face: face,
			Dot:  point,
		}
		d.DrawString(text)
		
		y += 25
	}

	// 保存图片
	file, err := os.Create("test_font_output.png")
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		fmt.Printf("编码图片失败: %v\n", err)
		return
	}

	fmt.Println("✅ 测试图片创建成功: test_font_output.png")
	fmt.Println("请查看图片文件，检查中文字符是否正确显示")
}
EOF

echo "2. 编译并运行测试程序..."
cd "$TEST_DIR"
go mod init test_font
go get golang.org/x/image/font/gofont/goregular
go run test_font.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "test_font_output.png" ]; then
	echo "✅ 测试图片生成成功"
	echo "图片大小: $(ls -lh test_font_output.png | awk '{print $5}')"
	echo "请打开 test_font_output.png 查看渲染效果"
else
	echo "❌ 测试图片生成失败"
fi

echo ""
echo "=== 测试完成 ==="
echo "如果中文字符显示正常，说明字体渲染功能正常"
echo "如果显示问号，说明需要进一步优化字体支持" 