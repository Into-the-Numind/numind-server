package card

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// AdvancedRenderer 高级文本渲染器
type AdvancedRenderer struct {
	config             *pagination.PaginationConfig
	templateBackground string
}

// 确保AdvancedRenderer实现了RendererInterface接口
var _ RendererInterface = (*AdvancedRenderer)(nil)

// NewAdvancedRenderer 创建新的高级渲染器
func NewAdvancedRenderer(config *pagination.PaginationConfig) *AdvancedRenderer {
	return &AdvancedRenderer{
		config: config,
	}
}

// SetTemplateBackground 设置模板背景
func (r *AdvancedRenderer) SetTemplateBackground(background string) error {
	r.templateBackground = background
	return nil
}

// RenderCardToImage 将卡片渲染为图片
func (r *AdvancedRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, r.config.Card.Width, r.config.Card.Height))

	// 设置背景 - 优先使用模板背景
	if r.templateBackground != "" {
		// 加载模板背景图片
		if err := r.drawTemplateBackground(img); err != nil {
			// 如果模板背景加载失败，使用白色背景
			draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		}
	} else {
		// 使用白色背景
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	}

	// 渲染元素
	currentY := r.config.Card.Padding.Top
	for _, element := range elements {
		elementHeight := r.renderElement(img, element, currentY)
		currentY += elementHeight
	}

	// 保存图片 - 改为webp格式
	imageURL, err := r.saveImageAsWebP(img, card.ID)
	if err != nil {
		return nil, err
	}

	return &RenderedCard{
		CardID:    card.ID,
		ImageURL:  imageURL,
		Width:     r.config.Card.Width,
		Height:    r.config.Card.Height,
		SortOrder: card.SortOrder,
	}, nil
}

// renderElement 渲染单个元素
func (r *AdvancedRenderer) renderElement(img *image.RGBA, element pagination.Element, y int) int {
	style := r.getElementStyle(element.Type)

	switch element.Type {
	case pagination.ElementTypeTitle:
		return r.renderTitle(img, element.Content, y, style)
	case pagination.ElementTypeSubtitle:
		return r.renderSubtitle(img, element.Content, y, style)
	case pagination.ElementTypeBody:
		return r.renderBody(img, element.Content, y, style)
	case pagination.ElementTypeList:
		return r.renderList(img, element.Content, y, style)
	case pagination.ElementTypeQuote:
		return r.renderQuote(img, element.Content, y, style)
	default:
		return r.renderBody(img, element.Content, y, style)
	}
}

// renderTitle 渲染标题
func (r *AdvancedRenderer) renderTitle(img *image.RGBA, content interface{}, y int, style *pagination.StyleConfig) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight, style.MarginBottom)
}

// renderSubtitle 渲染副标题
func (r *AdvancedRenderer) renderSubtitle(img *image.RGBA, content interface{}, y int, style *pagination.StyleConfig) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight, style.MarginBottom)
}

// renderBody 渲染正文
func (r *AdvancedRenderer) renderBody(img *image.RGBA, content interface{}, y int, style *pagination.StyleConfig) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight, style.MarginBottom)
}

// renderList 渲染列表
func (r *AdvancedRenderer) renderList(img *image.RGBA, content interface{}, y int, style *pagination.StyleConfig) int {
	var items []string
	switch v := content.(type) {
	case []string:
		items = v
	case []interface{}:
		for _, item := range v {
			items = append(items, fmt.Sprintf("%v", item))
		}
	default:
		items = []string{fmt.Sprintf("%v", content)}
	}

	// 应用上边距
	actualY := y + style.MarginTop
	currentY := actualY

	for _, item := range items {
		// 添加项目符号
		text := fmt.Sprintf("• %s", item)
		height := r.renderText(img, text, currentY, style.FontSize, style.Color, style.LineHeight, 8) // 列表项间距8rpx
		currentY += height + 8
	}

	return currentY - actualY + style.MarginBottom
}

