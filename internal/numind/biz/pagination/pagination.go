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
	FontSize     int `json:"fontSize"`
	LineHeight   int `json:"lineHeight"`
	MarginTop    int `json:"marginTop"`
	MarginBottom int `json:"marginBottom"`
	Indent       int `json:"indent,omitempty"`
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

	// 估算每行字符数（基于中文字符宽度）
	charsPerLine := availableWidth / (style.FontSize / 2) // 粗略估算

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

// Paginate 执行分页
func (p *PaginationEngine) Paginate(elements []Element) (*PaginatedContent, error) {
	var cards []Card
	var currentCardElements []Element
	currentHeight := p.config.Card.Padding.Top
	availableHeight := p.config.Card.Height - p.config.Card.Padding.Top - p.config.Card.Padding.Bottom

	for _, element := range elements {
		elementHeight := p.calculateElementHeight(element)

		// 检查是否需要新卡片
		if currentHeight+elementHeight > availableHeight && len(currentCardElements) > 0 {
			// 保存当前卡片
			cards = append(cards, Card{Elements: currentCardElements})

			// 重置当前卡片
			currentCardElements = []Element{element}
			currentHeight = p.config.Card.Padding.Top + elementHeight
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
			Width:  750,
			Height: 1334,
			Padding: struct {
				Top    int `json:"top"`
				Right  int `json:"right"`
				Bottom int `json:"bottom"`
				Left   int `json:"left"`
			}{
				Top:    80,
				Right:  60,
				Bottom: 80,
				Left:   60,
			},
		},
		Styles: map[ElementType]StyleConfig{
			ElementTypeTitle: {
				FontSize:     48,
				LineHeight:   72,
				MarginTop:    0,
				MarginBottom: 40,
			},
			ElementTypeSubtitle: {
				FontSize:     36,
				LineHeight:   54,
				MarginTop:    0,
				MarginBottom: 30,
			},
			ElementTypeBody: {
				FontSize:     32,
				LineHeight:   48,
				MarginTop:    0,
				MarginBottom: 20,
			},
			ElementTypeList: {
				FontSize:     32,
				LineHeight:   48,
				MarginTop:    0,
				MarginBottom: 20,
				Indent:       40,
			},
			ElementTypeQuote: {
				FontSize:     32,
				LineHeight:   48,
				MarginTop:    0,
				MarginBottom: 20,
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
