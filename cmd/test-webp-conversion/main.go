package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"time"
)

func main() {
	fmt.Println("🧪 开始测试WebP转换功能...")

	// 创建一个简单的测试图片
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))

	// 填充红色背景
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}

	// 创建临时PNG文件
	tempPNG := fmt.Sprintf("/tmp/test_%d.png", time.Now().UnixNano())
	tempFile, err := os.Create(tempPNG)
	if err != nil {
		fmt.Printf("❌ 创建临时PNG文件失败: %v\n", err)
		return
	}

	// 编码为PNG
	if err := png.Encode(tempFile, img); err != nil {
		tempFile.Close()
		fmt.Printf("❌ PNG编码失败: %v\n", err)
		return
	}
	tempFile.Close()

	// 验证临时PNG文件
	if info, err := os.Stat(tempPNG); err != nil {
		fmt.Printf("❌ 临时PNG文件验证失败: %v\n", err)
		return
	} else {
		fmt.Printf("✅ 临时PNG文件创建成功，大小: %d bytes\n", info.Size())
	}

	// 转换为WebP
	outputWebP := "/tmp/test_output.webp"
	cmd := exec.Command("cwebp", "-q", "95", "-m", "6", "-af", "-f", "50", "-sharpness", "0", tempPNG, "-o", outputWebP)

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("❌ WebP转换失败: %v\n", err)
		fmt.Printf("命令输出: %s\n", string(output))
		os.Remove(tempPNG)
		return
	}

	// 验证输出WebP文件
	if info, err := os.Stat(outputWebP); err != nil {
		fmt.Printf("❌ 输出WebP文件验证失败: %v\n", err)
	} else {
		fmt.Printf("✅ WebP转换成功，文件大小: %d bytes\n", info.Size())
	}

	// 清理临时文件
	os.Remove(tempPNG)
	os.Remove(outputWebP)

	fmt.Println("🎉 WebP转换测试完成！")
}
