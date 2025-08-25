package markdown

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
	"math"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"

	"numind-server/internal/pkg/log"
)

// HTMLConverter Markdown 到 HTML 转换器
type HTMLConverter struct {
	markdown goldmark.Markdown
	config   *HTMLConfig
}

// HTMLConfig HTML 转换配置
type HTMLConfig struct {
	EnableTables        bool    `json:"enable_tables"`        // 启用表格支持
	EnableStrikethrough bool    `json:"enable_strikethrough"` // 启用删除线
	EnableTaskList      bool    `json:"enable_task_list"`     // 启用任务列表
	UnsafeHTML          bool    `json:"unsafe_html"`          // 允许不安全的HTML
	XHTML               bool    `json:"xhtml"`                // 使用XHTML格式
	CardWidth           int     `json:"card_width"`           // 卡片宽度
	CardHeight          int     `json:"card_height"`          // 卡片高度
	FontFamily          string  `json:"font_family"`          // 字体系列
	FontSize            int     `json:"font_size"`            // 字体大小
	LineHeight          float64 `json:"line_height"`          // 行高
	Padding             int     `json:"padding"`              // 内边距
	BackgroundColor     string  `json:"background_color"`     // 背景色
	TextColor           string  `json:"text_color"`           // 文字颜色
}

// MarkdownContentBlock Markdown 内容块
type MarkdownContentBlock struct {
	Type    string      `json:"type"`     // 块类型
	Content interface{} `json:"content"`  // 内容
	Level   int         `json:"level"`    // 层级（标题用）
	RawText string      `json:"raw_text"` // 原始文本
}

// NewHTMLConverter 创建新的HTML转换器
func NewHTMLConverter() *HTMLConverter {
	config := &HTMLConfig{
		EnableTables:        true,
		EnableStrikethrough: true,
		EnableTaskList:      true,
		UnsafeHTML:          false,
		XHTML:               false,
		CardWidth:           1080,
		CardHeight:          1440,
		FontFamily:          "'Noto Sans SC', 'PingFang SC', 'Microsoft YaHei', sans-serif",
		FontSize:            16,
		LineHeight:          1.6,
		Padding:             40,
		BackgroundColor:     "#ffffff",
		TextColor:           "#333333",
	}

	// 配置 Goldmark 选项
	extensions := []goldmark.Extender{}

	if config.EnableTables {
		extensions = append(extensions, extension.Table)
	}
	if config.EnableStrikethrough {
		extensions = append(extensions, extension.Strikethrough)
	}
	if config.EnableTaskList {
		extensions = append(extensions, extension.TaskList)
	}

	md := goldmark.New(
		goldmark.WithExtensions(extensions...),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
		),
	)

	return &HTMLConverter{
		markdown: md,
		config:   config,
	}
}

// ConvertToHTML 将 Markdown 转换为 HTML
func (hc *HTMLConverter) ConvertToHTML(markdownText string) (string, error) {
	var buf bytes.Buffer
	if err := hc.markdown.Convert([]byte(markdownText), &buf); err != nil {
		return "", fmt.Errorf("failed to convert markdown to HTML: %v", err)
	}
	return buf.String(), nil
}

// ConvertToStyledHTML 将 Markdown 转换为带样式的完整 HTML 页面
func (hc *HTMLConverter) ConvertToStyledHTML(markdownText, title string) (string, error) {
	// 先转换为基础 HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		return "", err
	}

	// 包装为完整的 HTML 页面
	return hc.wrapWithStyles(contentHTML, title, false), nil
}

// ConvertToStyledHTMLWithFixedMargins 将 Markdown 转换为带固定边距的完整 HTML 页面
func (hc *HTMLConverter) ConvertToStyledHTMLWithFixedMargins(markdownText, title string) (string, error) {
	// 先转换为基础 HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		return "", err
	}

	// 包装为带固定边距的完整 HTML 页面
	return hc.wrapWithFixedMarginStyles(contentHTML, title), nil
}

