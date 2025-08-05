package pagination

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ElementType 定义元素类型
type ElementType string

const (
	ElementTypeTitle    ElementType = "title"
	ElementTypeSubtitle ElementType = "subtitle"
	ElementTypeBody     ElementType = "body"
	ElementTypeList     ElementType = "list"
	ElementTypeQuote    ElementType = "quote"
	ElementTypeTag      ElementType = "tag"
	ElementTypeNumber   ElementType = "number"
)

// Element 定义内容元素
type Element struct {
	Type    ElementType `json:"type"`
	Content interface{} `json:"content"`
}

// Card 定义卡片结构
type Card struct {
	Elements []Element `json:"elements"`
}

// PaginatedContent 定义分页后的内容结构
type PaginatedContent struct {
	Cards []Card `json:"cards"`
}

// StyleConfig 定义样式配置
type StyleConfig struct {
	FontSize     int    `json:"fontSize"`
	LineHeight   int    `json:"lineHeight"`
	MarginTop    int    `json:"marginTop"`
	MarginBottom int    `json:"marginBottom"`
	Indent       int    `json:"indent,omitempty"`
	Color        string `json:"color,omitempty"`
	Align        string `json:"align,omitempty"`
}

// CardConfig 定义卡片配置
type CardConfig struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Padding struct {
		Top    int `json:"top"`
		Right  int `json:"right"`
		Bottom int `json:"bottom"`
		Left   int `json:"left"`
	} `json:"padding"`
}

// PaginationConfig 定义分页配置
type PaginationConfig struct {
	Card   CardConfig                  `json:"card"`
	Styles map[ElementType]StyleConfig `json:"styles"`
}

// PaginationEngine 分页引擎
type PaginationEngine struct {
	config *PaginationConfig
}

// NewPaginationEngine 创建新的分页引擎
func NewPaginationEngine(config *PaginationConfig) *PaginationEngine {
	return &PaginationEngine{
		config: config,
	}
}

// calculateTextHeight 计算文本高度
func (p *PaginationEngine) calculateTextHeight(text string, style StyleConfig) int {
	if text == "" {
		return 0
	}

	// 计算可用宽度
	availableWidth := p.config.Card.Width - p.config.Card.Padding.Left - p.config.Card.Padding.Right

	// 如果是列表项，需要考虑缩进
	if style.Indent > 0 {
		availableWidth -= style.Indent
	}

	// 更精确的字符宽度计算（基于中文字符）
	// 中文字符宽度约为字体大小的1.1倍，英文字符约为0.6倍
	charWidth := float64(style.FontSize) * 1.1 // 以中文字符为准
	charsPerLine := int(float64(availableWidth) / charWidth)

	// 计算行数
	lines := p.splitTextIntoLines(text, charsPerLine)

	// 计算总高度
	totalHeight := len(lines) * style.LineHeight

	return totalHeight
}

