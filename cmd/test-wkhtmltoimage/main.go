package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"numind-server/pkg/util"
)

func main() {
	fmt.Println("🧪 测试 wkhtmltoimage 工具")
	fmt.Println("==========================")

	// 创建渲染器
	config := &util.WkhtmltoimageConfig{
		Width:   1080,
		Height:  1440,
		Quality: 85,
		Format:  "webp",
		Zoom:    1.0,
		Timeout: 30,
	}

	renderer := util.NewWkhtmltoimageRenderer(config)

	// 检查可用性
	fmt.Printf("✅ 渲染器可用性: %v\n", renderer.IsAvailable())
	fmt.Printf("📋 版本信息: %s\n", renderer.GetVersion())

	// 测试HTML内容
	htmlContent := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>测试页面</title>
    <style>
        body {
            font-family: 'Microsoft YaHei', Arial, sans-serif;
            margin: 0;
            padding: 40px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            text-align: center;
        }
        .container {
            max-width: 800px;
            margin: 0 auto;
            background: rgba(255, 255, 255, 0.1);
            border-radius: 20px;
            padding: 40px;
            backdrop-filter: blur(10px);
        }
        h1 {
            font-size: 48px;
            margin-bottom: 20px;
            text-shadow: 0 2px 4px rgba(0,0,0,0.3);
        }
        p {
            font-size: 24px;
            line-height: 1.6;
            margin-bottom: 30px;
        }
        .feature {
            background: rgba(255, 255, 255, 0.2);
            padding: 20px;
            border-radius: 10px;
            margin: 20px 0;
        }
        .emoji {
            font-size: 32px;
            margin-bottom: 10px;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎉 wkhtmltoimage 测试</h1>
        <p>这是一个测试页面，用于验证 HTML 到图片的转换功能。</p>
        
        <div class="feature">
            <div class="emoji">🚀</div>
            <h3>高性能渲染</h3>
            <p>使用纯 Go 实现，无需外部依赖</p>
        </div>
        
        <div class="feature">
            <div class="emoji">🎨</div>
            <h3>丰富样式</h3>
            <p>支持 CSS3、渐变、阴影等现代样式</p>
        </div>
        
        <div class="feature">
            <div class="emoji">📱</div>
            <h3>响应式设计</h3>
            <p>适配不同尺寸和分辨率</p>
        </div>
        
        <p>生成时间: 2025年1月</p>
    </div>
</body>
</html>`

	// 创建输出目录
	outputDir := "./test_output"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ 创建输出目录失败: %v\n", err)
		return
	}

	// 测试不同的输出格式
	formats := []string{"webp", "png"}

	for _, format := range formats {
		outputPath := filepath.Join(outputDir, fmt.Sprintf("test_output.%s", format))

		fmt.Printf("\n🔄 测试 %s 格式...\n", strings.ToUpper(format))

		// 更新配置
		config.Format = format
		renderer.SetConfig(config)

		// 渲染图片
		ctx := context.Background()
		if err := renderer.RenderHTMLToImage(ctx, htmlContent, outputPath); err != nil {
			fmt.Printf("❌ %s 格式渲染失败: %v\n", strings.ToUpper(format), err)
			continue
		}

		// 检查文件是否生成
		if _, err := os.Stat(outputPath); err != nil {
			fmt.Printf("❌ %s 文件未生成: %v\n", strings.ToUpper(format), err)
			continue
		}

		// 获取文件大小
		if fileInfo, err := os.Stat(outputPath); err == nil {
			fmt.Printf("✅ %s 格式渲染成功: %s (%.2f KB)\n",
				strings.ToUpper(format), outputPath, float64(fileInfo.Size())/1024)
		} else {
			fmt.Printf("✅ %s 格式渲染成功: %s\n", strings.ToUpper(format), outputPath)
		}
	}

	// 测试字节数组输出
	fmt.Printf("\n🔄 测试字节数组输出...\n")
	bytes, err := renderer.RenderHTMLToBytes(context.Background(), htmlContent)
	if err != nil {
		fmt.Printf("❌ 字节数组输出失败: %v\n", err)
	} else {
		fmt.Printf("✅ 字节数组输出成功: %d bytes\n", len(bytes))
	}

	// 测试Reader输出
	fmt.Printf("\n🔄 测试Reader输出...\n")
	reader, err := renderer.RenderHTMLToReader(context.Background(), htmlContent)
	if err != nil {
		fmt.Printf("❌ Reader输出失败: %v\n", err)
	} else {
		fmt.Printf("✅ Reader输出成功\n")
		_ = reader // 避免未使用变量警告
	}

	fmt.Printf("\n🎉 测试完成！输出文件保存在: %s\n", outputDir)
}