// ConvertBlocksToHTML 将 Markdown 内容块转换为 HTML
func (hc *HTMLConverter) ConvertBlocksToHTML(blocks []MarkdownContentBlock) (string, error) {
	log.C(context.Background()).Infow("开始转换内容块为HTML",
		"blocks_count", len(blocks))

	var htmlParts []string

	for i, block := range blocks {
		log.C(context.Background()).Debugw("转换内容块",
			"block_index", i,
			"block_type", block.Type,
			"block_level", block.Level,
			"content_length", len(fmt.Sprintf("%v", block.Content)))

		html, err := hc.convertBlockToHTML(block)
		if err != nil {
			log.C(context.Background()).Errorw("转换内容块失败",
				"block_index", i,
				"block_type", block.Type,
				"error", err)
			return "", fmt.Errorf("failed to convert block to HTML: %v", err)
		}
		if html != "" {
			htmlParts = append(htmlParts, html)
			log.C(context.Background()).Debugw("内容块转换成功",
				"block_index", i,
				"html_length", len(html))
		}
	}

	result := strings.Join(htmlParts, "\n")
	log.C(context.Background()).Infow("HTML转换完成",
		"total_blocks", len(blocks),
		"html_parts", len(htmlParts),
		"result_length", len(result))

	return result, nil
}

// convertBlockToHTML 转换单个内容块为 HTML
func (hc *HTMLConverter) convertBlockToHTML(block MarkdownContentBlock) (string, error) {
	switch block.Type {
	case "heading":
		level := block.Level
		if level < 1 || level > 6 {
			level = 1
		}
		content := fmt.Sprintf("%v", block.Content)
		return fmt.Sprintf("<h%d>%s</h%d>", level, hc.escapeHTML(content), level), nil

	case "paragraph":
		content := fmt.Sprintf("%v", block.Content)
		return fmt.Sprintf("<p>%s</p>", hc.escapeHTML(content)), nil

	case "image":
		// 处理图片块，生成封面卡片结构
		imageURL := fmt.Sprintf("%v", block.Content)
		return hc.generateCoverCardHTML(imageURL), nil

	case "list":
		if items, ok := block.Content.([]string); ok {
			var listItems []string
			for _, item := range items {
				listItems = append(listItems, fmt.Sprintf("<li>%s</li>", hc.escapeHTML(item)))
			}
			return fmt.Sprintf("<ul>%s</ul>", strings.Join(listItems, "")), nil
		}

	case "quote":
		content := fmt.Sprintf("%v", block.Content)
		return fmt.Sprintf("<blockquote><p>%s</p></blockquote>", hc.escapeHTML(content)), nil

	case "table":
		if rows, ok := block.Content.([][]string); ok {
			return hc.convertTableToHTML(rows), nil
		}

	case "code":
		if codeData, ok := block.Content.(map[string]interface{}); ok {
			language := ""
			code := ""
			if lang, exists := codeData["language"].(string); exists {
				language = lang
			}
			if codeStr, exists := codeData["code"].(string); exists {
				code = codeStr
			}

			if language != "" {
				return fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>",
					hc.escapeHTML(language), hc.escapeHTML(code)), nil
			} else {
				return fmt.Sprintf("<pre><code>%s</code></pre>", hc.escapeHTML(code)), nil
			}
		}
	}

	// 如果无法识别类型，使用原始文本
	return fmt.Sprintf("<p>%s</p>", hc.escapeHTML(block.RawText)), nil
}

// generateCoverCardHTML 生成封面卡片的HTML结构
func (hc *HTMLConverter) generateCoverCardHTML(title string) string {
	// 封面卡片布局：左边标题，右边图片
	return fmt.Sprintf(`
<div class="cover-card-container">
    <div class="cover-title-section">
        <h1 class="cover-title">%s</h1>
    </div>
    <div class="cover-image-section">
        <div class="cover-image-placeholder">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>
    </div>
</div>`, title)
}

