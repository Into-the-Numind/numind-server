package markdown

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spf13/viper"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
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
	H3FontSize           int     `json:"h3_font_size"`           // 三级标题字体大小
	BodyFontSize         int     `json:"body_font_size"`         // 正文字体大小
	ListFontSize         int     `json:"list_font_size"`         // 列表字体大小
	QuoteFontSize        int     `json:"quote_font_size"`        // 引用字体大小
	TitleLineHeight      float64 `json:"title_line_height"`      // 标题行高倍数
	SubtitleLineHeight   float64 `json:"subtitle_line_height"`   // 副标题行高倍数
	H3LineHeight         float64 `json:"h3_line_height"`         // 三级标题行高倍数
	BodyLineHeight       float64 `json:"body_line_height"`       // 正文行高倍数
	TitleMarginBottom    int     `json:"title_margin_bottom"`    // 标题下边距
	SubtitleMarginBottom int     `json:"subtitle_margin_bottom"` // 副标题下边距
	H3MarginBottom       int     `json:"h3_margin_bottom"`       // 三级标题下边距
	BodyMarginBottom     int     `json:"body_margin_bottom"`     // 正文下边距
	AvailableWidth       int     `json:"available_width"`        // 可用宽度
	MaxContentHeight     int     `json:"max_content_height"`     // 最大内容高度
	// 字符宽度系数（可配置）：用于宽度估算与换行
	CharWidthFactorCJK   float64 `json:"char_width_factor_cjk"`   // CJK字符宽度系数（em）
	CharWidthFactorASCII float64 `json:"char_width_factor_ascii"` // ASCII字符宽度系数（em）

	// 段落策略："markdown"(默认，按标准Markdown) | "single_newline"(将单换行视为段落边界的兜底策略)
	ParagraphStrategy string `json:"paragraph_strategy"`
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
		FontFamily:          "'SourceHanSerifSC', 'STFangsong', 'Noto Sans SC', 'Noto Sans CJK SC', 'Microsoft YaHei', sans-serif",
		FontSize:            16,
		LineHeight:          1.6,
		Padding:             60,
		PaddingTop:          60,
		PaddingRight:        50,
		PaddingBottom:       40, // 增加底部内边距，确保文字不被遮挡
		PaddingLeft:         50,
		BackgroundColor:     "#ffffff",
		TextColor:           "#333333",
		BackgroundImage:     "", // 默认无背景图
		// 默认字符宽度系数（与现有近似一致）
		CharWidthFactorCJK:   1.05,
		CharWidthFactorASCII: 0.55,
	}

	// 从配置文件加载分页相关配置
	// 优先从新的card配置结构加载
	loadHTMLConfigFromCard(config)

	// 向后兼容：从旧的配置结构加载
	loadHTMLConfigFromLegacy(config)

	// 规范化派生尺寸：统一以卡片宽高与内边距推导可用宽度/高度，避免分页与渲染不一致
	if config.CardWidth > 0 {
		derivedWidth := config.CardWidth - config.PaddingLeft - config.PaddingRight
		if derivedWidth > 0 {
			config.AvailableWidth = derivedWidth
		}
	}
	if config.CardHeight > 0 {
		derivedHeight := config.CardHeight - config.PaddingTop - config.PaddingBottom
		if derivedHeight > 0 {
			config.MaxContentHeight = derivedHeight
		}
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
		goldmark.WithRendererOptions(renderOptionsForParagraphStrategy(config.ParagraphStrategy)...),
	)

	return &HTMLConverter{
		markdown: md,
		config:   config,
	}
}

// renderOptionsForParagraphStrategy 根据段落策略返回 Goldmark 渲染选项
func renderOptionsForParagraphStrategy(strategy string) []renderer.Option {
	s := strings.ToLower(strings.TrimSpace(strategy))
	if s == "single_newline" {
		// HardWraps: 单换行渲染为<br>，配合 normalize 兼容混合数据
		return []renderer.Option{html.WithHardWraps()}
	}
	// 默认：标准 Markdown 段落
	return []renderer.Option{}
}

// normalizeNewlinesToParagraphs 标准Markdown段落处理：只有空行（双换行）才是段落分隔
// 规则：
// - 空行（\n\n）：段落分隔
// - 单换行：段内自然换行（在同一段落内）
// - 标题、列表、引用：独立块
func normalizeNewlinesToParagraphs(input string) string {
	// 标准Markdown：双换行才是段落分隔，直接返回原文
	// Goldmark会正确处理：空行分段，单换行在段内自动wrap
	return input
}