// renderQuote 渲染引用
func (r *AdvancedRenderer) renderQuote(img *image.RGBA, content interface{}, y int, style *pagination.StyleConfig) int {
	text := fmt.Sprintf("%v", content)

	// 应用上边距
	actualY := y + style.MarginTop

	// 计算引用区域
	quotePadding := 20
	quoteWidth := r.config.Card.Width - r.config.Card.Padding.Left - r.config.Card.Padding.Right
	quoteHeight := 100 // 临时高度，实际会根据内容调整

	// 绘制渐变背景
	quoteRect := image.Rect(
		r.config.Card.Padding.Left,
		actualY,
		r.config.Card.Padding.Left+quoteWidth,
		actualY+quoteHeight,
	)
	r.drawGradientBackground(img, quoteRect)

	// 绘制左边框
	borderRect := image.Rect(
		r.config.Card.Padding.Left,
		actualY,
		r.config.Card.Padding.Left+4,
		actualY+quoteHeight,
	)
	draw.Draw(img, borderRect, &image.Uniform{r.parseColor("#1E90FF")}, image.Point{}, draw.Src)

	// 渲染文本（斜体效果通过颜色和样式实现）
	height := r.renderText(img, text, actualY+quotePadding, style.FontSize, "#1E90FF", style.LineHeight, style.MarginBottom)

	return height + quotePadding*2
}

// renderText 渲染文本
func (r *AdvancedRenderer) renderText(img *image.RGBA, text string, y int, fontSize int, colorStr string, lineHeight int, marginBottom int) int {
	textColor := r.parseColor(colorStr)

	// 计算可用宽度
	availableWidth := r.config.Card.Width - r.config.Card.Padding.Left - r.config.Card.Padding.Right

	// 文本换行
	lines := r.wrapText(text, availableWidth, fontSize)

	currentY := y
	for _, line := range lines {
		r.drawTextLine(img, line, r.config.Card.Padding.Left, currentY, fontSize, textColor)
		currentY += lineHeight
	}

	return currentY - y + marginBottom
}

// wrapText 文本换行
func (r *AdvancedRenderer) wrapText(text string, maxWidth int, fontSize int) []string {
	// 使用与分页算法一致的文本换行逻辑
	// 中文字符宽度约为字体大小的1.05倍，英文字符约为0.6倍
	charWidth := float64(fontSize) * 1.05 // 以中文字符为准
	charsPerLine := int(float64(maxWidth) / charWidth)

	if charsPerLine <= 0 {
		charsPerLine = 20
	}

	var lines []string
	runes := []rune(text)
	currentLine := ""
	currentLineLength := 0.0

	for i := 0; i < len(runes); i++ {
		char := runes[i]
		charWidth := 1.0 // 默认中文字符宽度为1

		// 英文字符宽度约为中文字符的0.6倍
		if char < 128 {
			charWidth = 0.6
		}

		// 检查添加这个字符是否会超出行宽
		if currentLineLength+charWidth > float64(charsPerLine) {
			// 当前行已满，保存并开始新行
			if currentLine != "" {
				lines = append(lines, currentLine)
			}
			currentLine = string(char)
			currentLineLength = charWidth
		} else {
			// 添加到当前行
			currentLine += string(char)
			currentLineLength += charWidth
		}
	}

	// 添加最后一行
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return lines
}

// drawTextLine 绘制单行文本
func (r *AdvancedRenderer) drawTextLine(img *image.RGBA, text string, x, y int, fontSize int, textColor color.Color) {
	// 使用Go的字体库绘制文本
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13, // 使用基本字体
		Dot:  fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6((y + fontSize) * 64)},
	}

	// 绘制文本
	d.DrawString(text)
}

// drawGradientBackground 绘制渐变背景
func (r *AdvancedRenderer) drawGradientBackground(img *image.RGBA, rect image.Rectangle) {
	// 创建从左到右的渐变背景
	startColor := color.RGBA{234, 242, 255, 255} // #EAF2FF
	endColor := color.RGBA{250, 252, 255, 255}   // #FAFCFF

	for x := rect.Min.X; x < rect.Max.X; x++ {
		// 计算渐变比例
		ratio := float64(x-rect.Min.X) / float64(rect.Max.X-rect.Min.X)

		// 插值颜色
		r := uint8(float64(startColor.R)*(1-ratio) + float64(endColor.R)*ratio)
		g := uint8(float64(startColor.G)*(1-ratio) + float64(endColor.G)*ratio)
		b := uint8(float64(startColor.B)*(1-ratio) + float64(endColor.B)*ratio)

		gradientColor := color.RGBA{r, g, b, 255}

		// 绘制垂直线
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			img.Set(x, y, gradientColor)
		}
	}
}