// convertTableToHTML 转换表格为 HTML
func (hc *HTMLConverter) convertTableToHTML(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}

	var html strings.Builder
	html.WriteString("<table>")

	// 处理表头
	if len(rows) > 0 {
		html.WriteString("<thead><tr>")
		for _, cell := range rows[0] {
			html.WriteString(fmt.Sprintf("<th>%s</th>", hc.escapeHTML(cell)))
		}
		html.WriteString("</tr></thead>")
	}

	// 处理表体
	if len(rows) > 1 {
		html.WriteString("<tbody>")
		for _, row := range rows[1:] {
			html.WriteString("<tr>")
			for _, cell := range row {
				html.WriteString(fmt.Sprintf("<td>%s</td>", hc.escapeHTML(cell)))
			}
			html.WriteString("</tr>")
		}
		html.WriteString("</tbody>")
	}

	html.WriteString("</table>")
	return html.String()
}

// escapeHTML HTML 转义
func (hc *HTMLConverter) escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// ConvertCardBlocksToHTML 将卡片内容块转换为HTML
func (hc *HTMLConverter) ConvertCardBlocksToHTML(blocks []MarkdownContentBlock, title string, isCoverCard bool) string {
	log.C(context.Background()).Infow("开始转换卡片内容块为HTML", "blocks_count", len(blocks), "title", title, "is_cover_card", isCoverCard)

	// 如果是封面卡片，使用特殊的封面布局
	if isCoverCard {
		return hc.generateCoverCardHTML(title)
	}

	// 普通内容卡片
	var contentHTML strings.Builder
	for _, block := range blocks {
		html, err := hc.convertBlockToHTML(block)
		if err != nil {
			log.C(context.Background()).Warnw("转换内容块失败", "block_type", block.Type, "error", err)
			continue
		}
		contentHTML.WriteString(html)
	}

	fullHTML := hc.wrapWithStyles(contentHTML.String(), title, false) // 普通卡片
	log.C(context.Background()).Infow("卡片HTML转换完成", "content_html_length", len(contentHTML.String()), "full_html_length", len(fullHTML), "is_cover_card", isCoverCard, "content_preview", truncateString(contentHTML.String(), 200), "full_preview", truncateString(fullHTML, 300))

	return fullHTML
}

// isCoverCard 检查是否是封面卡片
func (hc *HTMLConverter) isCoverCard(blocks []MarkdownContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "image" {
			return true
		}
	}
	return false
}

// wrapWithStyles 包装 HTML 内容并添加样式
func (hc *HTMLConverter) wrapWithStyles(contentHTML, title string, isCoverCard bool) string {
	cssStyles := hc.generateCSS(isCoverCard)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>%s</style>
</head>
<body>
    <div class="card-container">
        %s
    </div>
</body>
</html>`, hc.escapeHTML(title), cssStyles, contentHTML)
}

// ConvertMarkdownCardToHTML 将Markdown内容转换为卡片HTML
func (hc *HTMLConverter) ConvertMarkdownCardToHTML(markdownText, title string, cardIndex int) string {
	// 转换markdown为HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		contentHTML = fmt.Sprintf("<p>%s</p>", hc.escapeHTML(markdownText))
	}

	// 包装为卡片HTML
	return hc.wrapWithMarkdownCardStyles(contentHTML, title, cardIndex)
}

// wrapWithMarkdownCardStyles 为Markdown卡片包装样式
func (hc *HTMLConverter) wrapWithMarkdownCardStyles(contentHTML, title string, cardIndex int) string {
	cssStyles := hc.generateMarkdownCardCSS()

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s - 第%d页</title>
    <style>%s</style>
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
</html>`, hc.escapeHTML(title), cardIndex, cssStyles, contentHTML, cardIndex)
}

