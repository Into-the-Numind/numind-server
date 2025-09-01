package markdown

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/spf13/viper"
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
	Padding             int     `json:"padding"`              // 内边距（兼容性）
	PaddingTop          int     `json:"padding_top"`          // 顶部内边距
	PaddingRight        int     `json:"padding_right"`        // 右侧内边距
	PaddingBottom       int     `json:"padding_bottom"`       // 底部内边距
	PaddingLeft         int     `json:"padding_left"`         // 左侧内边距
	BackgroundColor     string  `json:"background_color"`     // 背景色
	TextColor           string  `json:"text_color"`           // 文字颜色
	BackgroundImage     string  `json:"background_image"`     // 背景图片路径

	// 新增配置字段 - 用于内容分页
	TitleFontSize        int     `json:"title_font_size"`        // 标题字体大小
	SubtitleFontSize     int     `json:"subtitle_font_size"`     // 副标题字体大小
	BodyFontSize         int     `json:"body_font_size"`         // 正文字体大小
	ListFontSize         int     `json:"list_font_size"`         // 列表字体大小
	QuoteFontSize        int     `json:"quote_font_size"`        // 引用字体大小
	TitleLineHeight      float64 `json:"title_line_height"`      // 标题行高倍数
	SubtitleLineHeight   float64 `json:"subtitle_line_height"`   // 副标题行高倍数
	BodyLineHeight       float64 `json:"body_line_height"`       // 正文行高倍数
	TitleMarginBottom    int     `json:"title_margin_bottom"`    // 标题下边距
	SubtitleMarginBottom int     `json:"subtitle_margin_bottom"` // 副标题下边距
	BodyMarginBottom     int     `json:"body_margin_bottom"`     // 正文下边距
	AvailableWidth       int     `json:"available_width"`        // 可用宽度
	MaxContentHeight     int     `json:"max_content_height"`     // 最大内容高度
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
		FontFamily:          "'Noto Sans SC', 'Noto Sans CJK SC', 'Microsoft YaHei', sans-serif",
		FontSize:            16,
		LineHeight:          1.6,
		Padding:             60,
		PaddingTop:          60,
		PaddingRight:        50,
		PaddingBottom:       10, // 减少底部内边距
		PaddingLeft:         50,
		BackgroundColor:     "#ffffff",
		TextColor:           "#333333",
		BackgroundImage:     "", // 默认无背景图
	}

	// 从配置文件加载分页相关配置
	if viper.IsSet("html_converter.card.width") {
		config.CardWidth = viper.GetInt("html_converter.card.width")
	}
	if viper.IsSet("html_converter.card.height") {
		config.CardHeight = viper.GetInt("html_converter.card.height")
	}
	if viper.IsSet("html_converter.card.padding") {
		config.Padding = viper.GetInt("html_converter.card.padding")
	}
	// 加载分页配置中的内边距设置
	if viper.IsSet("pagination.card.padding.top") {
		config.PaddingTop = viper.GetInt("pagination.card.padding.top")
	}
	if viper.IsSet("pagination.card.padding.right") {
		config.PaddingRight = viper.GetInt("pagination.card.padding.right")
	}
	if viper.IsSet("pagination.card.padding.bottom") {
		config.PaddingBottom = viper.GetInt("pagination.card.padding.bottom")
	}
	if viper.IsSet("pagination.card.padding.left") {
		config.PaddingLeft = viper.GetInt("pagination.card.padding.left")
	}
	if viper.IsSet("html_converter.fonts.family") {
		config.FontFamily = viper.GetString("html_converter.fonts.family")
	}
	if viper.IsSet("html_converter.fonts.title_size") {
		config.TitleFontSize = viper.GetInt("html_converter.fonts.title_size")
	} else {
		config.TitleFontSize = 28 // 默认值
	}
	if viper.IsSet("html_converter.fonts.subtitle_size") {
		config.SubtitleFontSize = viper.GetInt("html_converter.fonts.subtitle_size")
	} else {
		config.SubtitleFontSize = 24 // 默认值
	}
	if viper.IsSet("html_converter.fonts.body_size") {
		config.BodyFontSize = viper.GetInt("html_converter.fonts.body_size")
	} else {
		config.BodyFontSize = 16 // 默认值
	}
	if viper.IsSet("html_converter.fonts.list_size") {
		config.ListFontSize = viper.GetInt("html_converter.fonts.list_size")
	} else {
		config.ListFontSize = 16 // 默认值，与body相同
	}
	if viper.IsSet("html_converter.fonts.quote_size") {
		config.QuoteFontSize = viper.GetInt("html_converter.fonts.quote_size")
	} else {
		config.QuoteFontSize = 16 // 默认值，与body相同
	}
	if viper.IsSet("html_converter.line_heights.title") {
		config.TitleLineHeight = viper.GetFloat64("html_converter.line_heights.title")
	} else {
		config.TitleLineHeight = 1.4 // 默认值
	}
	if viper.IsSet("html_converter.line_heights.subtitle") {
		config.SubtitleLineHeight = viper.GetFloat64("html_converter.line_heights.subtitle")
	} else {
		config.SubtitleLineHeight = 1.4 // 默认值
	}
	if viper.IsSet("html_converter.line_heights.body") {
		config.BodyLineHeight = viper.GetFloat64("html_converter.line_heights.body")
	} else {
		config.BodyLineHeight = 1.6 // 默认值
	}
	if viper.IsSet("html_converter.margins.title_bottom") {
		config.TitleMarginBottom = viper.GetInt("html_converter.margins.title_bottom")
	} else {
		config.TitleMarginBottom = 16 // 默认值
	}
	if viper.IsSet("html_converter.margins.subtitle_bottom") {
		config.SubtitleMarginBottom = viper.GetInt("html_converter.margins.subtitle_bottom")
	} else {
		config.SubtitleMarginBottom = 16 // 默认值
	}
	if viper.IsSet("html_converter.margins.body_bottom") {
		config.BodyMarginBottom = viper.GetInt("html_converter.margins.body_bottom")
	} else {
		config.BodyMarginBottom = 16 // 默认值
	}
	if viper.IsSet("html_converter.pagination.available_width") {
		config.AvailableWidth = viper.GetInt("html_converter.pagination.available_width")
	} else {
		config.AvailableWidth = 980 // 默认值：1080 - 100 (左右边距)
	}
	if viper.IsSet("html_converter.pagination.max_content_height") {
		config.MaxContentHeight = viper.GetInt("html_converter.pagination.max_content_height")
	} else {
		config.MaxContentHeight = 1200 // 默认值
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

// ConvertToStyledHTMLWithDynamicHeight 将 Markdown 转换为支持动态高度的 HTML 页面
func (hc *HTMLConverter) ConvertToStyledHTMLWithDynamicHeight(markdownText, title string) (string, error) {
	// 先转换为基础 HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		return "", err
	}

	// 包装为支持动态高度的 HTML 页面
	return hc.wrapWithDynamicHeightStyles(contentHTML, title), nil
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
	// 封面卡片布局：背景图在底层，图片和标题在上层
	return fmt.Sprintf(`
<div class="cover-card-container">
    <!-- 背景层：背景图在最后一层 -->
    <div class="cover-background-layer"></div>
    
    <!-- 内容层：图片和标题在上层 -->
    <div class="cover-content-layer">
        <div class="cover-image-section">
            <div class="cover-image-placeholder">
                <div class="placeholder-icon">🖼️</div>
                <div class="placeholder-text">封面图片</div>
            </div>
        </div>
        <div class="cover-title-section">
            <h1 class="cover-title">%s</h1>
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
		coverContent := hc.generateCoverCardHTML(title)
		return hc.wrapWithStyles(coverContent, title, true) // 封面卡片
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

// SetBackgroundImage 设置背景图片路径
func (hc *HTMLConverter) SetBackgroundImage(backgroundImage string) {
	hc.config.BackgroundImage = backgroundImage
}

// GetBackgroundImage 获取背景图片路径
func (hc *HTMLConverter) GetBackgroundImage() string {
	return hc.config.BackgroundImage
}

// ConvertMarkdownCardToHTML 将Markdown内容转换为卡片HTML
func (hc *HTMLConverter) ConvertMarkdownCardToHTML(markdownText, title string, cardIndex int) string {
	// 转换markdown为HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		contentHTML = fmt.Sprintf("<p>%s</p>", hc.escapeHTML(markdownText))
	}

	// 使用清晰大字号风格包装为卡片HTML
	return hc.wrapWithClearLargeFontStyles(contentHTML, title, cardIndex)
}

// ConvertMarkdownCardToHTMLWithLegacyStyle 将Markdown内容转换为卡片HTML（使用旧样式）
func (hc *HTMLConverter) ConvertMarkdownCardToHTMLWithLegacyStyle(markdownText, title string, cardIndex int) string {
	// 转换markdown为HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		contentHTML = fmt.Sprintf("<p>%s</p>", hc.escapeHTML(markdownText))
	}

	// 包装为卡片HTML（使用旧样式）
	return hc.wrapWithMarkdownCardStyles(contentHTML, title, cardIndex)
}

// wrapWithClearLargeFontStyles 为Markdown卡片包装清晰大字号样式
func (hc *HTMLConverter) wrapWithClearLargeFontStyles(contentHTML, title string, cardIndex int) string {
	cssStyles := hc.generateClearLargeFontCSS()

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>%s</style>
</head>
<body>
    <div class="markdown-card-container">
        <div class="markdown-content markdown-body">
            %s
        </div>
    </div>
</body>
</html>`, hc.escapeHTML(title), cssStyles, contentHTML)
}

