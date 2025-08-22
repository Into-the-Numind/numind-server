package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"numind-server/internal/numind/biz/markdown"
	"numind-server/pkg/util"
)

func main() {
	fmt.Println("🧪 测试卡片渲染功能")
	fmt.Println("====================")

	// 创建HTML转换器
	htmlConverter := markdown.NewHTMLConverter()

	// 测试Markdown内容
	markdownContent := `# 人工智能简介

人工智能（Artificial Intelligence，AI）是计算机科学的一个分支，它企图了解智能的实质，并生产出一种新的能以人类智能相似的方式做出反应的智能机器。

## 主要特点

- **学习能力**：能够从数据中学习并改进
- **推理能力**：能够进行逻辑推理和问题解决
- **感知能力**：能够理解和处理自然语言、图像等

## 应用领域

1. **机器学习**：通过算法让计算机自动学习
2. **自然语言处理**：理解和生成人类语言
3. **计算机视觉**：识别和理解图像内容
4. **机器人技术**：实现物理世界的智能操作

> 人工智能的发展正在改变我们的生活方式，从智能手机助手到自动驾驶汽车，AI技术无处不在。

` + "```" + `python
# 简单的AI示例
def simple_ai_decision(input_data):
    if input_data > 0.5:
        return "正面决策"
    else:
        return "负面决策"
` + "```" + `

### 未来展望

随着技术的不断进步，人工智能将在更多领域发挥重要作用，为人类创造更美好的未来。`

	// 转换为HTML
	htmlContent := htmlConverter.ConvertMarkdownCardToHTML(markdownContent, "AI简介", 1)

	fmt.Printf("✅ HTML转换成功，长度: %d 字符\n", len(htmlContent))

	// 创建输出目录
	outputDir := "./test_card_output"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ 创建输出目录失败: %v\n", err)
		return
	}

	// 测试不同的配置
	testConfigs := []struct {
		name   string
		config *util.WkhtmltoimageConfig
	}{
		{
			name: "标准卡片配置",
			config: &util.WkhtmltoimageConfig{
				Width:   1080,
				Height:  1440,
				Quality: 85,
				Format:  "webp",
				Zoom:    1.0,
				Timeout: 30,
			},
		},
		{
			name: "高质量配置",
			config: &util.WkhtmltoimageConfig{
				Width:   1080,
				Height:  1440,
				Quality: 95,
				Format:  "webp",
				Zoom:    1.0,
				Timeout: 30,
			},
		},
		{
			name: "PNG格式配置",
			config: &util.WkhtmltoimageConfig{
				Width:   1080,
				Height:  1440,
				Quality: 85,
				Format:  "png",
				Zoom:    1.0,
				Timeout: 30,
			},
		},
	}

	for i, testConfig := range testConfigs {
		fmt.Printf("\n🔄 测试 %s...\n", testConfig.name)

		// 创建渲染器
		renderer := util.NewWkhtmltoimageRenderer(testConfig.config)

		// 生成输出文件名
		outputPath := filepath.Join(outputDir, fmt.Sprintf("card_test_%d.%s", i+1, testConfig.config.Format))

		// 渲染图片
		ctx := context.Background()
		if err := renderer.RenderHTMLToImage(ctx, htmlContent, outputPath); err != nil {
			fmt.Printf("❌ 渲染失败: %v\n", err)
			continue
		}

		// 检查文件是否生成
		if fileInfo, err := os.Stat(outputPath); err != nil {
			fmt.Printf("❌ 文件未生成: %v\n", err)
		} else {
			fmt.Printf("✅ 渲染成功: %s (%.2f KB)\n", outputPath, float64(fileInfo.Size())/1024)
		}
	}

	// 测试字节数组输出
	fmt.Printf("\n🔄 测试字节数组输出...\n")
	renderer := util.NewWkhtmltoimageRenderer(&util.WkhtmltoimageConfig{
		Width:   1080,
		Height:  1440,
		Quality: 85,
		Format:  "webp",
		Zoom:    1.0,
		Timeout: 30,
	})

	bytes, err := renderer.RenderHTMLToBytes(context.Background(), htmlContent)
	if err != nil {
		fmt.Printf("❌ 字节数组输出失败: %v\n", err)
	} else {
		fmt.Printf("✅ 字节数组输出成功: %d bytes\n", len(bytes))
	}

	fmt.Printf("\n🎉 卡片渲染测试完成！输出文件保存在: %s\n", outputDir)
}
