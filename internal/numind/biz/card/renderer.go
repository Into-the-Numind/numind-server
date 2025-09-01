package card

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// RendererInterface 渲染器接口
type RendererInterface interface {
	RenderCardToImage(card *model.CardM) (*RenderedCard, error)
}

// Renderer 卡片渲染器
type Renderer struct {
	config *pagination.PaginationConfig
}

// NewRenderer 创建新的渲染器
func NewRenderer(config *pagination.PaginationConfig) *Renderer {
	return &Renderer{
		config: config,
	}
}

// RenderedCard 渲染后的卡片
type RenderedCard struct {
	CardID    uint   `json:"card_id"`
	ImageURL  string `json:"image_url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SortOrder int    `json:"sort_order"`
}

// RenderCardToImage 将卡片渲染为图片
func (r *Renderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, r.config.Card.Width, r.config.Card.Height))

	// 设置背景色
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	// 计算可用高度，确保内容不会超出边界
	availableHeight := r.config.Card.Height - r.config.Card.Padding.Top - r.config.Card.Padding.Bottom

	// 调试信息
	fmt.Printf("渲染卡片 - 卡片高度: %d, 上边距: %d, 下边距: %d, 可用高度: %d\n",
		r.config.Card.Height, r.config.Card.Padding.Top, r.config.Card.Padding.Bottom, availableHeight)

	// 渲染元素，添加边界检查
	currentY := r.config.Card.Padding.Top
	renderedElements := 0

	for i, element := range elements {
		// 预计算元素高度
		elementHeight := r.calculateElementHeight(element)

		// 检查是否会超出边界
		if currentY+elementHeight > r.config.Card.Height-r.config.Card.Padding.Bottom {
			fmt.Printf("⚠️  元素 %d [%s] 会超出卡片边界，停止渲染\n", i+1, element.Type)
			fmt.Printf("   当前Y位置: %d, 元素高度: %d, 剩余空间: %d\n",
				currentY, elementHeight, r.config.Card.Height-r.config.Card.Padding.Bottom-currentY)
			break
		}

		// 渲染元素
		actualHeight := r.renderElement(img, element, currentY)
		currentY += actualHeight
		renderedElements++

		fmt.Printf("渲染元素 %d [%s]: Y位置=%d, 高度=%d, 剩余空间=%d\n",
			i+1, element.Type, currentY, actualHeight, r.config.Card.Height-currentY)
	}

	fmt.Printf("渲染完成 - 共渲染 %d 个元素，最终Y位置: %d\n", renderedElements, currentY)

	// 保存图片
	imageURL, err := r.saveImage(img, card.ID)
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

// calculateElementHeight 计算元素高度（与分页算法保持一致）
func (r *Renderer) calculateElementHeight(element pagination.Element) int {
	style := r.getElementStyle(element.Type)

	switch element.Type {
	case pagination.ElementTypeTitle, pagination.ElementTypeSubtitle, pagination.ElementTypeBody, pagination.ElementTypeQuote:
		text := fmt.Sprintf("%v", element.Content)
		lines := r.wrapText(text, r.config.Card.Width-r.config.Card.Padding.Left-r.config.Card.Padding.Right, style.FontSize)
		lineHeight := int(float64(style.FontSize) * 1.6)
		return len(lines)*lineHeight + style.MarginTop + style.MarginBottom
	case pagination.ElementTypeList:
		var items []string
		switch v := element.Content.(type) {
		case []string:
			items = v
		case []interface{}:
			for _, item := range v {
				items = append(items, fmt.Sprintf("%v", item))
			}
		default:
			items = []string{fmt.Sprintf("%v", element.Content)}
		}

		totalHeight := style.MarginTop
		for _, item := range items {
			text := fmt.Sprintf("• %s", item)
			lines := r.wrapText(text, r.config.Card.Width-r.config.Card.Padding.Left-r.config.Card.Padding.Right, style.FontSize)
			lineHeight := int(float64(style.FontSize) * 1.6)
			totalHeight += len(lines)*lineHeight + 8 // 8像素的列表项间距
		}
		return totalHeight + style.MarginBottom
	default:
		return 50 // 默认高度
	}
}

// renderElement 渲染单个元素
func (r *Renderer) renderElement(img *image.RGBA, element pagination.Element, y int) int {
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
func (r *Renderer) renderTitle(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	return r.renderText(img, text, y, style.FontSize, style.Color, style.LineHeight)
}

// renderSubtitle 渲染副标题
func (r *Renderer) renderSubtitle(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	return r.renderText(img, text, y, style.FontSize, style.Color, style.LineHeight)
}

// renderBody 渲染正文
func (r *Renderer) renderBody(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	return r.renderText(img, text, y, style.FontSize, style.Color, style.LineHeight)
}

// renderList 渲染列表
func (r *Renderer) renderList(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
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

	currentY := y
	for _, item := range items {
		text := fmt.Sprintf("• %s", item)
		height := r.renderText(img, text, currentY, style.FontSize, style.Color, style.LineHeight)
		currentY += height + 8 // 列表项间距
	}

	return currentY - y
}

// renderQuote 渲染引用
func (r *Renderer) renderQuote(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("「%v」", content)

	// 绘制左边框
	borderRect := image.Rect(
		r.config.Card.Padding.Left,
		y,
		r.config.Card.Padding.Left+4,
		y+100,
	)
	draw.Draw(img, borderRect, &image.Uniform{color.RGBA{30, 144, 255, 255}}, image.Point{}, draw.Src)

	return r.renderText(img, text, y+20, style.FontSize, style.Color, style.LineHeight) + 40
}

// renderText 渲染文本
func (r *Renderer) renderText(img *image.RGBA, text string, y int, fontSize int, colorStr string, lineHeight float64) int {
	// 解析颜色
	textColor := r.parseColor(colorStr)

	// 计算可用宽度
	availableWidth := r.config.Card.Width - r.config.Card.Padding.Left - r.config.Card.Padding.Right

	// 简单的文本换行处理
	lines := r.wrapText(text, availableWidth, fontSize)

	currentY := y
	for _, line := range lines {
		r.drawTextLine(img, line, r.config.Card.Padding.Left, currentY, fontSize, textColor)
		currentY += int(float64(fontSize) * lineHeight)
	}

	return currentY - y
}

// wrapText 文本换行
func (r *Renderer) wrapText(text string, maxWidth int, fontSize int) []string {
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
func (r *Renderer) drawTextLine(img *image.RGBA, text string, x, y int, fontSize int, textColor color.Color) {
	// 使用基本字体渲染文本
	point := fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6((y + fontSize) * 64)}

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13,
		Dot:  point,
	}
	d.DrawString(text)
}

// parseColor 解析颜色字符串
func (r *Renderer) parseColor(colorStr string) color.Color {
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

// saveImage 保存图片
func (r *Renderer) saveImage(img *image.RGBA, cardID uint) (string, error) {
	// 获取卡片图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("调试：卡片保存目录: %s\n", cardDir)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}
	fmt.Printf("调试：目录创建成功或已存在\n")

	// 生成文件名 - 改为WebP格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("调试：文件完整路径: %s\n", filepath)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("调试：文件创建成功\n")

	// 编码为PNG
	if err := png.Encode(file, img); err != nil {
		return "", fmt.Errorf("failed to encode image: %v", err)
	}
	fmt.Printf("调试：PNG编码成功\n")

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("调试：返回的图片URL: %s\n", imageURL)
	return imageURL, nil
}

// getElementStyle 获取元素样式
func (r *Renderer) getElementStyle(elementType pagination.ElementType) *ElementStyle {
	if r.config == nil || r.config.Styles == nil {
		return r.getDefaultStyle(elementType)
	}

	style, exists := r.config.Styles[elementType]
	if !exists {
		return r.getDefaultStyle(elementType)
	}

	return &ElementStyle{
		FontSize:     style.FontSize,
		LineHeight:   float64(style.LineHeight),
		Color:        style.Color,
		Align:        style.Align,
		MarginTop:    style.MarginTop,
		MarginBottom: style.MarginBottom,
		Indent:       style.Indent,
	}
}

// getDefaultStyle 获取默认样式
func (r *Renderer) getDefaultStyle(elementType pagination.ElementType) *ElementStyle {
	switch elementType {
	case pagination.ElementTypeTitle:
		return &ElementStyle{
			FontSize:     64,
			LineHeight:   1.4,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    30, // 统一标准：30rpx
			MarginBottom: 30, // 统一标准：30rpx
		}
	case pagination.ElementTypeSubtitle:
		return &ElementStyle{
			FontSize:     48,
			LineHeight:   1.5,
			Color:        "#666666",
			Align:        "justify",
			MarginTop:    30, // 统一标准：30rpx
			MarginBottom: 30, // 统一标准：30rpx
		}
	case pagination.ElementTypeBody:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.6,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    30, // 统一标准：30rpx
			MarginBottom: 30, // 统一标准：30rpx
		}
	case pagination.ElementTypeList:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.6,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    30, // 统一标准：30rpx
			MarginBottom: 30, // 统一标准：30rpx
			Indent:       20,
		}
	case pagination.ElementTypeQuote:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.5,
			Color:        "#1E90FF",
			Align:        "justify",
			MarginTop:    30, // 统一标准：30rpx
			MarginBottom: 30, // 统一标准：30rpx
		}
	default:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.6,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	}
}

// ElementStyle 元素样式
type ElementStyle struct {
	FontSize     int     `json:"fontSize,omitempty"`
	LineHeight   float64 `json:"lineHeight,omitempty"`
	Color        string  `json:"color,omitempty"`
	Align        string  `json:"align,omitempty"`
	MarginTop    int     `json:"marginTop,omitempty"`
	MarginBottom int     `json:"marginBottom,omitempty"`
	Indent       int     `json:"indent,omitempty"`
}