// generateCSS 生成 CSS 样式
func (hc *HTMLConverter) generateCSS(isCoverCard bool) string {
	baseCSS := fmt.Sprintf(`
* {
    margin: 0;
    padding: 0;
    box-sizing: border-box;
}

body {
    font-family: %s;
    font-size: %dpx;
    line-height: %.1f;
    color: %s;
    background-color: %s;
    overflow: visible;
}

.card-container {
    width: %dpx;
    height: %dpx;
    padding: %dpx;
    overflow: visible;
    background-color: %s;
    position: relative;
}`,
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		hc.config.BackgroundColor,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.Padding,
		hc.config.BackgroundColor,
	)

	if isCoverCard {
		// 封面卡片特殊样式
		coverCSS := `
/* 封面卡片样式 */
.card-container {
    display: flex;
    flex-direction: row;
    background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
    color: white;
    padding: 0;
}

.cover-card-container {
    width: 100%;
    height: 100%;
    display: flex;
    flex-direction: row;
}

.cover-title-section {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 40px;
    background: rgba(255, 255, 255, 0.1);
    backdrop-filter: blur(10px);
    border-right: 1px solid rgba(255, 255, 255, 0.2);
}

.cover-title {
    font-size: 48px;
    font-weight: bold;
    text-align: center;
    text-shadow: 0 2px 8px rgba(0,0,0,0.5);
    line-height: 1.2;
    margin: 0;
}

.cover-image-section {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 40px;
    position: relative;
}

.cover-image {
    max-width: 100%;
    max-height: 100%;
    border-radius: 12px;
    box-shadow: 0 12px 40px rgba(0,0,0,0.4);
    object-fit: cover;
}

.cover-image-placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    background: rgba(255, 255, 255, 0.1);
    border-radius: 12px;
    border: 2px dashed rgba(255, 255, 255, 0.3);
}

.placeholder-icon {
    font-size: 48px;
    margin-bottom: 16px;
    opacity: 0.7;
}

.placeholder-text {
    font-size: 18px;
    color: rgba(255, 255, 255, 0.8);
    text-align: center;
}

/* 隐藏封面卡片中的其他内容 */
.card-container > *:not(.cover-card-container) {
    display: none;
}`
		return baseCSS + coverCSS
	}

	// 普通卡片样式
	normalCSS := fmt.Sprintf(`
/* 标题样式 */
h1 {
    font-size: 24px;
    font-weight: bold;
    margin-bottom: 16px;
    color: %s;
    text-align: center;
}

h2 {
    font-size: 20px;
    font-weight: bold;
    margin: 16px 0 12px 0;
    color: %s;
}

h3 {
    font-size: 18px;
    font-weight: bold;
    margin: 14px 0 10px 0;
    color: %s;
}

h4, h5, h6 {
    font-size: 16px;
    font-weight: bold;
    margin: 12px 0 8px 0;
    color: %s;
}

/* 段落样式 */
p {
    margin-bottom: 12px;
    text-align: justify;
    text-justify: inter-ideograph;
    word-wrap: break-word;
    hyphens: auto;
}

/* 列表样式 */
ul, ol {
    margin: 12px 0;
    padding-left: 24px;
}

li {
    margin-bottom: 6px;
    word-wrap: break-word;
}

/* 引用样式 */
blockquote {
    margin: 16px 0;
    padding: 12px 16px;
    border-left: 4px solid #e0e0e0;
    background-color: #f9f9f9;
    font-style: italic;
}

blockquote p {
    margin-bottom: 0;
}

/* 代码样式 */
code {
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: 14px;
    background-color: #f5f5f5;
    padding: 2px 4px;
    border-radius: 3px;
}

pre {
    margin: 16px 0;
    padding: 12px;
    background-color: #f5f5f5;
    border-radius: 4px;
    overflow: hidden;
    word-wrap: break-word;
    white-space: pre-wrap;
}

pre code {
    background-color: transparent;
    padding: 0;
}

/* 表格样式 */
table {
    width: 100%%;
    margin: 16px 0;
    border-collapse: collapse;
    font-size: 14px;
}

th, td {
    padding: 8px 12px;
    text-align: left;
    border: 1px solid #e0e0e0;
    word-wrap: break-word;
}

th {
    background-color: #f5f5f5;
    font-weight: bold;
}

/* 响应式调整 */
@media print {
    .card-container {
        width: 100%%;
        height: auto;
        padding: 20px;
    }
}`,
		hc.config.TextColor,
		hc.config.TextColor,
		hc.config.TextColor,
		hc.config.TextColor,
	)

	return baseCSS + normalCSS
}