// wrapWithMarkdownCardStyles 为Markdown卡片包装样式
func (hc *HTMLConverter) wrapWithMarkdownCardStyles(contentHTML, title string, cardIndex int) string {
	cssStyles := hc.generateMarkdownCardCSS()

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>%s</style>
</head>
<body>
    <div class="markdown-card-container">
        <div class="markdown-content">
            %s
        </div>
    </div>
</body>
</html>`, hc.escapeHTML(title), cssStyles, contentHTML)
}

// generateCSS 生成 CSS 样式
func (hc *HTMLConverter) generateCSS(isCoverCard bool) string {
	// 处理背景样式 - 优先使用模板背景，如果没有则使用默认背景色
	backgroundStyle := ""
	if hc.config.BackgroundImage != "" {
		// 使用模板背景图片，覆盖整个卡片
		backgroundStyle = fmt.Sprintf("background: url('%s') center center / cover no-repeat;", hc.config.BackgroundImage)
	} else {
		// 使用默认背景色
		backgroundStyle = fmt.Sprintf("background-color: %s;", hc.config.BackgroundColor)
	}

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
    %s
    overflow: visible;
    width: %dpx;
    height: %dpx;
    margin: 0;
    padding: 0;
}

.card-container {
    width: %dpx;
    height: %dpx;
    padding: %dpx %dpx %dpx %dpx; /* 上右下左内边距 */
    overflow: visible;
    %s
    position: relative;
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}`,
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		backgroundStyle,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.PaddingTop,
		hc.config.PaddingRight,
		hc.config.PaddingBottom,
		hc.config.PaddingLeft,
		backgroundStyle,
	)

	if isCoverCard {
		// 封面卡片特殊样式
		coverCSS := fmt.Sprintf(`
/* 封面卡片样式 */
.card-container {
    position: relative;
    %s
    color: white;
    padding: 0;
    overflow: hidden;
}

.cover-card-container {
    width: 100%%;
    height: 100%%;
    position: relative;
}

/* 背景层：背景图在最后一层 */
.cover-background-layer {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%%;
    height: 100%%;
    z-index: 1;
    background: inherit;
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}

/* 内容层：图片和标题在上层 */
.cover-content-layer {
    position: relative;
    width: 100%%;
    height: 100%%;
    z-index: 2;
    display: flex;
    flex-direction: column;
}

.cover-image-section {
    flex: 0 0 65%%;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 40px;
    position: relative;
}

.cover-title-section {
    flex: 0 0 35%%;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 40px;
    background: rgba(255, 255, 255, 0.95);
    backdrop-filter: blur(10px);
    position: relative;
}

.cover-title {
    font-size: %dpx;
    font-weight: bold;
    text-align: center;
    text-shadow: 0 2px 8px rgba(0,0,0,0.5);
    line-height: 1.2;
    margin: 0;
    color: #2c3e50;
}

.cover-image {
    max-width: 100%%;
    max-height: 100%%;
    border-radius: 12px;
    box-shadow: 0 12px 40px rgba(0,0,0,0.4);
    object-fit: cover;
}

.cover-image-placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    width: 80%%;
    height: 80%%;
    background: rgba(255, 255, 255, 0.9);
    border-radius: 12px;
    border: 2px dashed #dee2e6;
    color: #6c757d;
}

.placeholder-icon {
    font-size: 48px;
    margin-bottom: 16px;
    opacity: 0.8;
}

.placeholder-text {
    font-size: 18px;
    color: #6c757d;
    text-align: center;
    font-weight: bold;
}

/* 隐藏封面卡片中的其他内容 */
.card-container > *:not(.cover-card-container) {
    display: none;
}`, hc.config.TitleFontSize)
		return baseCSS + coverCSS
	}

	// 普通卡片样式
	normalCSS := fmt.Sprintf(`
/* 标题样式 */
h1 {
    font-size: %dpx;
    font-weight: bold;
    margin-bottom: %dpx;
    color: %s;
    text-align: center;
}

h2 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
}

h3 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
}

h4, h5, h6 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
}

/* 段落样式 */
p {
    font-size: %dpx;
    margin-bottom: %dpx;
    text-align: justify;
    text-justify: inter-ideograph;
    word-wrap: break-word;
    hyphens: auto;
}

/* 列表样式 */
ul, ol {
    font-size: %dpx;
    margin: %dpx 0;
    padding-left: 24px;
}

li {
    font-size: %dpx;
    margin-bottom: 6px;
    word-wrap: break-word;
}

/* 引用样式 */
blockquote {
    font-size: %dpx;
    margin: %dpx 0;
    padding: 12px 16px;
    border-left: 4px solid #e0e0e0;
    background-color: #f9f9f9;
    font-style: italic;
}

blockquote p {
    font-size: %dpx;
    margin-bottom: 0;
}

/* 代码样式 */
code {
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: %dpx;
    background-color: #f5f5f5;
    padding: 2px 4px;
    border-radius: 3px;
}

pre {
    font-size: %dpx;
    margin: %dpx 0;
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
    margin: %dpx 0;
    border-collapse: collapse;
    font-size: %dpx;
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
		hc.config.TitleFontSize,
		hc.config.TitleMarginBottom,
		hc.config.TextColor,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleMarginBottom,
		hc.config.SubtitleMarginBottom,
		hc.config.TextColor,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleMarginBottom,
		hc.config.SubtitleMarginBottom,
		hc.config.TextColor,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		hc.config.TextColor,
		hc.config.ListFontSize,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.QuoteFontSize,
		hc.config.BodyMarginBottom,
		hc.config.QuoteFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		hc.config.BodyMarginBottom,
		hc.config.BodyFontSize,
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
    padding: %dpx %dpx %dpx %dpx; /* 上右下左内边距 */
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
    margin-top: %dpx;
    margin-bottom: %dpx;
    font-weight: bold;
    line-height: 1.4;
    word-wrap: break-word;
}

h1 { 
    font-size: %dpx; 
    color: #2c3e50; 
    border-bottom: 2px solid #3498db;
    padding-bottom: 8px;
}

h2 { 
    font-size: %dpx; 
    color: #34495e; 
    border-bottom: 1px solid #bdc3c7;
    padding-bottom: 6px;
}

h3 { 
    font-size: %dpx; 
    color: #34495e; 
}

h4 { 
    font-size: %dpx; 
    color: #34495e; 
}

h5 { 
    font-size: %dpx; 
    color: #34495e; 
}

h6 { 
    font-size: %dpx; 
    color: #34495e; 
}

p {
    font-size: %dpx;
    margin-bottom: %dpx;
    text-align: justify;
    word-wrap: break-word;
    hyphens: auto;
}

/* 列表样式 */
ul, ol {
    font-size: %dpx;
    margin: %dpx 0;
    padding-left: 30px;
}

li {
    font-size: %dpx;
    margin-bottom: 8px;
    line-height: 1.6;
}

ul li::marker {
    color: #3498db;
}

/* 引用块样式 */
blockquote {
    font-size: %dpx;
    margin: %dpx 0;
    padding: 16px 20px;
    border-left: 4px solid #3498db;
    background: #ecf0f1;
    font-style: italic;
    border-radius: 0 6px 6px 0;
}

blockquote p {
    font-size: %dpx;
    margin-bottom: 0;
    color: #2c3e50;
}

/* 代码样式 */
code {
    background: #f8f9fa;
    padding: 2px 6px;
    border-radius: 3px;
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: %dpx;
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

/* 内容区域样式 */
.markdown-content {
    width: 100%;
    height: auto;
    overflow: visible;
}

/* 防止文本溢出 */
.markdown-card-container {
    word-wrap: break-word;
    overflow-wrap: break-word;
    hyphens: auto;
}
`,
		hc.config.TitleMarginBottom,
		hc.config.TitleMarginBottom,
		hc.config.TitleFontSize,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.QuoteFontSize,
		hc.config.BodyMarginBottom,
		hc.config.QuoteFontSize,
		hc.config.BodyFontSize,
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		hc.config.BackgroundColor,
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.PaddingTop,
		hc.config.PaddingRight,
		hc.config.PaddingBottom,
		hc.config.PaddingLeft,
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
    padding: %dpx %dpx %dpx %dpx; /* 上右下左内边距 */
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
    font-size: %dpx;
    font-weight: bold;
    margin: 0 0 %dpx 0;
    color: %s;
    line-height: 1.4;
}