// loadHTMLConfigFromCard 从新的card配置结构加载HTML转换器配置
func loadHTMLConfigFromCard(config *HTMLConfig) {
	// 加载卡片尺寸配置
	if viper.IsSet("card.dimensions.width") {
		config.CardWidth = viper.GetInt("card.dimensions.width")
	}
	if viper.IsSet("card.dimensions.height") {
		config.CardHeight = viper.GetInt("card.dimensions.height")
	}

	// 加载内边距配置
	if viper.IsSet("card.dimensions.padding.top") {
		config.PaddingTop = viper.GetInt("card.dimensions.padding.top")
	}
	if viper.IsSet("card.dimensions.padding.right") {
		config.PaddingRight = viper.GetInt("card.dimensions.padding.right")
	}
	if viper.IsSet("card.dimensions.padding.bottom") {
		config.PaddingBottom = viper.GetInt("card.dimensions.padding.bottom")
	}
	if viper.IsSet("card.dimensions.padding.left") {
		config.PaddingLeft = viper.GetInt("card.dimensions.padding.left")
	}

	// 加载字体配置
	if viper.IsSet("card.typography.font_family") {
		config.FontFamily = viper.GetString("card.typography.font_family")
	}

	// 加载字体大小配置
	if viper.IsSet("card.typography.sizes.title") {
		config.TitleFontSize = viper.GetInt("card.typography.sizes.title")
	}
	if viper.IsSet("card.typography.sizes.subtitle") {
		config.SubtitleFontSize = viper.GetInt("card.typography.sizes.subtitle")
	}
	// H3 默认跟随 subtitle，若提供 h3 配置则覆盖
	if viper.IsSet("card.typography.sizes.h3") {
		config.H3FontSize = viper.GetInt("card.typography.sizes.h3")
	} else {
		config.H3FontSize = config.SubtitleFontSize
	}
	if viper.IsSet("card.typography.sizes.body") {
		config.BodyFontSize = viper.GetInt("card.typography.sizes.body")
	}
	if viper.IsSet("card.typography.sizes.list") {
		config.ListFontSize = viper.GetInt("card.typography.sizes.list")
	}
	if viper.IsSet("card.typography.sizes.quote") {
		config.QuoteFontSize = viper.GetInt("card.typography.sizes.quote")
	}

	// 加载行高配置（将像素值转换为倍数）
	if viper.IsSet("card.typography.line_heights.title") && viper.IsSet("card.typography.sizes.title") {
		lineHeight := viper.GetInt("card.typography.line_heights.title")
		fontSize := viper.GetInt("card.typography.sizes.title")
		if fontSize > 0 {
			config.TitleLineHeight = float64(lineHeight) / float64(fontSize)
		}
	}
	if viper.IsSet("card.typography.line_heights.subtitle") && viper.IsSet("card.typography.sizes.subtitle") {
		lineHeight := viper.GetInt("card.typography.line_heights.subtitle")
		fontSize := viper.GetInt("card.typography.sizes.subtitle")
		if fontSize > 0 {
			config.SubtitleLineHeight = float64(lineHeight) / float64(fontSize)
		}
	}
	// H3 行高，默认跟随 subtitle，若提供 h3 配置则覆盖
	if viper.IsSet("card.typography.line_heights.h3") && viper.IsSet("card.typography.sizes.h3") {
		lineHeight := viper.GetInt("card.typography.line_heights.h3")
		fontSize := viper.GetInt("card.typography.sizes.h3")
		if fontSize > 0 {
			config.H3LineHeight = float64(lineHeight) / float64(fontSize)
		}
	} else {
		config.H3LineHeight = config.SubtitleLineHeight
	}
	if viper.IsSet("card.typography.line_heights.body") && viper.IsSet("card.typography.sizes.body") {
		lineHeight := viper.GetInt("card.typography.line_heights.body")
		fontSize := viper.GetInt("card.typography.sizes.body")
		if fontSize > 0 {
			config.BodyLineHeight = float64(lineHeight) / float64(fontSize)
		}
	}

	// 加载边距配置
	if viper.IsSet("card.typography.spacing.base_margin") {
		baseMargin := viper.GetInt("card.typography.spacing.base_margin")
		config.TitleMarginBottom = baseMargin
		config.SubtitleMarginBottom = baseMargin
		config.BodyMarginBottom = baseMargin
	}
	// H3 下边距，默认跟随 subtitle，若提供 h3_bottom 则覆盖
	if viper.IsSet("card.typography.spacing.margins.h3_bottom") {
		config.H3MarginBottom = viper.GetInt("card.typography.spacing.margins.h3_bottom")
	} else {
		config.H3MarginBottom = config.SubtitleMarginBottom
	}

	// 加载HTML转换特有配置（从html_converter节点）
	if viper.IsSet("html_converter.available_width") {
		config.AvailableWidth = viper.GetInt("html_converter.available_width")
	}
	if viper.IsSet("html_converter.max_content_height") {
		config.MaxContentHeight = viper.GetInt("html_converter.max_content_height")
	}
	// 加载分页估算相关（兼容card.pagination配置）
	if viper.IsSet("card.pagination.char_width_factor") {
		config.CharWidthFactorCJK = viper.GetFloat64("card.pagination.char_width_factor")
	}
	if viper.IsSet("card.pagination.char_width_factor_ascii") {
		config.CharWidthFactorASCII = viper.GetFloat64("card.pagination.char_width_factor_ascii")
	}
	// 段落策略
	if viper.IsSet("card.typography.paragraph_strategy") {
		config.ParagraphStrategy = viper.GetString("card.typography.paragraph_strategy")
	}
}

