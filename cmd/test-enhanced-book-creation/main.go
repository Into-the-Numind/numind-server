package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Println("🚀 测试增强版Book创建流程")
	fmt.Println("===============================")

	// 1. 测试封面HTML生成（上下布局）
	fmt.Println("1. 测试增强版封面HTML生成...")
	testCoverHTML()

	// 2. 测试Markdown HTML转换
	fmt.Println("\n2. 测试Markdown HTML转换...")
	testMarkdownHTML()

	// 3. 测试markdown内容分割
	fmt.Println("\n3. 测试markdown内容分割...")
	testMarkdownSplitting()

	fmt.Println("\n🎉 测试完成！")
}

// testCoverHTML 测试封面HTML生成
func testCoverHTML() {
	// 生成封面HTML（模拟CoverRenderer的GenerateCoverHTML方法）
	htmlContent := generateCoverHTML("增强版测试书籍", "", 1080, 1440)

	fmt.Printf("✅ 封面HTML生成成功，长度: %d 字符\n", len(htmlContent))

	// 验证上下布局
	if contains(htmlContent, "flex: 0 0 65%") && contains(htmlContent, "flex: 0 0 35%") {
		fmt.Println("✅ 上下布局验证成功（65%图片区域 + 35%标题区域）")
	} else {
		fmt.Println("❌ 上下布局验证失败")
	}

	// 验证占位符
	if contains(htmlContent, "🖼️") && contains(htmlContent, "封面图片") {
		fmt.Println("✅ 图片占位符验证成功")
	} else {
		fmt.Println("❌ 图片占位符验证失败")
	}
}

// generateCoverHTML 生成封面HTML（简化版）
func generateCoverHTML(title, imageURL string, width, height int) string {
	backgroundStyle := "background: linear-gradient(135deg, #f5f7fa 0%, #c3cfe2 100%);"

	imageHTML := `<div class="image-placeholder">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>`

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>Cover Card</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        html, body {
            width: %dpx;
            height: %dpx;
            margin: 0;
            padding: 0;
            overflow: hidden;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
        }
        
        /* 封面容器 - 上下布局 */
        .cover-container {
            width: 100%%;
            height: 100%%;
            display: flex;
            flex-direction: column;
            %s
            position: relative;
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }
        
        /* 上半部分：图片区域 (65%%) */
        .image-section {
            flex: 0 0 65%%;
            display: flex;
            align-items: center;
            justify-content: center;
            position: relative;
            overflow: hidden;
            width: 100%%;
            background: inherit;
        }
        
        /* 下半部分：标题区域 (35%%) */
        .title-section {
            flex: 0 0 35%%;
            display: flex;
            align-items: center;
            justify-content: center;
            position: relative;
            width: 100%%;
            background: rgba(255, 255, 255, 0.95);
            backdrop-filter: blur(10px);
        }
        
        .title-container {
            text-align: center;
            padding: 30px 40px;
            width: 100%%;
            max-width: 90%%;
        }
        
        .title {
            font-size: 48px;
            font-weight: bold;
            color: #2c3e50;
            line-height: 1.3;
            margin: 0;
            text-shadow: 0 2px 4px rgba(0,0,0,0.1);
            word-wrap: break-word;
            hyphens: auto;
        }
        
        .image-placeholder {
            width: 80%%;
            height: 80%%;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            border-radius: 12px;
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: center;
            color: white;
            font-size: 24px;
            font-weight: bold;
            box-shadow: 0 8px 32px rgba(0,0,0,0.3);
            text-align: center;
        }
        
        .placeholder-icon {
            font-size: 48px;
            margin-bottom: 16px;
            opacity: 0.8;
        }
        
        .placeholder-text {
            font-size: 18px;
            opacity: 0.9;
        }
    </style>
</head>
<body>
    <div class="cover-container">
        <div class="image-section">
            %s
        </div>
        <div class="title-section">
            <div class="title-container">
                <h1 class="title">%s</h1>
            </div>
        </div>
    </div>
</body>
</html>`, width, height, backgroundStyle, imageHTML, title)
}

// testMarkdownHTML 测试Markdown转换
func testMarkdownHTML() {
	markdownText := `# 测试标题

这是一个**测试段落**，包含了*斜体文本*和` + "`代码片段`" + `。

## 二级标题

- 列表项目 1
- 列表项目 2
- 列表项目 3

> 这是一个引用块，用来测试样式。

` + "```go" + `
func main() {
    fmt.Println("Hello, World!")
}
` + "```"

	htmlContent := generateMarkdownCardHTML(markdownText, "测试书籍", 2)

	fmt.Printf("✅ Markdown转换成功，长度: %d 字符\n", len(htmlContent))

	// 验证关键元素
	if contains(htmlContent, "markdown-card-container") {
		fmt.Println("✅ Markdown卡片容器验证成功")
	}

	if contains(htmlContent, "第 2 页") {
		fmt.Println("✅ 页码显示验证成功")
	}

	if contains(htmlContent, "测试标题") {
		fmt.Println("✅ 标题内容验证成功")
	}

	if contains(htmlContent, "列表项目") {
		fmt.Println("✅ 列表内容验证成功")
	}

	if contains(htmlContent, "引用块") {
		fmt.Println("✅ 引用块内容验证成功")
	}

	if contains(htmlContent, "Hello, World!") {
		fmt.Println("✅ 代码块内容验证成功")
	}
}