h2 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
    line-height: 1.4;
}

h3 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
    line-height: 1.4;
}

h4, h5, h6 {
    font-size: %dpx;
    font-weight: bold;
    margin: %dpx 0 %dpx 0;
    color: %s;
    line-height: 1.4;
}

/* 段落样式 */
p {
    font-size: %dpx;
    margin: 0 0 %dpx 0;
    text-align: justify;
    text-justify: inter-ideograph;
    word-wrap: break-word;
    hyphens: auto;
    line-height: 1.6;
}

/* 列表样式 */
ul, ol {
    font-size: %dpx;
    margin: 0 0 %dpx 0;
    padding-left: 24px;
}

li {
    font-size: %dpx;
    margin-bottom: 8px;
    word-wrap: break-word;
    line-height: 1.6;
}

/* 引用样式 */
blockquote {
    font-size: %dpx;
    margin: %dpx 0;
    padding: 12px 16px;
    border-left: 4px solid #e0e0e0;
    background-color: #f9f9f9;
    font-style: italic;
}

blockquote p {
    font-size: %dpx;
    margin-bottom: 0;
}

/* 代码样式 */
code {
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: %dpx;
    background-color: #f5f5f5;
    padding: 2px 4px;
    border-radius: 3px;
}

pre {
    font-size: %dpx;
    margin: %dpx 0;
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
    margin: %dpx 0;
    border-collapse: collapse;
    font-size: %dpx;
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
		hc.config.PaddingTop,
		hc.config.PaddingRight,
		hc.config.PaddingBottom,
		hc.config.PaddingLeft,
		hc.config.BackgroundColor,
		fixedMargin, fixedMargin,
		hc.config.CardHeight, fixedMargin*2, hc.config.PaddingTop+hc.config.PaddingBottom,
		hc.config.TitleFontSize,
		hc.config.TitleMarginBottom,
		hc.config.TextColor,
		hc.config.SubtitleFontSize,
		hc.config.TitleMarginBottom,
		hc.config.TitleMarginBottom,
		hc.config.TextColor,
		hc.config.SubtitleFontSize,
		hc.config.TitleMarginBottom,
		hc.config.TitleMarginBottom,
		hc.config.TextColor,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		hc.config.TextColor,
		hc.config.ListFontSize,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.QuoteFontSize,
		hc.config.BodyMarginBottom,
		hc.config.QuoteFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		hc.config.BodyMarginBottom,
		hc.config.BodyFontSize,
		fixedMargin, fixedMargin)
}