// loadHTMLConfigFromLegacy 从旧配置结构加载HTML转换器配置（向后兼容）
func loadHTMLConfigFromLegacy(config *HTMLConfig) {
	// 从html_converter配置加载
	if viper.IsSet("html_converter.card.width") {
		config.CardWidth = viper.GetInt("html_converter.card.width")
	}
	if viper.IsSet("html_converter.card.height") {
		config.CardHeight = viper.GetInt("html_converter.card.height")
	}
	if viper.IsSet("html_converter.card.padding") {
		config.Padding = viper.GetInt("html_converter.card.padding")
	}

	// 从pagination配置中加载内边距设置
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
	}
	if viper.IsSet("html_converter.fonts.subtitle_size") {
		config.SubtitleFontSize = viper.GetInt("html_converter.fonts.subtitle_size")
	}
	if viper.IsSet("html_converter.fonts.body_size") {
		config.BodyFontSize = viper.GetInt("html_converter.fonts.body_size")
	}
	if viper.IsSet("html_converter.fonts.list_size") {
		config.ListFontSize = viper.GetInt("html_converter.fonts.list_size")
	}
	if viper.IsSet("html_converter.fonts.quote_size") {
		config.QuoteFontSize = viper.GetInt("html_converter.fonts.quote_size")
	}

	if viper.IsSet("html_converter.line_heights.title") {
		config.TitleLineHeight = viper.GetFloat64("html_converter.line_heights.title")
	}
	if viper.IsSet("html_converter.line_heights.subtitle") {
		config.SubtitleLineHeight = viper.GetFloat64("html_converter.line_heights.subtitle")
	}
	if viper.IsSet("html_converter.line_heights.body") {
		config.BodyLineHeight = viper.GetFloat64("html_converter.line_heights.body")
	}

	// 优先从新路径加载（card.typography.spacing.margins），向后兼容旧路径
	if viper.IsSet("card.typography.spacing.margins.body_bottom") {
		config.BodyMarginBottom = viper.GetInt("card.typography.spacing.margins.body_bottom")
	} else if viper.IsSet("html_converter.margins.body_bottom") {
		config.BodyMarginBottom = viper.GetInt("html_converter.margins.body_bottom")
	}

	if viper.IsSet("card.typography.spacing.margins.subtitle_bottom") {
		config.SubtitleMarginBottom = viper.GetInt("card.typography.spacing.margins.subtitle_bottom")
	} else if viper.IsSet("html_converter.margins.subtitle_bottom") {
		config.SubtitleMarginBottom = viper.GetInt("html_converter.margins.subtitle_bottom")
	}

	if viper.IsSet("card.typography.spacing.margins.title_bottom") {
		config.TitleMarginBottom = viper.GetInt("card.typography.spacing.margins.title_bottom")
	} else if viper.IsSet("html_converter.margins.title_bottom") {
		config.TitleMarginBottom = viper.GetInt("html_converter.margins.title_bottom")
	}

	if viper.IsSet("html_converter.pagination.available_width") {
		config.AvailableWidth = viper.GetInt("html_converter.pagination.available_width")
	}
	if viper.IsSet("html_converter.pagination.max_content_height") {
		config.MaxContentHeight = viper.GetInt("html_converter.pagination.max_content_height")
	}
	// 旧配置兼容：字符宽度系数
	if viper.IsSet("pagination.char_width_factor") {
		config.CharWidthFactorCJK = viper.GetFloat64("pagination.char_width_factor")
	}
	if viper.IsSet("pagination.char_width_factor_ascii") {
		config.CharWidthFactorASCII = viper.GetFloat64("pagination.char_width_factor_ascii")
	}
}

