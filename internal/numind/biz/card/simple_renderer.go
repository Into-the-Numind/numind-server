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

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// SimpleRenderer 简单文本渲染器
type SimpleRenderer struct {
	config             *pagination.PaginationConfig
	templateBackground string
}

// 确保SimpleRenderer实现了RendererInterface接口
var _ RendererInterface = (*SimpleRenderer)(nil)

// NewSimpleRenderer 创建新的简单渲染器
func NewSimpleRenderer(config *pagination.PaginationConfig) *SimpleRenderer {
	return &SimpleRenderer{
		config: config,
	}
}

// SetTemplateBackground 设置模板背景
func (r *SimpleRenderer) SetTemplateBackground(background string) error {
	r.templateBackground = background
	return nil
}

// RenderCardToImage 将卡片渲染为图片
func (r *SimpleRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
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
func (r *SimpleRenderer) renderElement(img *image.RGBA, element pagination.Element, y int) int {
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
func (r *SimpleRenderer) renderTitle(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight)
}

// renderSubtitle 渲染副标题
func (r *SimpleRenderer) renderSubtitle(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight)
}

// renderBody 渲染正文
func (r *SimpleRenderer) renderBody(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("%v", content)
	// 应用上边距
	actualY := y + style.MarginTop
	return r.renderText(img, text, actualY, style.FontSize, style.Color, style.LineHeight)
}

// renderList 渲染列表
func (r *SimpleRenderer) renderList(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
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
		text := fmt.Sprintf("• %s", item)
		height := r.renderText(img, text, currentY, style.FontSize, style.Color, style.LineHeight)
		currentY += height + 8 // 列表项间距
	}

	return currentY - actualY
}

// renderQuote 渲染引用
func (r *SimpleRenderer) renderQuote(img *image.RGBA, content interface{}, y int, style *ElementStyle) int {
	text := fmt.Sprintf("「%v」", content)

	// 应用上边距
	actualY := y + style.MarginTop

	// 绘制左边框
	borderRect := image.Rect(
		r.config.Card.Padding.Left,
		actualY,
		r.config.Card.Padding.Left+4,
		actualY+100,
	)
	draw.Draw(img, borderRect, &image.Uniform{color.RGBA{30, 144, 255, 255}}, image.Point{}, draw.Src)

	return r.renderText(img, text, actualY+20, style.FontSize, style.Color, style.LineHeight) + 40
}

// renderText 渲染文本
func (r *SimpleRenderer) renderText(img *image.RGBA, text string, y int, fontSize int, colorStr string, lineHeight float64) int {
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
func (r *SimpleRenderer) wrapText(text string, maxWidth int, fontSize int) []string {
	// 简单的字符数换行
	charsPerLine := maxWidth / (fontSize / 2)
	if charsPerLine <= 0 {
		charsPerLine = 20
	}

	var lines []string
	runes := []rune(text)

	for i := 0; i < len(runes); i += charsPerLine {
		end := i + charsPerLine
		if end > len(runes) {
			end = len(runes)
		}
		lines = append(lines, string(runes[i:end]))
	}

	return lines
}

// drawTextLine 绘制单行文本
func (r *SimpleRenderer) drawTextLine(img *image.RGBA, text string, x, y int, fontSize int, textColor color.Color) {
	// 使用简单的像素绘制方法
	charWidth := fontSize / 2
	charHeight := fontSize

	for i, char := range text {
		charX := x + i*charWidth
		charY := y

		// 绘制字符的简单表示
		if char == '•' {
			// 绘制项目符号
			r.drawBullet(img, charX, charY, charWidth, charHeight, textColor)
		} else if char >= 0x4E00 && char <= 0x9FFF {
			// 中文字符，绘制一个填充的矩形
			r.drawChineseChar(img, charX, charY, charWidth, charHeight, textColor)
		} else {
			// 英文字符，绘制简单的字符表示
			r.drawEnglishChar(img, char, charX, charY, charWidth, charHeight, textColor)
		}
	}
}

// drawBullet 绘制项目符号
func (r *SimpleRenderer) drawBullet(img *image.RGBA, x, y, width, height int, color color.Color) {
	// 绘制一个圆形项目符号
	centerX := x + width/2
	centerY := y + height/2
	radius := width / 4

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= radius*radius {
				img.Set(centerX+dx, centerY+dy, color)
			}
		}
	}
}

// drawChineseChar 绘制中文字符
func (r *SimpleRenderer) drawChineseChar(img *image.RGBA, x, y, width, height int, color color.Color) {
	// 绘制一个填充的矩形来表示中文字符
	rect := image.Rect(x, y, x+width-1, y+height-1)
	draw.Draw(img, rect, image.NewUniform(color), image.Point{}, draw.Src)
}

// drawEnglishChar 绘制英文字符
func (r *SimpleRenderer) drawEnglishChar(img *image.RGBA, char rune, x, y, width, height int, color color.Color) {
	// 简单的英文字符绘制
	// 这里可以扩展为更复杂的字符绘制逻辑
	rect := image.Rect(x, y, x+width/2-1, y+height-1)
	draw.Draw(img, rect, image.NewUniform(color), image.Point{}, draw.Src)
}

// parseColor 解析颜色字符串
func (r *SimpleRenderer) parseColor(colorStr string) color.Color {
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
func (r *SimpleRenderer) drawTemplateBackground(img *image.RGBA) error {
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
func (r *SimpleRenderer) saveImageAsWebP(img *image.RGBA, cardID uint) (string, error) {
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
func (r *SimpleRenderer) getElementStyle(elementType pagination.ElementType) *ElementStyle {
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
func (r *SimpleRenderer) getDefaultStyle(elementType pagination.ElementType) *ElementStyle {
	switch elementType {
	case pagination.ElementTypeTitle:
		return &ElementStyle{
			FontSize:     64,
			LineHeight:   1.4,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	case pagination.ElementTypeSubtitle:
		return &ElementStyle{
			FontSize:     48,
			LineHeight:   1.5,
			Color:        "#666666",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 25,
		}
	case pagination.ElementTypeBody:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.6,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
		}
	case pagination.ElementTypeList:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.6,
			Color:        "#333333",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 8,
			Indent:       20,
		}
	case pagination.ElementTypeQuote:
		return &ElementStyle{
			FontSize:     36,
			LineHeight:   1.5,
			Color:        "#1E90FF",
			Align:        "justify",
			MarginTop:    0,
			MarginBottom: 30,
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
