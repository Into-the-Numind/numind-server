package pagination

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// DynamicPaginationConfig 动态分页配置
type DynamicPaginationConfig struct {
	*PaginationConfig
	MinHeight        int `json:"min_height"`          // 最小卡片高度
	MaxHeight        int `json:"max_height"`          // 最大卡片高度
	MinBottomPadding int `json:"min_bottom_padding"`  // 最小底部留白
	ImageMaxWidth    int `json:"image_max_width"`     // 图片最大宽度
	ImageMaxHeight   int `json:"image_max_height"`    // 图片最大高度
	MaxImagesPerCard int `json:"max_images_per_card"` // 每张卡片最大图片数
	MaxTextLength    int `json:"max_text_length"`     // 每张卡片最大文本长度
}

// DynamicPaginationEngine 动态分页引擎
type DynamicPaginationEngine struct {
	config *DynamicPaginationConfig
}

// NewDynamicPaginationEngine 创建动态分页引擎
func NewDynamicPaginationEngine(config *DynamicPaginationConfig) *DynamicPaginationEngine {
	return &DynamicPaginationEngine{
		config: config,
	}
}

// GetDynamicConfig 获取动态分页配置
func GetDynamicConfig() *DynamicPaginationConfig {
	baseConfig := GetDefaultConfig()
	return &DynamicPaginationConfig{
		PaginationConfig: baseConfig,
		MinHeight:        720,  // 最小高度（1440的一半）
		MaxHeight:        4320, // 最大高度（1440的3倍）
		MinBottomPadding: 5,    // 最小底部留白5px（进一步减少空白）
		ImageMaxWidth:    1080, // 图片最大宽度
		ImageMaxHeight:   720,  // 图片最大高度
		MaxImagesPerCard: 5,    // 每张卡片最多5张图片
		MaxTextLength:    2000, // 每张卡片最多2000字符
	}
}

// CalculateContentHeight 计算内容的实际高度
func (d *DynamicPaginationEngine) CalculateContentHeight(elements []Element) int {
	totalHeight := 0
	totalHeight += d.config.Card.Padding.Top // 顶部边距

	for _, element := range elements {
		elementHeight := d.calculateElementHeight(element)
		totalHeight += elementHeight
	}

	totalHeight += d.config.MinBottomPadding // 底部边距（优化后的最小值）

	fmt.Printf("🔍 动态高度计算：内容高度=%d, 顶部边距=%d, 底部边距=%d, 总高度=%d\n",
		totalHeight-d.config.Card.Padding.Top-d.config.MinBottomPadding,
		d.config.Card.Padding.Top,
		d.config.MinBottomPadding,
		totalHeight)

	return totalHeight
}

// calculateElementHeight 计算单个元素的高度
func (d *DynamicPaginationEngine) calculateElementHeight(element Element) int {
	style, exists := d.config.Styles[element.Type]
	if !exists {
		style = d.config.Styles[ElementTypeBody] // 默认使用正文样式
	}

	switch element.Type {
	case "image":
		return d.calculateImageHeight(element.Content)
	case "title", "subtitle", "body", "quote":
		return d.calculateTextHeight(fmt.Sprintf("%v", element.Content), style)
	case "list":
		return d.calculateListHeight(element.Content, style)
	default:
		return d.calculateTextHeight(fmt.Sprintf("%v", element.Content), style)
	}
}

// calculateImageHeight 计算图片高度
func (d *DynamicPaginationEngine) calculateImageHeight(content interface{}) int {
	// 图片高度计算：假设图片按比例缩放到卡片宽度，计算对应高度
	// 这里使用一个合理的默认高度，实际项目中可以根据图片实际尺寸计算
	baseImageHeight := 400 // 基础图片高度
	marginTop := 20        // 图片上边距
	marginBottom := 20     // 图片下边距

	return baseImageHeight + marginTop + marginBottom
}

// calculateTextHeight 计算文本高度
func (d *DynamicPaginationEngine) calculateTextHeight(text string, style StyleConfig) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}

	// 计算可用宽度（卡片宽度减去左右边距）
	availableWidth := d.config.Card.Width - d.config.Card.Padding.Left - d.config.Card.Padding.Right

	// 计算字符宽度（粗略估算：中文字符宽度约等于字体大小，英文字符约为字体大小的0.6倍）
	charWidth := float64(style.FontSize) * 0.8 // 平均字符宽度
	charsPerLine := int(float64(availableWidth) / charWidth)

	if charsPerLine <= 0 {
		charsPerLine = 1 // 防止除零错误
	}

	// 计算行数
	textLength := utf8.RuneCountInString(text)
	lines := int(math.Ceil(float64(textLength) / float64(charsPerLine)))

	if lines == 0 {
		lines = 1 // 至少一行
	}

	// 计算总高度：行数 × 行高 + 上下边距
	totalHeight := lines*style.LineHeight + style.MarginTop + style.MarginBottom

	fmt.Printf("🔍 文本高度计算：文本长度=%d, 每行字符数=%d, 行数=%d, 行高=%d, 总高度=%d\n",
		textLength, charsPerLine, lines, style.LineHeight, totalHeight)

	return totalHeight
}