// ConvertToHTML 将 Markdown 转换为 HTML
func (hc *HTMLConverter) ConvertToHTML(markdownText string) (string, error) {
	// 强制将单换行视为段落边界（绕过配置，直接应用）
	text := normalizeNewlinesToParagraphs(markdownText)

	var buf bytes.Buffer
	if err := hc.markdown.Convert([]byte(text), &buf); err != nil {
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

// preprocessMarkdownForCorrectHeadingDetection 预处理markdown内容，确保只有真正以 ## 开头的内容才被识别为二级标题
func (hc *HTMLConverter) preprocessMarkdownForCorrectHeadingDetection(markdownText string) string {
	lines := strings.Split(markdownText, "\n")
	var processedLines []string

	for _, line := range lines {
		// 检查是否是以 ## 开头的真正二级标题
		if strings.HasPrefix(line, "## ") {
			// 这是真正的二级标题，保持不变
			processedLines = append(processedLines, line)
		} else if strings.HasPrefix(line, "# ") {
			// 这是一级标题，保持不变
			processedLines = append(processedLines, line)
		} else {
			// 其他内容，确保不会被误识别为标题
			// 特别处理：如果内容以冒号结尾且看起来像标题，但实际不是以 ## 开头，则强制转换为普通段落
			trimmedLine := strings.TrimSpace(line)
			if strings.HasSuffix(trimmedLine, ":") && len(trimmedLine) > 5 && len(trimmedLine) < 50 {
				// 这可能是被误识别为标题的内容，强制添加段落标记
				processedLines = append(processedLines, line)
			} else {
				processedLines = append(processedLines, line)
			}
		}
	}

	return strings.Join(processedLines, "\n")
}

// fixIncorrectHeadingDetection 修复被错误识别为标题的内容
func (hc *HTMLConverter) fixIncorrectHeadingDetection(htmlContent, originalMarkdown string) string {
	// 分析原始markdown内容，找出哪些内容被错误识别为标题
	lines := strings.Split(originalMarkdown, "\n")

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// 跳过空行和真正的标题
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "# ") || strings.HasPrefix(trimmedLine, "## ") {
			continue
		}

		// 检查这一行是否被错误识别为标题
		// 如果原始内容没有标题前缀，但HTML中有对应的h2标签，则修复
		if hc.isLineIncorrectlyIdentifiedAsHeading(trimmedLine, htmlContent) {
			htmlContent = hc.fixSpecificIncorrectHeading(trimmedLine, htmlContent)
		}
	}

	return htmlContent
}

// isLineIncorrectlyIdentifiedAsHeading 检查某一行是否被错误识别为标题
func (hc *HTMLConverter) isLineIncorrectlyIdentifiedAsHeading(line, htmlContent string) bool {
	// 如果原始行没有标题前缀，但HTML中包含对应的h2标签，则认为是错误识别
	if !strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "# ") {
		// 转义特殊字符用于正则表达式
		escapedLine := regexp.QuoteMeta(line)
		// 检查HTML中是否有对应的h2标签
		pattern := fmt.Sprintf(`<h2[^>]*>%s`, escapedLine)
		re := regexp.MustCompile(pattern)
		return re.MatchString(htmlContent)
	}
	return false
}

// fixSpecificIncorrectHeading 修复特定被错误识别的标题
func (hc *HTMLConverter) fixSpecificIncorrectHeading(line, htmlContent string) string {
	// 转义特殊字符用于正则表达式
	escapedLine := regexp.QuoteMeta(line)

	// 匹配h2标签及其内容，直到遇到</h2>
	pattern := fmt.Sprintf(`<h2[^>]*>%s([^<]*(?:<br>[^<]*)*)</h2>`, escapedLine)
	re := regexp.MustCompile(pattern)

	// 替换为段落标签
	replacement := fmt.Sprintf(`<p>%s$1</p>`, line)

	return re.ReplaceAllString(htmlContent, replacement)
}

// ConvertMarkdownCardToHTML 将Markdown内容转换为卡片HTML
func (hc *HTMLConverter) ConvertMarkdownCardToHTML(markdownText, title string, cardIndex int) string {
	// 添加调试日志，查看输入的markdown内容
	log.C(context.Background()).Infow("🔍 ConvertMarkdownCardToHTML 输入内容",
		"card_index", cardIndex,
		"markdown_length", len(markdownText),
		"markdown_content", markdownText)

	// 预处理markdown内容，修复标题识别问题
	processedMarkdown := hc.preprocessMarkdownForCorrectHeadingDetection(markdownText)

	// 正文卡片：移除所有 H1 行，确保 H1 仅出现在封面
	if cardIndex > 0 {
		processedMarkdown = hc.stripH1Only(processedMarkdown)
	}

	// 添加调试日志，查看预处理后的markdown内容
	log.C(context.Background()).Infow("🔍 ConvertMarkdownCardToHTML 预处理后内容",
		"card_index", cardIndex,
		"processed_length", len(processedMarkdown),
		"processed_content", processedMarkdown)

	// 转换markdown为HTML
	contentHTML, err := hc.ConvertToHTML(processedMarkdown)
	if err != nil {
		contentHTML = fmt.Sprintf("<p>%s</p>", hc.escapeHTML(processedMarkdown))
	}

	// 后处理：修复被错误识别为标题的内容
	contentHTML = hc.fixIncorrectHeadingDetection(contentHTML, markdownText)

	// 添加调试日志，查看转换后的HTML内容
	log.C(context.Background()).Infow("🔍 ConvertMarkdownCardToHTML 转换结果",
		"card_index", cardIndex,
		"html_length", len(contentHTML),
		"html_content", contentHTML)

	// 使用清晰大字号风格包装为卡片HTML
	return hc.wrapWithClearLargeFontStyles(contentHTML, title, cardIndex)
}

