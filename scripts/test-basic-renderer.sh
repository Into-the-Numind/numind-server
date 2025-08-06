#!/bin/bash

# 基础渲染器测试脚本

set -e

echo "=== 基础渲染器测试 ==="

# 创建测试目录
TEST_DIR="./test_basic_renderer"
mkdir -p "$TEST_DIR"

echo "1. 创建测试程序..."

# 创建一个简单的Go程序来测试基础渲染器
cat > "$TEST_DIR/test_basic.go" << 'EOF'
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
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
		"Test Card Rendering",
		"Basic Font Test",
		"English Text Only",
		"Simple Rendering",
		"Working Solution",
	}

	// 绘制文本
	y := 30
	for _, text := range testTexts {
		point := fixed.Point26_6{X: fixed.Int26_6(20 * 64), Y: fixed.Int26_6(y * 64)}
		
		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.RGBA{51, 51, 51, 255}),
			Face: basicfont.Face7x13,
			Dot:  point,
		}
		d.DrawString(text)
		
		y += 25
	}

	// 保存图片
	file, err := os.Create("test_basic_output.png")
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		fmt.Printf("编码图片失败: %v\n", err)
		return
	}

	fmt.Println("✅ 基础渲染器测试图片创建成功: test_basic_output.png")
	fmt.Println("请查看图片文件，检查英文字符是否正确显示")
}
EOF

echo "2. 编译并运行测试程序..."
cd "$TEST_DIR"
go mod init test_basic
go run test_basic.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "test_basic_output.png" ]; then
	echo "✅ 基础渲染器测试图片生成成功"
	echo "图片大小: $(ls -lh test_basic_output.png | awk '{print $5}')"
	echo "请打开 test_basic_output.png 查看渲染效果"
else
	echo "❌ 基础渲染器测试图片生成失败"
fi

echo ""
echo "=== 测试完成 ==="
echo "如果英文字符显示正常，说明基础渲染器工作正常"
echo "这个方案至少能确保英文文本正确显示" 