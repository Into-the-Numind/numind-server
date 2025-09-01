#!/bin/bash

# 字体加载测试脚本

set -e

echo "=== 字体加载测试 ==="

# 创建测试目录
TEST_DIR="./test_font_loading"
mkdir -p "$TEST_DIR"

echo "1. 创建测试程序..."

# 创建一个简单的Go程序来测试字体加载
cat > "$TEST_DIR/test_font_loading.go" << 'EOF'
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
	"golang.org/x/image/math/fixed"
)

// loadChineseFont 加载支持中文的字体
func loadChineseFont() font.Face {
	// 尝试加载系统字体
	fontPaths := []string{
		"/System/Library/Fonts/STHeiti Light.ttc",      // macOS
		"/System/Library/Fonts/STHeiti Medium.ttc",     // macOS
		"/System/Library/Fonts/AppleSDGothicNeo.ttc",  // macOS
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", // Linux
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", // Linux
		"./fonts/NotoSansCJKsc-Regular.otf",            // 本地字体
		"./fonts/SourceHanSansCN-Regular.otf",          // 本地字体
	}
	
	fmt.Println("尝试加载字体...")
	for i, fontPath := range fontPaths {
		fmt.Printf("  %d. 尝试加载: %s\n", i+1, fontPath)
		if face := loadFontFromPath(fontPath); face != nil {
			fmt.Printf("    ✅ 成功加载字体: %s\n", fontPath)
			return face
		} else {
			fmt.Printf("    ❌ 加载失败: %s\n", fontPath)
		}
	}
	
	fmt.Println("所有字体加载失败，使用基本字体")
	return basicfont.Face7x13
}

// loadFontFromPath 从路径加载字体
func loadFontFromPath(path string) font.Face {
	// 检查文件是否存在
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	
	// 读取字体文件
	fontData, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	
	// 解析字体
	font, err := opentype.Parse(fontData)
	if err != nil {
		return nil
	}
	
	// 创建字体
	face, err := opentype.NewFace(font, &opentype.FaceOptions{
		Size: 14,
		DPI:  72,
	})
	if err != nil {
		return nil
	}
	
	return face
}

func main() {
	// 加载字体
	face := loadChineseFont()
	
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

	// 绘制文本
	y := 30
	for _, text := range testTexts {
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
	file, err := os.Create("test_font_loading_output.png")
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		fmt.Printf("编码图片失败: %v\n", err)
		return
	}

	fmt.Println("✅ 字体加载测试图片创建成功: test_font_loading_output.png")
	fmt.Println("请查看图片文件，检查中文字符是否正确显示")
}
EOF

echo "2. 编译并运行测试程序..."
cd "$TEST_DIR"
go mod init test_font_loading
go run test_font_loading.go

echo ""
echo "3. 检查生成的图片..."
if [ -f "test_font_loading_output.png" ]; then
	echo "✅ 字体加载测试图片生成成功"
	echo "图片大小: $(ls -lh test_font_loading_output.png | awk '{print $5}')"
	echo "请打开 test_font_loading_output.png 查看渲染效果"
else
	echo "❌ 字体加载测试图片生成失败"
fi

echo ""
echo "=== 测试完成 ==="
echo "如果中文字符显示正常，说明字体加载成功"
echo "如果显示问号，说明需要安装支持中文的字体" 