// generateMarkdownCardHTML 生成Markdown卡片HTML（简化版）
func generateMarkdownCardHTML(markdown, title string, cardIndex int) string {
	// 简单的markdown转HTML（实际项目中会使用goldmark）
	content := strings.ReplaceAll(markdown, "\n", "<br>")
	content = strings.ReplaceAll(content, "**", "<strong>")
	content = strings.ReplaceAll(content, "*", "<em>")
	content = strings.ReplaceAll(content, "`", "<code>")

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s - 第%d页</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: 'Noto Sans SC', 'PingFang SC', 'Microsoft YaHei', sans-serif;
            font-size: 16px;
            line-height: 1.6;
            color: #333333;
            background-color: #ffffff;
            overflow: visible;
        }

        .markdown-card-container {
            width: 1080px;
            min-height: 1440px;
            padding: 40px;
            overflow: visible;
            background-color: #ffffff;
            position: relative;
        }

        .markdown-content {
            width: 100%%;
            height: auto;
            overflow: visible;
            padding-bottom: 50px;
        }

        /* 页脚样式 */
        .card-footer {
            position: absolute;
            bottom: 15px;
            right: 25px;
            font-size: 12px;
            color: #7f8c8d;
            opacity: 0.8;
            background: rgba(255, 255, 255, 0.9);
            padding: 4px 8px;
            border-radius: 4px;
            border: 1px solid #ddd;
        }
    </style>
</head>
<body>
    <div class="markdown-card-container">
        <div class="markdown-content">
            %s
        </div>
        <div class="card-footer">
            <span class="page-number">第 %d 页</span>
        </div>
    </div>
</body>
</html>`, title, cardIndex, content, cardIndex)
}

// testMarkdownSplitting 测试markdown分割
func testMarkdownSplitting() {
	longMarkdown := `# 第一章：介绍

这是第一章的内容，介绍了项目的背景和目标。

## 1.1 项目背景

在现代软件开发中，文档管理是一个重要的环节。我们需要一个高效的系统来处理和展示内容。

## 1.2 技术选型

我们选择了Go语言作为后端开发语言，主要考虑因素包括：

- 高性能
- 并发支持
- 简洁的语法
- 丰富的标准库

# 第二章：架构设计

本章介绍系统的整体架构设计思路。

## 2.1 系统架构

系统采用微服务架构，主要包括以下组件：

1. API网关
2. 用户服务
3. 内容服务
4. 渲染服务

每个服务都有明确的职责边界，通过HTTP/gRPC进行通信。

## 2.2 数据存储

我们使用MySQL作为主数据库，Redis作为缓存层。

# 第三章：实现细节

这一章详细介绍各个模块的实现。

## 3.1 用户管理

用户管理模块负责处理用户注册、登录、权限验证等功能。

## 3.2 内容处理

内容处理模块是系统的核心，负责：

- Markdown解析
- HTML生成
- 图片处理
- 分页算法

这些功能确保了内容能够正确地转换和展示。`

	cards := splitMarkdownIntoCards(longMarkdown)

	fmt.Printf("✅ Markdown分割成功，生成 %d 张卡片\n", len(cards))

	for i, card := range cards {
		if i < 3 { // 只显示前3张卡片的预览
			preview := card
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("  卡片 %d: %s\n", i+1, preview)
		}
	}

	if len(cards) >= 3 {
		fmt.Println("✅ 内容分割验证成功（生成了多张卡片）")
	} else {
		fmt.Println("❌ 内容分割可能有问题（卡片数量过少）")
	}
}

// splitMarkdownIntoCards 分割markdown内容为多张卡片
func splitMarkdownIntoCards(content string) []string {
	lines := strings.Split(content, "\n")
	var cards []string
	var currentCard strings.Builder

	const maxCardLength = 1000 // 每张卡片最大字符数

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 检查是否是一级标题（新卡片的开始）
		if strings.HasPrefix(line, "# ") && currentCard.Len() > 0 {
			// 保存当前卡片
			cards = append(cards, strings.TrimSpace(currentCard.String()))
			currentCard.Reset()
		}

		// 检查是否是二级标题（可能的新卡片开始）
		if strings.HasPrefix(line, "## ") && currentCard.Len() > maxCardLength {
			// 如果当前卡片已经很长，开始新卡片
			cards = append(cards, strings.TrimSpace(currentCard.String()))
			currentCard.Reset()
		}

		// 添加当前行到卡片
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n")
		}
		currentCard.WriteString(line)

		// 如果当前卡片过长，在合适的地方分割
		if currentCard.Len() > maxCardLength*1.5 {
			cards = append(cards, strings.TrimSpace(currentCard.String()))
			currentCard.Reset()
		}
	}

	// 添加最后一张卡片
	if currentCard.Len() > 0 {
		cards = append(cards, strings.TrimSpace(currentCard.String()))
	}

	return cards
}

// contains 检查字符串是否包含子字符串
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