// splitTextIntoLines 将文本分割成行
func (p *PaginationEngine) splitTextIntoLines(text string, charsPerLine int) []string {
	if charsPerLine <= 0 {
		return []string{text}
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

// calculateElementHeight 计算元素高度
func (p *PaginationEngine) calculateElementHeight(element Element) int {
	style, exists := p.config.Styles[element.Type]
	if !exists {
		// 默认样式
		style = StyleConfig{
			FontSize:     32,
			LineHeight:   48,
			MarginTop:    0,
			MarginBottom: 20,
		}
	}

	var content string
	switch v := element.Content.(type) {
	case string:
		content = v
	case []string:
		// 列表类型
		content = strings.Join(v, "\n")
	default:
		content = fmt.Sprintf("%v", v)
	}

	textHeight := p.calculateTextHeight(content, style)
	return textHeight + style.MarginTop + style.MarginBottom
}

// calculateElementCharCount 计算元素字符数
func (p *PaginationEngine) calculateElementCharCount(element Element) int {
	switch v := element.Content.(type) {
	case string:
		return len([]rune(v)) // 使用rune来正确计算中文字符
	case []string:
		// 列表类型，计算所有项目的总字符数
		totalChars := 0
		for _, item := range v {
			totalChars += len([]rune(item))
		}
		return totalChars
	default:
		// 其他类型，转换为字符串计算
		text := fmt.Sprintf("%v", v)
		return len([]rune(text))
	}
}

// Paginate 执行分页
func (p *PaginationEngine) Paginate(elements []Element) (*PaginatedContent, error) {
	var cards []Card
	var currentCardElements []Element
	currentHeight := 0
	availableHeight := p.config.Card.Height - p.config.Card.Padding.Top - p.config.Card.Padding.Bottom

	for _, element := range elements {
		elementHeight := p.calculateElementHeight(element)

		// 检查是否需要新卡片
		if currentHeight+elementHeight > availableHeight && len(currentCardElements) > 0 {
			// 保存当前卡片
			cards = append(cards, Card{Elements: currentCardElements})

			// 重置当前卡片
			currentCardElements = []Element{element}
			currentHeight = elementHeight
		} else {
			// 添加到当前卡片
			currentCardElements = append(currentCardElements, element)
			currentHeight += elementHeight
		}
	}

	// 添加最后一页（如果非空）
	if len(currentCardElements) > 0 {
		cards = append(cards, Card{Elements: currentCardElements})
	}

	return &PaginatedContent{Cards: cards}, nil
}

// GetDefaultConfig 获取默认配置
func GetDefaultConfig() *PaginationConfig {
	return &PaginationConfig{
		Card: CardConfig{
			Width:  1080, // 标准尺寸: 1080×1440（3:4比例）
			Height: 1440,
			Padding: struct {
				Top    int `json:"top"`
				Right  int `json:"right"`
				Bottom int `json:"bottom"`
				Left   int `json:"left"`
			}{
				Top:    60, // 标准内边距: 60rpx 50rpx
				Right:  50,
				Bottom: 60,
				Left:   50,
			},
		},
		Styles: map[ElementType]StyleConfig{
			ElementTypeTitle: {
				FontSize:     64, // 标题: 64rpx（最大）
				LineHeight:   96, // 1.5倍行高
				MarginTop:    0,
				MarginBottom: 30,        // 标题下方: 30rpx
				Color:        "#333333", // 主标题: #333333（深灰）
				Align:        "left",    // 左对齐
			},
			ElementTypeSubtitle: {
				FontSize:     48, // 副标题: 48rpx（中等）
				LineHeight:   72, // 1.5倍行高
				MarginTop:    0,
				MarginBottom: 25,        // 副标题下方: 25rpx
				Color:        "#666666", // 副标题: #666666（中灰）
				Align:        "left",    // 左对齐
			},
			ElementTypeBody: {
				FontSize:     36, // 正文: 36rpx（标准）
				LineHeight:   58, // 1.6倍行高（标准行高）
				MarginTop:    0,
				MarginBottom: 30,        // 正文下方: 30rpx
				Color:        "#333333", // 正文: #333333（深灰）
				Align:        "left",    // 左对齐
			},
			ElementTypeList: {
				FontSize:     36, // 列表: 36rpx（标准）
				LineHeight:   54, // 1.5倍行高（紧凑行高）
				MarginTop:    0,
				MarginBottom: 30,        // 正文下方: 30rpx
				Indent:       40,        // 缩进
				Color:        "#333333", // 正文: #333333（深灰）
				Align:        "left",    // 左对齐
			},
			ElementTypeQuote: {
				FontSize:     36, // 引用: 36rpx（强调）
				LineHeight:   54, // 1.5倍行高（紧凑行高）
				MarginTop:    0,
				MarginBottom: 30,        // 正文下方: 30rpx
				Color:        "#1E90FF", // 引用: #1E90FF（蓝色）
				Align:        "left",    // 左对齐
			},
			ElementTypeTag: {
				FontSize:     28, // 标签: 28rpx（最小）
				LineHeight:   42, // 1.5倍行高
				MarginTop:    0,
				MarginBottom: 20,
				Color:        "#1E90FF", // 标签: #1E90FF（蓝色）
				Align:        "left",    // 左对齐
			},
		},
	}
}

// PaginateFromJSON 从JSON字符串分页
func PaginateFromJSON(jsonStr string) (*PaginatedContent, error) {
	var elements []Element
	if err := json.Unmarshal([]byte(jsonStr), &elements); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	engine := NewPaginationEngine(GetDefaultConfig())
	return engine.Paginate(elements)
}