// SplitContentByHeight 根据内容高度和固定边距进行精准分页 - 优化版
func (hc *HTMLConverter) SplitContentByHeight(markdownText string) ([]string, error) {
	// 使用配置中的卡片高度和边距
	cardHeight := hc.config.CardHeight
	fixedMargin := hc.config.PaddingTop + hc.config.PaddingBottom
	maxContentHeight := cardHeight - fixedMargin

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
	log.C(context.Background()).Infow("🎨 测量内容高度",
		"content_height", contentHeight,
		"max_content_height", maxContentHeight,
		"card_height", cardHeight,
		"fixed_margin", fixedMargin)

	// 检查是否需要分页
	if contentHeight <= maxContentHeight {
		// 内容高度未超出，直接返回单张卡片
		return []string{markdownText}, nil
	}

	// 内容高度超出，使用优化的分页算法
	return hc.splitContentByHeightOptimized(markdownText, maxContentHeight)
}

// measureHTMLHeight 测量HTML内容高度（改进版）
func (hc *HTMLConverter) measureHTMLHeight(html string) (int, error) {
	// 使用更精确的高度计算方法
	// 考虑不同元素类型的字体大小和行高

	// 解析HTML，计算不同类型元素的高度
	totalHeight := 0
	lines := strings.Split(html, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var elementHeight int
		var marginBottom int

		// 根据HTML标签类型计算高度
		if strings.Contains(line, "<h1>") {
			// 一级标题
			text := hc.extractTextFromTag(line, "h1")
			elementHeight = hc.calculateTextHeight(text, hc.config.TitleFontSize, hc.config.AvailableWidth, hc.config.TitleLineHeight)
			marginBottom = hc.config.TitleMarginBottom
		} else if strings.Contains(line, "<h2>") {
			// 二级标题
			text := hc.extractTextFromTag(line, "h2")
			elementHeight = hc.calculateTextHeight(text, hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
			marginBottom = hc.config.SubtitleMarginBottom
		} else if strings.Contains(line, "<h3>") {
			// 三级标题
			text := hc.extractTextFromTag(line, "h3")
			elementHeight = hc.calculateTextHeight(text, hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
			marginBottom = hc.config.SubtitleMarginBottom
		} else if strings.Contains(line, "<p>") {
			// 段落
			text := hc.extractTextFromTag(line, "p")
			elementHeight = hc.calculateTextHeight(text, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
			marginBottom = hc.config.BodyMarginBottom
		} else if strings.Contains(line, "<li>") {
			// 列表项
			text := hc.extractTextFromTag(line, "li")
			elementHeight = hc.calculateTextHeight(text, hc.config.ListFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
			marginBottom = hc.config.BodyMarginBottom
		} else if strings.Contains(line, "<blockquote>") {
			// 引用
			text := hc.extractTextFromTag(line, "blockquote")
			elementHeight = hc.calculateTextHeight(text, hc.config.QuoteFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
			marginBottom = hc.config.BodyMarginBottom
		} else {
			// 其他内容，使用正文字体
			text := hc.stripHTMLTags(line)
			elementHeight = hc.calculateTextHeight(text, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
			marginBottom = hc.config.BodyMarginBottom
		}

		totalHeight += elementHeight + marginBottom
	}

	return totalHeight, nil
}

// extractTextFromTag 从HTML标签中提取文本内容
func (hc *HTMLConverter) extractTextFromTag(htmlLine, tagName string) string {
	startTag := "<" + tagName + ">"
	endTag := "</" + tagName + ">"

	startIndex := strings.Index(htmlLine, startTag)
	if startIndex == -1 {
		return ""
	}

	endIndex := strings.Index(htmlLine, endTag)
	if endIndex == -1 {
		return ""
	}

	startPos := startIndex + len(startTag)
	if startPos >= endIndex {
		return ""
	}

	return strings.TrimSpace(htmlLine[startPos:endIndex])
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

// splitContentByHeightOptimized 优化的分页算法 - 支持段落内分页的智能内容分布
func (hc *HTMLConverter) splitContentByHeightOptimized(markdownText string, maxContentHeight int) ([]string, error) {
	// 预处理：清理输入文本，移除无效内容
	markdownText = strings.TrimSpace(markdownText)
	if markdownText == "" || markdownText == "\"" || markdownText == "'" {
		return []string{}, fmt.Errorf("invalid or empty content")
	}

	lines := strings.Split(markdownText, "\n")

	// 完全移除安全边距限制，让内容精确填充到底边距临界点
	// 使用真正的最大内容高度进行分页
	effectiveMaxHeight := maxContentHeight

	// 全新的段落内分页算法
	return hc.splitContentWithInParagraphPagination(lines, effectiveMaxHeight, maxContentHeight, 0)
}

// splitContentWithInParagraphPagination 支持段落内分页的新算法
func (hc *HTMLConverter) splitContentWithInParagraphPagination(lines []string, effectiveMaxHeight, maxContentHeight, _ int) ([]string, error) {
	log.C(context.Background()).Infow("🔄 开始精确分页处理",
		"total_lines", len(lines),
		"max_content_height", maxContentHeight,
		"fill_to_margin_boundary", true)

	var cards []string
	var currentCard strings.Builder
	currentHeight := 0
	cardIndex := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 跳过空行、无效行和一级标题
		if line == "" || line == "\"" || line == "'" || strings.HasPrefix(line, "# ") {
			continue
		}

		// 判断行类型和计算高度
		var lineHeight, marginBottom int
		var isTitle bool

		if strings.HasPrefix(line, "## ") {
			lineHeight = hc.calculateTextHeight(line[3:], hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
			marginBottom = hc.config.SubtitleMarginBottom
			isTitle = true
		} else if strings.HasPrefix(line, "### ") {
			lineHeight = hc.calculateTextHeight(line[4:], hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
			marginBottom = hc.config.SubtitleMarginBottom
			isTitle = true
		} else {
			// 普通段落 - 这里是关键：支持段落内分页
			lineHeight = hc.calculateTextHeight(line, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
			marginBottom = hc.config.BodyMarginBottom
		}

		totalLineHeight := lineHeight + marginBottom

		// 检查是否需要分页
		needNewCard := false

		// 一级标题已被过滤，不再需要强制分页逻辑

		// 2. 检查高度是否超限 - 使用真正的最大高度，不预留过多空间
		willExceedHeight := currentHeight+totalLineHeight > maxContentHeight

		// 如果会超出高度限制
		if willExceedHeight {
			// 计算剩余可用高度 - 使用真正的剩余空间
			remainingHeight := maxContentHeight - currentHeight

			// 超激进的段落分割策略：只要有剩余空间就尝试分割，最大化利用边距空间
			if !isTitle && len(line) > 20 && remainingHeight > hc.config.BodyFontSize/3 { // 进一步降低阈值
				// 执行段落内分页
				firstPart, secondPart := hc.SplitLongParagraph(line, remainingHeight)
				if firstPart != "" && secondPart != "" {
					// 添加第一部分到当前卡片
					if currentCard.Len() > 0 {
						currentCard.WriteString("\n")
					}
					currentCard.WriteString(firstPart)

					// 结束当前卡片
					cardContent := strings.TrimSpace(currentCard.String())
					cards = append(cards, cardContent)

					log.C(context.Background()).Infow("📄 段落内分页卡片创建",
						"card_index", cardIndex+1,
						"card_height", currentHeight+hc.calculateTextHeight(firstPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight),
						"split_paragraph", true,
						"first_part_chars", len(firstPart),
						"second_part_chars", len(secondPart))

					// 开始新卡片并添加第二部分
					currentCard.Reset()
					currentCard.WriteString(secondPart)
					currentHeight = hc.calculateTextHeight(secondPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight) + marginBottom
					cardIndex++
					continue
				}
			}
			needNewCard = true
		}

		// 执行分页
		if needNewCard && currentCard.Len() > 0 {
			cardContent := strings.TrimSpace(currentCard.String())
			cards = append(cards, cardContent)

			utilization := float64(currentHeight) / float64(maxContentHeight) * 100

			log.C(context.Background()).Infow("📄 常规卡片创建",
				"card_index", cardIndex+1,
				"card_height", currentHeight,
				"utilization", fmt.Sprintf("%.1f%%", utilization),
				"remaining_space", maxContentHeight-currentHeight,
				"content_lines", strings.Count(cardContent, "\n")+1)

			currentCard.Reset()
			currentHeight = 0
			cardIndex++
		}

		// 添加当前行到卡片
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n")
		}
		currentCard.WriteString(line)
		currentHeight += totalLineHeight
	}

	// 处理最后一张卡片
	if currentCard.Len() > 0 {
		cardContent := strings.TrimSpace(currentCard.String())
		if cardContent != "" && cardContent != "\"" && cardContent != "'" && len(cardContent) > 2 {
			cards = append(cards, cardContent)

			utilization := float64(currentHeight) / float64(maxContentHeight) * 100

			log.C(context.Background()).Infow("📄 最后卡片创建",
				"card_index", cardIndex+1,
				"card_height", currentHeight,
				"utilization", fmt.Sprintf("%.1f%%", utilization),
				"remaining_space", maxContentHeight-currentHeight,
				"content_lines", strings.Count(cardContent, "\n")+1)
		}
	}

	// 确保至少有一张卡片
	if len(cards) == 0 {
		cards = append(cards, strings.Join(lines, "\n"))
	}

	log.C(context.Background()).Infow("✅ 段落内分页完成",
		"total_cards", len(cards),
		"algorithm", "in-paragraph-pagination")

	return cards, nil
}

// SplitLongParagraph 精确分割段落，确保第一部分恰好填满到底部边距位置
func (hc *HTMLConverter) SplitLongParagraph(paragraph string, remainingHeight int) (string, string) {
	// 精确计算参数
	charHeight := float64(hc.config.BodyFontSize) * hc.config.BodyLineHeight
	charWidth := float64(hc.config.BodyFontSize) * 1.05 // 中文字符宽度约为字体大小的1.05倍
	charsPerLine := int(float64(hc.config.AvailableWidth) / charWidth)
	if charsPerLine <= 0 {
		charsPerLine = 30 // 默认每行30字符
	}

	// 计算可用行数（精确到0.3行）- 更激进的空间利用
	exactLines := float64(remainingHeight) / charHeight
	if exactLines < 1.5 {
		return "", paragraph // 空间不足，不分割
	}

	// 计算理想的字符数（考虑行数的小数部分）
	idealChars := int(exactLines * float64(charsPerLine))

	runes := []rune(paragraph)
	if len(runes) <= idealChars {
		return paragraph, "" // 不需要分割
	}

	// 多步骤寻找最佳分割点
	bestSplitPoint := idealChars

	// 第一优先级：在理想位置附近寻找句子结尾（。！？）
	searchRange := min(idealChars/3, 100) // 搜索范围：理想位置前后1/3或100字符内
	for i := idealChars - searchRange; i <= idealChars+searchRange/2 && i < len(runes); i++ {
		if i > 0 && i < len(runes) && (runes[i] == '。' || runes[i] == '！' || runes[i] == '？') {
			// 验证分割后第一部分的高度是否合适
			candidateFirstPart := strings.TrimSpace(string(runes[:i+1]))
			candidateHeight := hc.calculateTextHeight(candidateFirstPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)

			// 更激进的高度利用：允许95%-105%的剩余高度，精确填充到边距
			heightRatio := float64(candidateHeight) / float64(remainingHeight)
			if heightRatio >= 0.95 && heightRatio <= 1.05 {
				bestSplitPoint = i + 1
				break
			}
		}
	}

	// 第二优先级：如果没找到合适的句子结尾，寻找逗号分割点
	if bestSplitPoint == idealChars {
		for i := idealChars - searchRange/2; i <= idealChars+searchRange/3 && i < len(runes); i++ {
			if i > 0 && i < len(runes) && (runes[i] == '，' || runes[i] == '、') {
				candidateFirstPart := strings.TrimSpace(string(runes[:i+1]))
				candidateHeight := hc.calculateTextHeight(candidateFirstPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)

				heightRatio := float64(candidateHeight) / float64(remainingHeight)
				if heightRatio >= 0.90 && heightRatio <= 1.10 {
					bestSplitPoint = i + 1
					break
				}
			}
		}
	}

	// 第三优先级：如果还没找到，进行二分搜索找到最佳长度
	if bestSplitPoint == idealChars {
		left := idealChars - searchRange/2
		right := idealChars + searchRange/2
		if left < 0 {
			left = 0
		}
		if right >= len(runes) {
			right = len(runes) - 1
		}

		bestDiff := float64(remainingHeight)

		for i := left; i <= right; i++ {
			candidateFirstPart := strings.TrimSpace(string(runes[:i]))
			candidateHeight := hc.calculateTextHeight(candidateFirstPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)

			diff := math.Abs(float64(candidateHeight) - float64(remainingHeight))
			if diff < bestDiff {
				bestDiff = diff
				bestSplitPoint = i
			}
		}
	}

	// 确保分割点不超出范围
	if bestSplitPoint >= len(runes) {
		bestSplitPoint = len(runes) - 1
	}
	if bestSplitPoint <= 0 {
		bestSplitPoint = 1
	}

	firstPart := strings.TrimSpace(string(runes[:bestSplitPoint]))
	secondPart := strings.TrimSpace(string(runes[bestSplitPoint:]))

	// 验证分割结果
	if len(firstPart) < 10 || len(secondPart) < 10 {
		return "", paragraph // 分割结果太短，不分割
	}

	// 计算实际高度以验证效果
	actualHeight := hc.calculateTextHeight(firstPart, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
	utilizationRatio := float64(actualHeight) / float64(remainingHeight)

	log.C(context.Background()).Infow("📝 精确段落分割",
		"original_length", len(runes),
		"ideal_chars", idealChars,
		"actual_split_point", bestSplitPoint,
		"first_part_chars", len([]rune(firstPart)),
		"second_part_chars", len([]rune(secondPart)),
		"remaining_height", remainingHeight,
		"actual_height", actualHeight,
		"space_utilization", fmt.Sprintf("%.1f%%", utilizationRatio*100),
		"exact_lines_available", fmt.Sprintf("%.2f", exactLines),
		"chars_per_line", charsPerLine)

	return firstPart, secondPart
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

// wrapWithDynamicHeightStyles 包装HTML内容为支持动态高度的HTML页面
func (hc *HTMLConverter) wrapWithDynamicHeightStyles(contentHTML, title string) string {
	// 使用CSS的 min-height 和 max-height 特性来支持动态高度
	// 同时使用 overflow: auto 来处理内容溢出
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: %s;
            background-color: %s;
            color: %s;
            line-height: %f;
            padding: %dpx;
            min-height: %dpx;
            max-height: %dpx;
            overflow: auto;
            word-wrap: break-word;
            word-break: break-all;
        }
        
        /* 动态高度容器 */
        .content-container {
            min-height: %dpx;
            max-height: %dpx;
            overflow: auto;
            padding: %dpx;
            background: white;
            border-radius: 8px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        
        /* 标题样式 */
        h1 {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            color: #333333;
            font-weight: bold;
        }
        
        h2 {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            color: #666666;
            font-weight: bold;
        }
        
        h3 {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            color: #666666;
            font-weight: bold;
        }
        
        /* 段落样式 */
        p {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            text-align: justify;
            word-wrap: break-word;
        }
        
        /* 列表样式 */
        ul, ol {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            padding-left: 20px;
        }
        
        li {
            margin-bottom: 8px;
            font-size: %dpx;
        }
        
        /* 引用样式 */
        blockquote {
            font-size: %dpx;
            line-height: %f;
            margin-bottom: %dpx;
            padding: 10px 15px;
            border-left: 4px solid #1E90FF;
            background-color: #f8f9fa;
            color: #1E90FF;
        }
        
        /* 代码样式 */
        code {
            font-size: %dpx;
            background-color: #f4f4f4;
            padding: 2px 4px;
            border-radius: 3px;
        }
        
        pre {
            font-size: %dpx;
            background-color: #f4f4f4;
            padding: 10px;
            border-radius: 5px;
            overflow-x: auto;
            margin-bottom: %dpx;
        }
        
        /* 响应式设计 */
        @media (max-width: 768px) {
            body {
                padding: 10px;
                min-height: auto;
                max-height: none;
            }
            
            .content-container {
                min-height: auto;
                max-height: none;
            }
        }
    </style>
</head>
<body>
    <div class="content-container">
        %s
    </div>
</body>
</html>`,
		hc.escapeHTML(title),
		hc.config.FontFamily,
		hc.config.BackgroundColor,
		hc.config.TextColor,
		hc.config.LineHeight,
		hc.config.PaddingTop+hc.config.PaddingBottom, // 使用顶部和底部内边距
		hc.config.CardHeight,
		hc.config.CardHeight*2, // 最大高度为卡片高度的2倍
		hc.config.CardHeight,
		hc.config.CardHeight*2,
		hc.config.PaddingTop+hc.config.PaddingBottom, // 使用顶部和底部内边距
		hc.config.TitleFontSize,
		hc.config.TitleLineHeight,
		hc.config.TitleMarginBottom,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleLineHeight,
		hc.config.SubtitleMarginBottom,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleLineHeight,
		hc.config.SubtitleMarginBottom,
		hc.config.BodyFontSize,
		hc.config.BodyLineHeight,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.BodyLineHeight,
		hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.QuoteFontSize,
		hc.config.BodyLineHeight,
		hc.config.BodyMarginBottom,
		hc.config.QuoteFontSize,
		contentHTML)
}

// generateClearLargeFontCSS 生成清晰大字号风格的CSS样式
func (hc *HTMLConverter) generateClearLargeFontCSS() string {
	// 处理背景样式
	backgroundStyle := ""
	if hc.config.BackgroundImage != "" {
		// 如果有背景图，使用背景图
		backgroundStyle = fmt.Sprintf("background: url('%s') center center / cover no-repeat;", hc.config.BackgroundImage)
	} else {
		// 否则使用背景色
		backgroundStyle = fmt.Sprintf("background-color: %s;", hc.config.BackgroundColor)
	}

	return fmt.Sprintf(`
/* 全局基础样式 - 清晰大字号风格 */
body {
    font-family: "Noto Sans CJK SC", "Helvetica Neue", Arial, sans-serif;
    font-size: 16px;          /* 基础字号 */
    line-height: 1.8;         /* 增大行高，提升可读性 */
    color: #333;              /* 深灰色文字，替代默认黑色 */
    margin: 0;
    padding: 0;
    width: 1080px;
    height: 1440px;
    overflow: hidden;
    %s
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}

.markdown-card-container {
    width: 100%%;
    height: 100%%;
    padding: 60px 50px;       /* 上右下左内边距 */
    box-sizing: border-box;
    %s
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}

.markdown-body {
    font-family: "Noto Sans CJK SC", "Helvetica Neue", Arial, sans-serif;
    font-size: %dpx;          /* 基础字号 */
    line-height: 1.6;         /* 优化行高，平衡可读性和空间利用 */
    color: #333;              /* 深灰色文字，替代默认黑色 */
    padding: 0;               /* 移除内边距，由容器控制 */
    width: 100%%;
    max-height: calc(100%% - 80px); /* 强制底部边距：容器高度减去80px底部边距 */
    overflow: hidden;         /* 隐藏超出部分，强制执行边距限制 */
    word-wrap: break-word;
    word-break: break-word;   /* 中文换行优化 */
    hyphens: auto;
    box-sizing: border-box;   /* 确保盒模型正确 */
    display: flex;
    flex-direction: column;
    justify-content: flex-start; /* 内容从顶部开始 */
}

/* 标题样式：参考目标案例的大字号、加粗 */
.markdown-body h1 {
    font-size: %dpx;          /* 大字号标题 */
    font-weight: 700;         /* 加粗 */
    margin: 0 0 %dpx 0;       /* 下边距 */
    color: #2c3e50;           /* 深色标题 */
    line-height: 1.3;         /* 标题行高 */
    text-align: center;       /* 居中对齐 */
}

.markdown-body h2 {
    font-size: %dpx;          /* 副标题字号 */
    font-weight: 600;         /* 加粗 */
    margin: %dpx 0 %dpx 0;    /* 上下边距 */
    color: #34495e;           /* 深色副标题 */
    line-height: 1.3;         /* 副标题行高 */
}

.markdown-body h3 {
    font-size: %dpx;          /* 三级标题字号 */
    font-weight: 600;         /* 加粗 */
    margin: %dpx 0 %dpx 0;    /* 上下边距 */
    color: #34495e;           /* 深色三级标题 */
    line-height: 1.3;         /* 三级标题行高 */
}

.markdown-body h4, .markdown-body h5, .markdown-body h6 {
    font-size: %dpx;          /* 小标题字号 */
    font-weight: 600;         /* 加粗 */
    margin: %dpx 0 %dpx 0;    /* 上下边距 */
    color: #34495e;           /* 深色小标题 */
    line-height: 1.3;         /* 小标题行高 */
}

/* 段落样式：避免过窄行宽，适配卡片宽度 */
.markdown-body p {
    font-size: %dpx;          /* 段落字号 */
    margin: %dpx 0;           /* 减少段落间距，提高空间利用率 */
    line-height: 1.6;         /* 优化段落行高 */
    text-align: justify;      /* 两端对齐 */
    word-wrap: break-word;    /* 自动换行 */
    hyphens: auto;            /* 自动连字符 */
    color: #333;              /* 段落文字颜色 */
    flex: 0 0 auto;           /* 不拉伸段落 */
}

/* 列表样式：优化符号与缩进 */
.markdown-body ol, .markdown-body ul {
    font-size: %dpx;          /* 列表字号 */
    padding-left: 60px;       /* 增大列表缩进，确保数字有足够空间 */
    margin: %dpx 0;           /* 列表间距 */
}

.markdown-body li {
    font-size: %dpx;          /* 列表项字号 */
    margin: 10px 0;           /* 列表项间距 */
    line-height: 1.6;         /* 列表项行高 */
    color: #333;              /* 列表项文字颜色 */
}

/* 有序列表：确保显示数字 */
.markdown-body ol li {
    list-style-type: decimal; /* 确保显示数字 */
    padding-left: 0;          /* 移除额外内边距，让数字自然显示 */
}

/* 无序列表：优化符号样式 */
.markdown-body ul li {
    list-style-type: disc;    /* 实心圆点 */
    padding-left: 0;          /* 移除额外内边距，让符号自然显示 */
}

.markdown-body ul li::marker {
    color: #3498db;           /* 符号颜色 */
    font-weight: bold;        /* 符号加粗 */
}

/* 引用样式 */
.markdown-body blockquote {
    font-size: %dpx;          /* 引用字号 */
    margin: %dpx 0;           /* 引用间距 */
    padding: 20px 24px;       /* 引用内边距 */
    border-left: 5px solid #3498db; /* 引用左边框 */
    background: #ecf0f1;      /* 引用背景色 */
    font-style: italic;       /* 引用斜体 */
    border-radius: 0 8px 8px 0; /* 引用圆角 */
    color: #2c3e50;           /* 引用文字颜色 */
}

.markdown-body blockquote p {
    font-size: %dpx;          /* 引用段落字号 */
    margin: 0;                /* 引用段落无边距 */
    line-height: 1.6;         /* 引用段落行高 */
}

/* 代码样式 */
.markdown-body code {
    background: #f8f9fa;      /* 代码背景色 */
    padding: 4px 8px;         /* 代码内边距 */
    border-radius: 4px;       /* 代码圆角 */
    font-size: %dpx;          /* 代码字号 */
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    color: #e74c3c;           /* 代码文字颜色 */
}

.markdown-body pre {
    background: #f8f9fa;      /* 代码块背景色 */
    padding: 16px;            /* 代码块内边距 */
    border-radius: 6px;       /* 代码块圆角 */
    overflow: hidden;         /* 隐藏溢出 */
    word-wrap: break-word;    /* 自动换行 */
    white-space: pre-wrap;    /* 保留空格和换行 */
    font-size: %dpx;          /* 代码块字号 */
    margin: %dpx 0;           /* 代码块间距 */
    border: 1px solid #e9ecef; /* 代码块边框 */
}

.markdown-body pre code {
    background: transparent;  /* 代码块内代码无背景 */
    padding: 0;               /* 代码块内代码无内边距 */
    color: #333;              /* 代码块内代码颜色 */
}

/* 表格样式 */
.markdown-body table {
    width: 100%%;             /* 表格宽度 */
    margin: %dpx 0;           /* 表格间距 */
    border-collapse: collapse; /* 表格边框合并 */
    font-size: %dpx;          /* 表格字号 */
}

.markdown-body th, .markdown-body td {
    padding: 12px 16px;       /* 表格单元格内边距 */
    text-align: left;         /* 表格文字左对齐 */
    border: 1px solid #e0e0e0; /* 表格边框 */
    word-wrap: break-word;    /* 表格文字自动换行 */
}

.markdown-body th {
    background-color: #f5f5f5; /* 表头背景色 */
    font-weight: bold;        /* 表头加粗 */
    color: #2c3e50;           /* 表头文字颜色 */
}

/* 响应式适配 */
@media (max-width: 768px) {
    .markdown-body {
        font-size: %dpx;      /* 移动端基础字号 */
        padding: %dpx;        /* 移动端内边距 */
    }
    
    .markdown-body h1 {
        font-size: %dpx;      /* 移动端标题字号 */
    }
    
    .markdown-body h2 {
        font-size: %dpx;      /* 移动端副标题字号 */
    }
    
    .markdown-body p {
        font-size: %dpx;      /* 移动端段落字号 */
        max-width: 100%%;     /* 移动端段落宽度 */
    }
    
    .markdown-body ol, .markdown-body ul {
        font-size: %dpx;      /* 移动端列表字号 */
        padding-left: 24px;   /* 移动端列表缩进 */
    }
}

/* 卡片容器样式 */
.markdown-card-container {
    width: %dpx;
    height: %dpx; /* 强制固定高度 */
    padding: %dpx %dpx 80px %dpx; /* 优化内边距：上右(底部固定80px)左 */
    overflow: hidden; /* 隐藏超出部分，确保边距限制 */
    background-color: %s;
    position: relative;
    box-sizing: border-box; /* 确保padding包含在宽度内 */
    display: flex;
    flex-direction: column;
    justify-content: flex-start; /* 内容从顶部开始 */
}

/* 底部边距线条（调试用，可在生产环境移除） */
.markdown-card-container::after {
    content: '';
    position: absolute;
    bottom: 80px;
    left: 0;
    right: 0;
    height: 1px;
    background: rgba(255, 0, 0, 0.2); /* 红色半透明线，标示底部边距边界 */
    z-index: 1000;
}

.markdown-content {
    width: 100%%;
    height: 100%%; /* 填满容器 */
    overflow: visible; /* 允许内容可见 */
    max-width: 100%%; /* 确保内容不超出容器 */
    word-wrap: break-word; /* 长单词自动换行 */
    word-break: break-word; /* 中文换行优化 */
    flex: 1; /* 占用剩余空间 */
}
`, backgroundStyle, backgroundStyle, hc.config.BodyFontSize,
		hc.config.TitleFontSize, hc.config.TitleMarginBottom,
		hc.config.SubtitleFontSize, hc.config.TitleMarginBottom, hc.config.SubtitleMarginBottom,
		hc.config.SubtitleFontSize, hc.config.TitleMarginBottom, hc.config.SubtitleMarginBottom,
		hc.config.BodyFontSize, hc.config.TitleMarginBottom, hc.config.SubtitleMarginBottom,
		hc.config.BodyFontSize, int(float64(hc.config.BodyMarginBottom)*0.7), // 减少段落间距，提高空间利用
		hc.config.ListFontSize, hc.config.BodyMarginBottom,
		hc.config.ListFontSize,
		hc.config.QuoteFontSize, hc.config.BodyMarginBottom,
		hc.config.QuoteFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize, hc.config.BodyMarginBottom,
		hc.config.BodyMarginBottom, hc.config.BodyFontSize,
		// 响应式设计参数
		int(float64(hc.config.BodyFontSize)*0.9), hc.config.PaddingTop/2,
		int(float64(hc.config.TitleFontSize)*0.9),
		int(float64(hc.config.SubtitleFontSize)*0.9),
		int(float64(hc.config.BodyFontSize)*0.9),
		int(float64(hc.config.ListFontSize)*0.9),
		// 卡片容器
		hc.config.CardWidth,       // 卡片宽度
		hc.config.CardHeight,      // 固定高度
		hc.config.PaddingTop,      // 上内边距
		hc.config.PaddingRight,    // 右内边距
		hc.config.PaddingLeft,     // 左内边距
		hc.config.BackgroundColor) // 卡片背景色
}

// ConvertToClearLargeFontHTML 转换为清晰大字号风格的HTML
func (hc *HTMLConverter) ConvertToClearLargeFontHTML(markdownText, title string) (string, error) {
	// 先转换为HTML
	contentHTML, err := hc.ConvertToHTML(markdownText)
	if err != nil {
		return "", fmt.Errorf("failed to convert markdown to HTML: %v", err)
	}

	// 生成清晰大字号风格的CSS
	cssStyles := hc.generateClearLargeFontCSS()

	// 构建完整的HTML文档
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>%s</style>
</head>
<body>
    <div class="markdown-card-container">
        <div class="markdown-content markdown-body">
            %s
        </div>
    </div>
</body>
</html>`, hc.escapeHTML(title), cssStyles, contentHTML)

	return html, nil
}