// parseColor 解析颜色字符串
func (r *AdvancedRenderer) parseColor(colorStr string) color.Color {
	switch colorStr {
	case "#333333":
		return color.RGBA{51, 51, 51, 255}
	case "#666666":
		return color.RGBA{102, 102, 102, 255}
	case "#1E90FF":
		return color.RGBA{30, 144, 255, 255}
	default:
		return color.RGBA{51, 51, 51, 255}
	}
}

// drawTemplateBackground 绘制模板背景
func (r *AdvancedRenderer) drawTemplateBackground(img *image.RGBA) error {
	// 打开模板背景图片
	file, err := os.Open(r.templateBackground)
	if err != nil {
		return fmt.Errorf("failed to open template background: %v", err)
	}
	defer file.Close()

	// 解码背景图片
	bgImg, _, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("failed to decode template background: %v", err)
	}

	// 将背景图片缩放到卡片尺寸并绘制
	draw.Draw(img, img.Bounds(), bgImg, image.Point{}, draw.Src)
	return nil
}

// saveImageAsWebP 保存图片为webp格式
func (r *AdvancedRenderer) saveImageAsWebP(img *image.RGBA, cardID uint) (string, error) {
	// 获取卡片图片保存路径
	cardDir := util.GetCardImagePath(cardID)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}

	// 生成文件名 - 改为webp格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
	filepath := filepath.Join(cardDir, filename)

	// 创建临时PNG文件
	tempPNG := fmt.Sprintf("/tmp/temp_%d.png", time.Now().UnixNano())
	tempFile, err := os.Create(tempPNG)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %v", err)
	}

	// 将图片编码为临时PNG文件
	if err := png.Encode(tempFile, img); err != nil {
		tempFile.Close()
		os.Remove(tempPNG)
		return "", fmt.Errorf("failed to encode temp PNG: %v", err)
	}

	// 关闭临时文件，确保数据写入
	tempFile.Close()

	// 验证临时PNG文件是否创建成功
	if info, err := os.Stat(tempPNG); err != nil || info.Size() == 0 {
		os.Remove(tempPNG)
		return "", fmt.Errorf("temp PNG file creation failed or is empty: %v", err)
	}

	// 使用cwebp转换，设置高质量参数
	cmd := exec.Command("cwebp", "-q", "95", "-m", "6", "-af", "-f", "50", "-sharpness", "0", tempPNG, "-o", filepath)

	// 捕获命令输出用于调试
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tempPNG)
		return "", fmt.Errorf("failed to convert to webp: %v, output: %s", err, string(output))
	}

	// 清理临时文件
	os.Remove(tempPNG)

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	return imageURL, nil
}

// getElementStyle 获取元素样式
func (r *AdvancedRenderer) getElementStyle(elementType pagination.ElementType) *pagination.StyleConfig {
	if r.config == nil || r.config.Styles == nil {
		return r.getDefaultStyle(elementType)
	}

	style, exists := r.config.Styles[elementType]
	if !exists {
		return r.getDefaultStyle(elementType)
	}

	return &style
}

// getDefaultStyle 获取默认样式
func (r *AdvancedRenderer) getDefaultStyle(elementType pagination.ElementType) *pagination.StyleConfig {
	switch elementType {
	case pagination.ElementTypeTitle:
		return &pagination.StyleConfig{
			FontSize:     64,
			LineHeight:   96,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	case pagination.ElementTypeSubtitle:
		return &pagination.StyleConfig{
			FontSize:     48,
			LineHeight:   72,
			Color:        "#666666",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 25,
		}
	case pagination.ElementTypeBody:
		return &pagination.StyleConfig{
			FontSize:     36,
			LineHeight:   58,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	case pagination.ElementTypeList:
		return &pagination.StyleConfig{
			FontSize:     36,
			LineHeight:   54,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 8,
			Indent:       20,
		}
	case pagination.ElementTypeQuote:
		return &pagination.StyleConfig{
			FontSize:     36,
			LineHeight:   54,
			Color:        "#1E90FF",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	default:
		return &pagination.StyleConfig{
			FontSize:     36,
			LineHeight:   58,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	}
}