// stripH1Only 去除所有以 "# " 开头的一级标题行
func (hc *HTMLConverter) stripH1Only(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "# ") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
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
    font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
    font-size: %dpx;
    font-weight: bold;
    text-align: justify;
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
	normalCSS := fmt.Sprintf(` //nolint
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
		hc.config.H3FontSize,
		hc.config.H3MarginBottom,
		hc.config.H3MarginBottom,
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
		hc.config.BodyMarginBottom, // table margin
		hc.config.BodyFontSize,     // table font-size
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
    width: 100%%;
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
		// body
		hc.config.FontFamily,
		hc.config.FontSize,
		hc.config.LineHeight,
		hc.config.TextColor,
		hc.config.BackgroundColor,
		// container
		hc.config.CardWidth,
		hc.config.CardHeight,
		hc.config.PaddingTop,
		hc.config.PaddingRight,
		hc.config.PaddingBottom,
		hc.config.PaddingLeft,
		hc.config.BackgroundColor,
		// headings common margins
		hc.config.TitleMarginBottom,
		hc.config.TitleMarginBottom,
		// h1..h6 sizes
		hc.config.TitleFontSize,
		hc.config.SubtitleFontSize,
		hc.config.SubtitleFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		hc.config.BodyFontSize,
		// paragraph
		hc.config.BodyFontSize,
		hc.config.BodyMarginBottom,
		// lists
		hc.config.ListFontSize,
		hc.config.BodyMarginBottom,
		// li
		hc.config.ListFontSize,
		// blockquote
		hc.config.QuoteFontSize,
		hc.config.BodyMarginBottom,
		// blockquote p
		hc.config.QuoteFontSize,
		// code
		hc.config.BodyFontSize,
	)
}

// wrapWithFixedMarginStyles 包装 HTML 内容并添加固定边距样式
func (hc *HTMLConverter) wrapWithFixedMarginStyles(contentHTML, title string) string {
	cssStyles := hc.generateFixedMarginCSS()

	//nolint:staticcheck
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

	//nolint:staticcheck
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
		hc.config.BodyMarginBottom,
		hc.config.BodyMarginBottom,
		fixedMargin, fixedMargin)
}

// SplitContentByHeight 根据内容高度和固定边距进行精准分页 - 优化版
func (hc *HTMLConverter) SplitContentByHeight(markdownText string) ([]string, error) {
	// 使用配置中的卡片高度和边距
	cardHeight := hc.config.CardHeight
	fixedMargin := hc.config.PaddingTop + hc.config.PaddingBottom
	// 优先使用配置的最大内容高度
	maxContentHeight := hc.config.MaxContentHeight
	if maxContentHeight <= 0 {
		maxContentHeight = cardHeight - fixedMargin
	}

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
			elementHeight = hc.calculateTextHeight(text, hc.config.H3FontSize, hc.config.AvailableWidth, hc.config.H3LineHeight)
			marginBottom = hc.config.H3MarginBottom
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

	// 使用贪心逐行分页算法
	return hc.splitContentGreedyLineWrap(lines, effectiveMaxHeight, maxContentHeight)
}

// splitContentGreedyLineWrap 按段落分页：保持Markdown段落结构，让浏览器自动换行
func (hc *HTMLConverter) splitContentGreedyLineWrap(lines []string, _, maxContentHeight int) ([]string, error) {
	var cards []string
	var currentCard strings.Builder
	currentHeight := 0

	// 首页面仅含H1：仅当第一条非空行是H1时启用
	enforceCoverH1Only := firstNonEmptyIsH1(lines)
	isFirstCard := true

	// 将lines重新组合为段落，保持原始Markdown结构
	paragraphs := hc.groupLinesIntoParagraphs(lines)

	for idx, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" || paragraph == "\"" || paragraph == "'" {
			continue
		}

		// 非首页：彻底跳过所有 H1 段落，避免生成"只含H1"的空内容卡
		if !isFirstCard && strings.HasPrefix(paragraph, "# ") {
			continue
		}

		// 首页面规则：仅当首页且启用了封面规则且当前段落非H1时，跳过到H1出现后再处理
		if isFirstCard && enforceCoverH1Only && !strings.HasPrefix(paragraph, "# ") {
			// 直到遇到H1才开始往首页写入；非H1延后至H1之后的卡片
			continue
		}

		// 计算整个段落的高度（不破坏段落结构）
		paragraphHeight := hc.calculateParagraphHeight(paragraph)
		if paragraphHeight == 0 {
			continue
		}

		// 检查是否需要分页
		if currentHeight+paragraphHeight > maxContentHeight && currentCard.Len() > 0 {
			// 当前段落放不下，结束当前卡片
			content := strings.TrimSpace(currentCard.String())
			if content != "" {
				cards = append(cards, content)
				utilization := float64(currentHeight) / float64(maxContentHeight) * 100
				log.C(context.Background()).Infow("📄 卡片创建(段落分页)",
					"cards_so_far", len(cards),
					"card_height", currentHeight,
					"utilization", fmt.Sprintf("%.1f%%", utilization))
			}
			currentCard.Reset()
			currentHeight = 0
			if isFirstCard {
				isFirstCard = false
			}
		}

		// 超高段落：强制放置（去掉段尾margin）
		if paragraphHeight > maxContentHeight && currentHeight == 0 {
			// 对于超高的段落，仍然保持段落结构，让浏览器处理
			paragraphHeight = hc.calculateParagraphHeightWithoutMargin(paragraph)
		}

		// 添加段落到当前卡片
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n\n") // 段落分隔符
		}
		currentCard.WriteString(paragraph)
		currentHeight += paragraphHeight

		// 若这是H1段落并且首页规则启用：H1段落后立即结束首页
		if isFirstCard && enforceCoverH1Only && strings.HasPrefix(paragraph, "# ") {
			// 下一段落若不是H1或已经到末尾，则收尾首页
			nextIsH1 := false
			// 判断下一有效段落是否仍是H1
			for j := idx + 1; j < len(paragraphs); j++ {
				nextParagraph := strings.TrimSpace(paragraphs[j])
				if nextParagraph == "" || nextParagraph == "\"" || nextParagraph == "'" {
					continue
				}
				if strings.HasPrefix(nextParagraph, "# ") {
					nextIsH1 = true
				}
				break
			}
			if !nextIsH1 {
				// 结束首页卡片
				if currentCard.Len() > 0 {
					content := strings.TrimSpace(currentCard.String())
					if content != "" {
						cards = append(cards, content)
						utilization := float64(currentHeight) / float64(maxContentHeight) * 100
						log.C(context.Background()).Infow("📄 首页面创建(仅H1)",
							"cards_so_far", len(cards),
							"card_height", currentHeight,
							"utilization", fmt.Sprintf("%.1f%%", utilization))
					}
					currentCard.Reset()
					currentHeight = 0
				}
				isFirstCard = false
			}
		}
	}

	if currentCard.Len() > 0 {
		content := strings.TrimSpace(currentCard.String())
		if content != "" {
			cards = append(cards, content)
			utilization := float64(currentHeight) / float64(maxContentHeight) * 100
			log.C(context.Background()).Infow("📄 最后卡片创建(段落分页)",
				"cards_total", len(cards),
				"card_height", currentHeight,
				"utilization", fmt.Sprintf("%.1f%%", utilization))
		}
	}
	if len(cards) == 0 {
		cards = append(cards, strings.Join(lines, "\n"))
	}
	return cards, nil
}

// groupLinesIntoParagraphs 将行重新组合为段落，保持原始Markdown结构
func (hc *HTMLConverter) groupLinesIntoParagraphs(lines []string) []string {
	var paragraphs []string
	var currentParagraph strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 跳过空行和无效行
		if line == "" || line == "\"" || line == "'" {
			// 如果当前段落不为空，结束当前段落
			if currentParagraph.Len() > 0 {
				paragraphs = append(paragraphs, currentParagraph.String())
				currentParagraph.Reset()
			}
			continue
		}

		// 检查是否是标题行（独立成段）
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			// 如果当前段落不为空，先结束当前段落
			if currentParagraph.Len() > 0 {
				paragraphs = append(paragraphs, currentParagraph.String())
				currentParagraph.Reset()
			}
			// 标题独立成段
			paragraphs = append(paragraphs, line)
			continue
		}

		// 检查是否是列表项（独立成段）
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "> ") {
			// 如果当前段落不为空，先结束当前段落
			if currentParagraph.Len() > 0 {
				paragraphs = append(paragraphs, currentParagraph.String())
				currentParagraph.Reset()
			}
			// 列表项独立成段
			paragraphs = append(paragraphs, line)
			continue
		}

		// 普通文本行，添加到当前段落
		if currentParagraph.Len() > 0 {
			currentParagraph.WriteString(" ") // 段落内用空格连接
		}
		currentParagraph.WriteString(line)
	}

	// 处理最后一个段落
	if currentParagraph.Len() > 0 {
		paragraphs = append(paragraphs, currentParagraph.String())
	}

	return paragraphs
}

// calculateParagraphHeight 计算整个段落的高度（包含margin）
func (hc *HTMLConverter) calculateParagraphHeight(paragraph string) int {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return 0
	}

	// 根据段落类型计算高度
	if strings.HasPrefix(paragraph, "# ") {
		// H1标题
		text := strings.TrimSpace(paragraph[2:])
		contentHeight := hc.calculateTextHeight(text, hc.config.TitleFontSize, hc.config.AvailableWidth, hc.config.TitleLineHeight)
		return contentHeight + hc.config.TitleMarginBottom
	} else if strings.HasPrefix(paragraph, "## ") {
		// H2标题
		text := strings.TrimSpace(paragraph[3:])
		contentHeight := hc.calculateTextHeight(text, hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
		return contentHeight + hc.config.SubtitleMarginBottom
	} else if strings.HasPrefix(paragraph, "### ") {
		// H3标题
		text := strings.TrimSpace(paragraph[4:])
		contentHeight := hc.calculateTextHeight(text, hc.config.H3FontSize, hc.config.AvailableWidth, hc.config.H3LineHeight)
		return contentHeight + hc.config.H3MarginBottom
	} else if strings.HasPrefix(paragraph, "- ") || strings.HasPrefix(paragraph, "* ") {
		// 列表项
		text := strings.TrimSpace(paragraph[2:])
		contentHeight := hc.calculateTextHeight(text, hc.config.ListFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
		return contentHeight + hc.config.BodyMarginBottom
	} else if strings.HasPrefix(paragraph, "> ") {
		// 引用
		text := strings.TrimSpace(paragraph[2:])
		contentHeight := hc.calculateTextHeight(text, hc.config.QuoteFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
		return contentHeight + hc.config.BodyMarginBottom
	} else {
		// 普通段落
		contentHeight := hc.calculateTextHeight(paragraph, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
		return contentHeight + hc.config.BodyMarginBottom
	}
}

// calculateParagraphHeightWithoutMargin 计算段落高度（不包含margin）
func (hc *HTMLConverter) calculateParagraphHeightWithoutMargin(paragraph string) int {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return 0
	}

	// 根据段落类型计算高度（不包含margin）
	if strings.HasPrefix(paragraph, "# ") {
		// H1标题
		text := strings.TrimSpace(paragraph[2:])
		return hc.calculateTextHeight(text, hc.config.TitleFontSize, hc.config.AvailableWidth, hc.config.TitleLineHeight)
	} else if strings.HasPrefix(paragraph, "## ") {
		// H2标题
		text := strings.TrimSpace(paragraph[3:])
		return hc.calculateTextHeight(text, hc.config.SubtitleFontSize, hc.config.AvailableWidth, hc.config.SubtitleLineHeight)
	} else if strings.HasPrefix(paragraph, "### ") {
		// H3标题
		text := strings.TrimSpace(paragraph[4:])
		return hc.calculateTextHeight(text, hc.config.H3FontSize, hc.config.AvailableWidth, hc.config.H3LineHeight)
	} else if strings.HasPrefix(paragraph, "- ") || strings.HasPrefix(paragraph, "* ") {
		// 列表项
		text := strings.TrimSpace(paragraph[2:])
		return hc.calculateTextHeight(text, hc.config.ListFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
	} else if strings.HasPrefix(paragraph, "> ") {
		// 引用
		text := strings.TrimSpace(paragraph[2:])
		return hc.calculateTextHeight(text, hc.config.QuoteFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
	} else {
		// 普通段落
		return hc.calculateTextHeight(paragraph, hc.config.BodyFontSize, hc.config.AvailableWidth, hc.config.BodyLineHeight)
	}
}

// firstNonEmptyIsH1 判断首个非空行是否为H1
func firstNonEmptyIsH1(lines []string) bool {
	for _, raw := range lines {
		t := strings.TrimSpace(raw)
		if t == "" || t == "\"" || t == "'" {
			continue
		}
		return strings.HasPrefix(t, "# ")
	}
	return false
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

			// 保守的高度利用：允许80%-90%的剩余高度，保留安全边距
			heightRatio := float64(candidateHeight) / float64(remainingHeight)
			if heightRatio >= 0.80 && heightRatio <= 0.90 {
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
				if heightRatio >= 0.80 && heightRatio <= 0.90 {
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

	// 计算字符宽度（使用可配置的CJK宽度系数近似）
	charWidth := float64(fontSize) * hc.config.CharWidthFactorCJK
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
	//nolint:staticcheck
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
		hc.config.BodyMarginBottom,
		hc.config.BodyFontSize,
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

	//nolint:staticcheck
	return fmt.Sprintf(`
/* 思源宋体字体定义 - 容器环境优化 */
@font-face {
    font-family: "SourceHanSerifSC";
    src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf") format("opentype"),
         local("Source Han Serif SC"),
         local("SourceHanSerifSC"),
         local("STFangsong"),
         local("Source Han Sans CN"),
         local("Noto Sans CJK SC"),
         local("PingFang SC"),
         local("Hiragino Sans GB"),
         local("Microsoft YaHei"),
         local("sans-serif");
    font-weight: normal;
    font-style: normal;
}

@font-face {
    font-family: "SourceHanSerifSC";
    src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf") format("opentype"),
         local("Source Han Serif SC Bold"),
         local("SourceHanSerifSC-Bold"),
         local("STFangsong"),
         local("Source Han Sans CN Bold"),
         local("Noto Sans CJK SC Semibold"),
         local("PingFang SC"),
         local("Hiragino Sans GB"),
         local("Microsoft YaHei Bold"),
         local("sans-serif");
    font-weight: bold;
    font-style: normal;
}

/* 全局基础样式 - 清晰大字号风格 */
body {
    font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
    font-size: 16px;          /* 基础字号 */
    line-height: 1.8;         /* 增大行高，提升可读性 */
    color: #333;              /* 深灰色文字，替代默认黑色 */
    margin: 0;
    padding: 0;
    width: 1080px;
    height: 1440px;
    overflow: visible;
    %s
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}

    .markdown-card-container {
        width: 100%%;
        height: 100%%;
        padding: %dpx %dpx %dpx %dpx;       /* 上右下左内边距（来自配置，左右对称） */
    box-sizing: border-box;
    %s
    background-size: cover !important;
    background-position: center center !important;
    background-repeat: no-repeat !important;
}

.markdown-body {
    font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
    font-size: %dpx;          /* 基础字号 */
    line-height: 1.6;         /* 优化行高，平衡可读性和空间利用 */
    color: #333;              /* 深灰色文字，替代默认黑色 */
    padding: 0;               /* 移除内边距，由容器控制 */
    width: 100%%;
    /* 移除高度限制，允许内容完整显示 */
    overflow: visible;        /* 允许内容可见，不隐藏超出部分 */
    word-wrap: break-word;
    word-break: break-word;   /* 中文换行优化 */
    hyphens: auto;
    box-sizing: border-box;   /* 确保盒模型正确 */
    /* 移除 flex 布局，恢复标准块级布局以正确显示段间距 */
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
        margin: %dpx 0;           /* 段落间距：与分页使用的段后距一致 */
        line-height: 1.6;         /* 优化段落行高 */
        text-align: justify;      /* 两端对齐 */
            word-wrap: break-word;    /* 自动换行 */
    hyphens: auto;            /* 自动连字符 */
    color: #333;              /* 段落文字颜色 */
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
        height: %dpx; /* 固定高度 */
        padding: %dpx %dpx %dpx %dpx; /* 上右下左：来自配置，底部不再强制80，避免过多留空 */
    overflow: visible; /* 允许内容可见，不隐藏超出部分 */
    background-color: %s;
    position: relative;
    box-sizing: border-box; /* 确保padding包含在宽度内 */
    /* 移除 flex 布局，恢复标准块级布局以正确显示段间距 */
}

/* 底部边距线条已移除 */

.markdown-content {
    width: 100%%;
    height: 100%%; /* 填满容器 */
    overflow: visible; /* 允许内容可见 */
    max-width: 100%%; /* 确保内容不超出容器 */
    word-wrap: break-word; /* 长单词自动换行 */
    word-break: break-word; /* 中文换行优化 */
    flex: 1; /* 占用剩余空间 */
}
`, backgroundStyle,
		hc.config.PaddingTop, hc.config.PaddingRight, hc.config.PaddingBottom, hc.config.PaddingLeft,
		backgroundStyle,
		hc.config.BodyFontSize,
		hc.config.TitleFontSize, hc.config.TitleMarginBottom,
		hc.config.SubtitleFontSize, hc.config.SubtitleMarginBottom, hc.config.SubtitleMarginBottom,
		hc.config.SubtitleFontSize, hc.config.SubtitleMarginBottom, hc.config.SubtitleMarginBottom,
		hc.config.BodyFontSize, hc.config.BodyMarginBottom, hc.config.BodyMarginBottom,
		hc.config.BodyFontSize, hc.config.BodyMarginBottom,
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
		hc.config.PaddingBottom,   // 底内边距（使用配置值，避免过多留空）
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