// calculateListHeight 计算列表高度
func (d *DynamicPaginationEngine) calculateListHeight(content interface{}, style StyleConfig) int {
	listItems, ok := content.([]interface{})
	if !ok {
		// 如果不是列表格式，按普通文本处理
		return d.calculateTextHeight(fmt.Sprintf("%v", content), style)
	}

	totalHeight := style.MarginTop + style.MarginBottom

	for _, item := range listItems {
		itemText := fmt.Sprintf("%v", item)
		itemHeight := d.calculateTextHeight(itemText, style)
		totalHeight += itemHeight
	}

	return totalHeight
}

// CreateOptimizedPages 创建优化的分页内容
func (d *DynamicPaginationEngine) CreateOptimizedPages(elements []Element) (*PaginatedContent, error) {
	var cards []Card
	var currentCard Card
	var currentHeight int

	fmt.Printf("🚀 开始动态分页，元素总数：%d\n", len(elements))

	for i, element := range elements {
		elementHeight := d.calculateElementHeight(element)

		// 检查是否需要新建卡片
		needNewCard := false

		// 1. 第一个元素，直接添加
		if len(currentCard.Elements) == 0 {
			currentCard.Elements = append(currentCard.Elements, element)
			currentHeight = d.config.Card.Padding.Top + elementHeight + d.config.MinBottomPadding
			fmt.Printf("📄 新卡片开始，添加元素[%d] %s，当前高度：%d\n", i, element.Type, currentHeight)
			continue
		}

		// 2. 检查添加此元素后是否超出最大高度
		potentialHeight := currentHeight + elementHeight
		if potentialHeight > d.config.MaxHeight {
			needNewCard = true
			fmt.Printf("⚠️ 添加元素[%d] %s会超出最大高度(%d > %d)，创建新卡片\n",
				i, element.Type, potentialHeight, d.config.MaxHeight)
		}

		// 3. 检查是否超出内容限制
		if d.isContentOverLimit(currentCard, element) {
			needNewCard = true
			fmt.Printf("⚠️ 添加元素[%d] %s会超出内容限制，创建新卡片\n", i, element.Type)
		}

		// 4. 如果需要新卡片，保存当前卡片并开始新卡片
		if needNewCard {
			// 计算当前卡片的最终高度
			finalHeight := d.CalculateContentHeight(currentCard.Elements)
			// 确保高度符合1440的整数倍要求
			finalHeight = d.normalizeHeight(finalHeight)

			fmt.Printf("✅ 完成卡片，最终高度：%d\n", finalHeight)
			cards = append(cards, currentCard)

			// 开始新卡片
			currentCard = Card{Elements: []Element{element}}
			currentHeight = d.config.Card.Padding.Top + elementHeight + d.config.MinBottomPadding
			fmt.Printf("📄 开始新卡片，添加元素[%d] %s，当前高度：%d\n", i, element.Type, currentHeight)
		} else {
			// 添加到当前卡片
			currentCard.Elements = append(currentCard.Elements, element)
			currentHeight = potentialHeight
			fmt.Printf("➕ 添加元素[%d] %s到当前卡片，当前高度：%d\n", i, element.Type, currentHeight)
		}
	}

	// 添加最后一张卡片
	if len(currentCard.Elements) > 0 {
		finalHeight := d.CalculateContentHeight(currentCard.Elements)
		finalHeight = d.normalizeHeight(finalHeight)
		fmt.Printf("✅ 完成最后卡片，最终高度：%d\n", finalHeight)
		cards = append(cards, currentCard)
	}

	fmt.Printf("🎉 动态分页完成，总卡片数：%d\n", len(cards))

	return &PaginatedContent{Cards: cards}, nil
}

// normalizeHeight 规范化高度到1440的整数倍
func (d *DynamicPaginationEngine) normalizeHeight(height int) int {
	baseHeight := 1440

	// 如果高度小于最小高度，使用最小高度
	if height < d.config.MinHeight {
		return baseHeight
	}

	// 计算需要的倍数
	multiplier := int(math.Ceil(float64(height) / float64(baseHeight)))

	// 确保不超过最大高度
	normalizedHeight := multiplier * baseHeight
	if normalizedHeight > d.config.MaxHeight {
		normalizedHeight = d.config.MaxHeight
	}

	fmt.Printf("🔧 高度规范化：原始高度=%d, 规范化高度=%d (倍数=%d)\n",
		height, normalizedHeight, multiplier)

	return normalizedHeight
}

// isContentOverLimit 检查内容是否超出限制
func (d *DynamicPaginationEngine) isContentOverLimit(currentCard Card, newElement Element) bool {
	// 统计当前卡片的图片数量
	imageCount := 0
	textLength := 0

	for _, element := range currentCard.Elements {
		if element.Type == "image" {
			imageCount++
		} else {
			textLength += len(fmt.Sprintf("%v", element.Content))
		}
	}

	// 检查新元素
	if newElement.Type == "image" {
		imageCount++
	} else {
		textLength += len(fmt.Sprintf("%v", newElement.Content))
	}

	// 检查限制
	if imageCount > d.config.MaxImagesPerCard {
		fmt.Printf("⚠️ 图片数量超限：%d > %d\n", imageCount, d.config.MaxImagesPerCard)
		return true
	}

	if textLength > d.config.MaxTextLength {
		fmt.Printf("⚠️ 文本长度超限：%d > %d\n", textLength, d.config.MaxTextLength)
		return true
	}

	return false
}

// GetOptimizedCardHeight 获取优化的卡片高度
func (d *DynamicPaginationEngine) GetOptimizedCardHeight(elements []Element) int {
	height := d.CalculateContentHeight(elements)
	return d.normalizeHeight(height)
}