// UpdateConfig 更新配置
func (hc *HTMLConverter) UpdateConfig(config *HTMLConfig) {
	hc.config = config
}

// GetConfig 获取当前配置
func (hc *HTMLConverter) GetConfig() *HTMLConfig {
	return hc.config
}

// generateMarkdownCardCSS 生成Markdown卡片专用CSS
func (hc *HTMLConverter) generateMarkdownCardCSS() string {
	return fmt.Sprintf(`
* {
    margin: 0;
    padding: 0;
    box-sizing: border-box;
}

body {
    font-family: %s;
    font-size: %dpx;
    line-height: %.1f;
    color: %s;
    background-color: %s;
    overflow: visible;
}

.markdown-card-container {
    width: %dpx;
    min-height: %dpx;
    padding: %dpx;
    overflow: visible;
    background-color: %s;
    position: relative;
}

.markdown-content {
    width: 100%%;
    height: auto;
    overflow: visible;
}

/* Markdown元素样式 */
h1, h2, h3, h4, h5, h6 {
    margin-top: 24px;
    margin-bottom: 16px;
    font-weight: bold;
    line-height: 1.4;
    word-wrap: break-word;
}

h1 { 
    font-size: 28px; 
    color: #2c3e50; 
    border-bottom: 2px solid #3498db;
    padding-bottom: 8px;
}

h2 { 
    font-size: 24px; 
    color: #34495e; 
    border-bottom: 1px solid #bdc3c7;
    padding-bottom: 6px;
}

h3 { 
    font-size: 20px; 
    color: #34495e; 
}

h4 { 
    font-size: 18px; 
    color: #34495e; 
}

h5 { 
    font-size: 16px; 
    color: #34495e; 
}

h6 { 
    font-size: 14px; 
    color: #34495e; 
}

p {
    margin-bottom: 16px;
    text-align: justify;
    word-wrap: break-word;
    hyphens: auto;
}

/* 列表样式 */
ul, ol {
    margin: 16px 0;
    padding-left: 30px;
}

li {
    margin-bottom: 8px;
    line-height: 1.6;
}

ul li::marker {
    color: #3498db;
}

/* 引用块样式 */
blockquote {
    margin: 20px 0;
    padding: 16px 20px;
    border-left: 4px solid #3498db;
    background: #ecf0f1;
    font-style: italic;
    border-radius: 0 6px 6px 0;
}

blockquote p {
    margin-bottom: 0;
    color: #2c3e50;
}

/* 代码样式 */
code {
    background: #f8f9fa;
    padding: 2px 6px;
    border-radius: 3px;
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: 14px;
    color: #e74c3c;
    border: 1px solid #e9ecef;
}

pre {
    background: #2c3e50;
    color: #ecf0f1;
    border-radius: 6px;
    padding: 16px;
    overflow-x: auto;
    margin: 20px 0;
    box-shadow: 0 2px 8px rgba(0,0,0,0.1);
}

pre code {
    background: transparent;
    padding: 0;
    border: none;
    color: inherit;
}

/* 表格样式 */
table {
    width: 100%%;
    border-collapse: collapse;
    margin: 20px 0;
    font-size: 14px;
    box-shadow: 0 2px 8px rgba(0,0,0,0.1);
    border-radius: 6px;
    overflow: hidden;
}

th, td {
    padding: 12px 16px;
    text-align: left;
    border-bottom: 1px solid #ddd;
    word-wrap: break-word;
}

th {
    background: #3498db;
    color: white;
    font-weight: bold;
}

tr:nth-child(even) {
    background: #f8f9fa;
}

tr:hover {
    background: #e8f4fd;
}

/* 链接样式 */
a {
    color: #3498db;
    text-decoration: none;
    border-bottom: 1px dotted #3498db;
}

a:hover {
    background: #e8f4fd;
    text-decoration: underline;
}

/* 强调文本 */
strong {
    color: #2c3e50;
    font-weight: bold;
}

em {
    color: #34495e;
    font-style: italic;
}

/* 删除线 */
del {
    color: #7f8c8d;
    text-decoration: line-through;
}

/* 分割线 */
hr {
    margin: 30px 0;
    border: none;
    border-top: 2px solid #bdc3c7;
    border-radius: 1px;
}

/* 任务列表 */
input[type="checkbox"] {
    margin-right: 8px;
    transform: scale(1.2);
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

/* 确保内容不与页脚重叠 */
.markdown-content {
    padding-bottom: 50px;
}

/* 防止文本溢出 */
.markdown-card-container {
    word-wrap: break-word;
    overflow-wrap: break-word;
    hyphens: auto;
}
`,
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		hc.config.BackgroundColor,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.Padding,
		hc.config.BackgroundColor,
	)
}

// wrapWithFixedMarginStyles 包装 HTML 内容并添加固定边距样式
func (hc *HTMLConverter) wrapWithFixedMarginStyles(contentHTML, title string) string {
	cssStyles := hc.generateFixedMarginCSS()

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>%s</style>
</head>
<body>
    <div class="card-container">
        %s
    </div>
</body>
</html>`, hc.escapeHTML(title), cssStyles, contentHTML)
}

// generateFixedMarginCSS 生成带固定边距的 CSS 样式
func (hc *HTMLConverter) generateFixedMarginCSS() string {
	const fixedMargin = 20 // 固定上下边距为20px
	
	return fmt.Sprintf(`
* {
    margin: 0;
    padding: 0;
    box-sizing: border-box;
}

body {
    font-family: %s;
    font-size: %dpx;
    line-height: %.1f;
    color: %s;
    background-color: %s;
    overflow: visible;
}

.card-container {
    width: %dpx;
    height: %dpx;
    padding: %dpx;
    overflow: visible;
    background-color: %s;
    position: relative;
    /* 强制固定上下边距 */
    margin-top: %dpx !important;
    margin-bottom: %dpx !important;
}

/* 内容区域样式 */
.card-content {
    /* 内容区域高度 = 卡片高度 - 上下边距 - 内边距 */
    height: calc(%dpx - %dpx - %dpx);
    overflow: hidden;
    position: relative;
}

/* 标题样式 */
h1 {
    font-size: 28px;
    font-weight: bold;
    margin: 0 0 16px 0;
    color: %s;
    line-height: 1.4;
}

h2 {
    font-size: 24px;
    font-weight: bold;
    margin: 24px 0 16px 0;
    color: %s;
    line-height: 1.4;
}

h3 {
    font-size: 20px;
    font-weight: bold;
    margin: 24px 0 16px 0;
    color: %s;
    line-height: 1.4;
}

h4, h5, h6 {
    font-size: 18px;
    font-weight: bold;
    margin: 24px 0 16px 0;
    color: %s;
    line-height: 1.4;
}

/* 段落样式 */
p {
    margin: 0 0 16px 0;
    text-align: justify;
    text-justify: inter-ideograph;
    word-wrap: break-word;
    hyphens: auto;
    line-height: 1.6;
}

/* 列表样式 */
ul, ol {
    margin: 0 0 16px 0;
    padding-left: 24px;
}

li {
    margin-bottom: 8px;
    word-wrap: break-word;
    line-height: 1.6;
}

/* 引用样式 */
blockquote {
    margin: 16px 0;
    padding: 12px 16px;
    border-left: 4px solid #e0e0e0;
    background-color: #f9f9f9;
    font-style: italic;
}

blockquote p {
    margin-bottom: 0;
}

/* 代码样式 */
code {
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: 14px;
    background-color: #f5f5f5;
    padding: 2px 4px;
    border-radius: 3px;
}

pre {
    margin: 16px 0;
    padding: 12px;
    background-color: #f5f5f5;
    border-radius: 4px;
    overflow: hidden;
    word-wrap: break-word;
    white-space: pre-wrap;
}

pre code {
    background-color: transparent;
    padding: 0;
}

/* 表格样式 */
table {
    width: 100%%;
    margin: 16px 0;
    border-collapse: collapse;
    font-size: 14px;
}

th, td {
    padding: 8px 12px;
    text-align: left;
    border: 1px solid #e0e0e0;
    word-wrap: break-word;
}

th {
    background-color: #f5f5f5;
    font-weight: bold;
}

/* 强制边距一致性 */
.card-container::before {
    content: '';
    display: block;
    height: %dpx;
    width: 100%%;
}

.card-container::after {
    content: '';
    display: block;
    height: %dpx;
    width: 100%%;
}
`,
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		hc.config.BackgroundColor,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.Padding,
		hc.config.BackgroundColor,
		fixedMargin, fixedMargin,
		hc.config.CardHeight, fixedMargin*2, hc.config.Padding*2,
		hc.config.TextColor,
		hc.config.TextColor,
		hc.config.TextColor,
		hc.config.TextColor,
		fixedMargin, fixedMargin)
}

// SplitContentByHeight 根据内容高度和固定边距进行精准分页
func (hc *HTMLConverter) SplitContentByHeight(markdownText string) ([]string, error) {
	const CARD_HEIGHT = 1440
	const FIXED_MARGIN = 20
	const MAX_CONTENT_HEIGHT = CARD_HEIGHT - FIXED_MARGIN*2 // 1400px

	// 先转换为HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		return nil, fmt.Errorf("failed to convert markdown to HTML: %v", err)
	}

	// 测量内容高度
	contentHeight, err := hc.measureHTMLHeight(contentHTML)
	if err != nil {
		return nil, fmt.Errorf("failed to measure HTML height: %v", err)
	}

	// 检查是否需要分页
	if contentHeight <= MAX_CONTENT_HEIGHT {
		// 内容高度未超出，直接返回单张卡片
		return []string{markdownText}, nil
	}

	// 内容高度超出，需要分页
	return hc.splitContentByHeight(markdownText, MAX_CONTENT_HEIGHT)
}

// measureHTMLHeight 测量HTML内容高度（模拟实现）
func (hc *HTMLConverter) measureHTMLHeight(html string) (int, error) {
	// 这里应该使用实际的HTML高度测量方法
	// 目前使用基于字符数的估算方法
	// 实际项目中可以使用 html-to-image 或其他工具进行精确测量
	
	// 移除HTML标签，计算纯文本长度
	textContent := hc.stripHTMLTags(html)
	
	// 基于字符数估算高度
	// 假设每行平均50个字符，每行高度为字体大小 * 行高
	charsPerLine := 50
	lineHeight := float64(hc.config.FontSize) * hc.config.LineHeight
	
	lines := (len(textContent) + charsPerLine - 1) / charsPerLine
	estimatedHeight := int(float64(lines) * lineHeight)
	
	return estimatedHeight, nil
}

// stripHTMLTags 移除HTML标签，获取纯文本内容
func (hc *HTMLConverter) stripHTMLTags(html string) string {
	// 简单的HTML标签移除
	// 实际项目中可以使用更复杂的HTML解析器
	html = strings.ReplaceAll(html, "<h1>", "")
	html = strings.ReplaceAll(html, "</h1>", "\n")
	html = strings.ReplaceAll(html, "<h2>", "")
	html = strings.ReplaceAll(html, "</h2>", "\n")
	html = strings.ReplaceAll(html, "<h3>", "")
	html = strings.ReplaceAll(html, "</h3>", "\n")
	html = strings.ReplaceAll(html, "<p>", "")
	html = strings.ReplaceAll(html, "</p>", "\n")
	html = strings.ReplaceAll(html, "<li>", "")
	html = strings.ReplaceAll(html, "</li>", "\n")
	html = strings.ReplaceAll(html, "<ul>", "")
	html = strings.ReplaceAll(html, "</ul>", "\n")
	html = strings.ReplaceAll(html, "<ol>", "")
	html = strings.ReplaceAll(html, "</ol>", "\n")
	html = strings.ReplaceAll(html, "<blockquote>", "")
	html = strings.ReplaceAll(html, "</blockquote>", "\n")
	html = strings.ReplaceAll(html, "<pre>", "")
	html = strings.ReplaceAll(html, "</pre>", "\n")
	html = strings.ReplaceAll(html, "<code>", "")
	html = strings.ReplaceAll(html, "</code>", "")
	
	return html
}

// splitContentByHeight 根据高度拆分内容
func (hc *HTMLConverter) splitContentByHeight(markdownText string, maxContentHeight int) ([]string, error) {
	var cards []string
	lines := strings.Split(markdownText, "\n")
	var currentCard strings.Builder
	var currentHeight int
	
	// 卡片配置
	const titleFontSize = 28
	const subtitleFontSize = 24
	const bodyFontSize = 16
	const availableWidth = 980 // 1080 - 100 (左右边距)
	const titleLineHeight = 1.4
	const bodyLineHeight = 1.6
	const titleMarginBottom = 16
	const bodyMarginBottom = 16

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 计算当前行的高度
		var lineHeight int
		var marginBottom int

		if strings.HasPrefix(line, "# ") {
			// 一级标题
			lineHeight = hc.calculateTextHeight(line[2:], titleFontSize, availableWidth, titleLineHeight)
			marginBottom = titleMarginBottom
		} else if strings.HasPrefix(line, "## ") {
			// 二级标题
			lineHeight = hc.calculateTextHeight(line[3:], subtitleFontSize, availableWidth, titleLineHeight)
			marginBottom = titleMarginBottom
		} else if strings.HasPrefix(line, "### ") {
			// 三级标题
			lineHeight = hc.calculateTextHeight(line[4:], subtitleFontSize, availableWidth, titleLineHeight)
			marginBottom = titleMarginBottom
		} else {
			// 正文
			lineHeight = hc.calculateTextHeight(line, bodyFontSize, availableWidth, bodyLineHeight)
			marginBottom = bodyMarginBottom
		}

		totalElementHeight := lineHeight + marginBottom

		// 检查是否需要新卡片
		needNewCard := false

		// 1. 如果是一级标题且当前卡片非空，强制新卡片
		if strings.HasPrefix(line, "# ") && currentCard.Len() > 0 {
			needNewCard = true
		}

		// 2. 如果添加当前行会超出最大内容高度，需要新卡片
		if currentHeight+totalElementHeight > maxContentHeight {
			needNewCard = true
		}

		// 3. 如果是二级标题且当前卡片已经比较满，考虑新卡片
		if strings.HasPrefix(line, "## ") && currentHeight > int(float64(maxContentHeight)*0.8) {
			needNewCard = true
		}

		if needNewCard && currentCard.Len() > 0 {
			// 保存当前卡片
			cards = append(cards, strings.TrimSpace(currentCard.String()))
			currentCard.Reset()
			currentHeight = 0
		}

		// 添加当前行到卡片
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n")
		}
		currentCard.WriteString(line)
		currentHeight += totalElementHeight
	}

	// 添加最后一张卡片
	if currentCard.Len() > 0 {
		cards = append(cards, strings.TrimSpace(currentCard.String()))
	}

	return cards, nil
}

// calculateTextHeight 计算文本高度
func (hc *HTMLConverter) calculateTextHeight(text string, fontSize int, availableWidth int, lineHeightMultiplier float64) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}

	// 计算字符宽度（中文字符约为字体大小的1.05倍）
	charWidth := float64(fontSize) * 1.05
	charsPerLine := int(float64(availableWidth) / charWidth)

	if charsPerLine <= 0 {
		charsPerLine = 1
	}

	// 计算行数
	textLength := utf8.RuneCountInString(text)
	lines := int(math.Ceil(float64(textLength) / float64(charsPerLine)))

	if lines == 0 {
		lines = 1
	}

	// 计算总高度：行数 × 字体大小 × 行高倍数
	totalHeight := int(float64(lines) * float64(fontSize) * lineHeightMultiplier)

	return totalHeight
}

// truncateString 截断字符串
func truncateString(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length] + "..."
}
