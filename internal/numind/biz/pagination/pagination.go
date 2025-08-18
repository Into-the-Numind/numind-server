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

	// 更精确的字符宽度计算
	// 中文字符宽度约为字体大小的1.0倍，英文字符约为0.6倍
	// 为了安全起见，我们使用更保守的估计
	charWidth := float64(style.FontSize) * 1.05 // 稍微保守的估计
	charsPerLine := int(float64(availableWidth) / charWidth)

	// 计算行数
	lines := p.splitTextIntoLines(text, charsPerLine)

	// 计算总高度：每行的实际高度 + 行间距
	// 行高应该基于字体大小，而不是固定的像素值
	lineHeight := int(float64(style.FontSize) * 1.6) // 1.6倍行高
	totalHeight := len(lines) * lineHeight

	return totalHeight
}

// splitTextIntoLines 将文本分割成行
func (p *PaginationEngine) splitTextIntoLines(text string, charsPerLine int) []string {
	if charsPerLine <= 0 {
		return []string{text}
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
		// 列表类型，每个项目单独计算高度
		totalHeight := 0
		for i, item := range v {
			itemHeight := p.calculateTextHeight(item, style)
			totalHeight += itemHeight
			// 列表项之间添加间距（除了最后一项）
			if i < len(v)-1 {
				totalHeight += 8 // 列表项间距
			}
		}
		return totalHeight + style.MarginTop + style.MarginBottom
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

// PaginateElements 分页元素
func (p *PaginationEngine) PaginateElements(elements []Element) (*PaginatedContent, error) {
	if len(elements) == 0 {
		return &PaginatedContent{Cards: []Card{}}, nil
	}

	// 计算可用高度
	availableHeight := p.config.Card.Height - p.config.Card.Padding.Top - p.config.Card.Padding.Bottom

	fmt.Printf("分页开始 - 卡片高度: %d, 上边距: %d, 下边距: %d, 可用高度: %d\n",
		p.config.Card.Height, p.config.Card.Padding.Top, p.config.Card.Padding.Bottom, availableHeight)

	var cards []Card
	var currentCardElements []Element
	currentHeight := 0

	for i, element := range elements {
		// 计算元素高度
		elementHeight := p.calculateElementHeight(element)
		
		fmt.Printf("元素 %d [%s]: 高度=%d, 当前总高度=%d, 可用高度=%d\n", 
			i+1, element.Type, elementHeight, currentHeight, availableHeight)

		// 检查是否需要创建新卡片
		if currentHeight+elementHeight > availableHeight && len(currentCardElements) > 0 {
			// 创建新卡片
			cards = append(cards, Card{Elements: currentCardElements})
			fmt.Printf("创建新卡片，元素数: %d, 总高度: %d\n", len(currentCardElements), currentHeight)
			
			// 重置当前卡片
			currentCardElements = []Element{element}
			currentHeight = elementHeight
		} else {
			// 添加到当前卡片
			currentCardElements = append(currentCardElements, element)
			currentHeight += elementHeight
		}
	}

	// 处理最后一个卡片
	if len(currentCardElements) > 0 {
		// 检查最后一个卡片是否超出边界
		if currentHeight > availableHeight {
			// 如果超出边界，尝试分割最后一个元素
			lastElement := currentCardElements[len(currentCardElements)-1]
			remainingElements := currentCardElements[:len(currentCardElements)-1]
			
			if len(remainingElements) > 0 {
				// 先添加前面的元素
				cards = append(cards, Card{Elements: remainingElements})
				fmt.Printf("创建卡片（分割前），元素数: %d\n", len(remainingElements))
			}
			
			// 尝试分割最后一个元素
			splitElements := p.splitLongElement(lastElement, availableHeight)
			if len(splitElements) > 0 {
				// 分割成功，创建新卡片
				cards = append(cards, Card{Elements: splitElements})
				fmt.Printf("创建卡片（分割后），元素数: %d\n", len(splitElements))
			} else {
				// 分割失败，强制创建新卡片
				fmt.Printf("▲ 无法分割元素%d,强制添加到新卡片\n", len(elements))
				cards = append(cards, Card{Elements: []Element{lastElement}})
			}
		} else {
			// 正常添加最后一个卡片
			cards = append(cards, Card{Elements: currentCardElements})
		}
		
		fmt.Printf("最后一页，元素数: %d, 总高度: %d, 边距: 上%d下%d\n", 
			len(currentCardElements), currentHeight, p.config.Card.Padding.Top, p.config.Card.Padding.Bottom)
	}

	fmt.Printf("分页完成 - 总卡片数: %d\n", len(cards))
	return &PaginatedContent{Cards: cards}, nil
}

// splitLongElement 分割长文本元素
func (p *PaginationEngine) splitLongElement(element Element, maxHeight int) []Element {
	// 只处理文本类型的元素
	if element.Type != ElementTypeBody && element.Type != ElementTypeTitle && 
	   element.Type != ElementTypeSubtitle && element.Type != ElementTypeQuote {
		return nil // 无法分割
	}

	content, ok := element.Content.(string)
	if !ok {
		return nil
	}

	// 获取样式配置
	style, exists := p.config.Styles[element.Type]
	if !exists {
		style = StyleConfig{
			FontSize:     36,
			LineHeight:   58,
			MarginTop:    30,        // 修复：使用正确的边距
			MarginBottom: 30,
		}
	}

	// 计算可用宽度
	availableWidth := p.config.Card.Width - p.config.Card.Padding.Left - p.config.Card.Padding.Right
	charWidth := float64(style.FontSize) * 1.05
	charsPerLine := int(float64(availableWidth) / charWidth)

	// 计算每行的高度
	lineHeight := int(float64(style.FontSize) * 1.6)
	
	// 计算最大行数，考虑边距
	maxLines := (maxHeight - style.MarginTop - style.MarginBottom) / lineHeight
	if maxLines <= 0 {
		fmt.Printf("高度太小，无法分割: maxHeight=%d, margins=%d, lineHeight=%d, maxLines=%d\n", 
			maxHeight, style.MarginTop+style.MarginBottom, lineHeight, maxLines)
		return nil
	}

	fmt.Printf("分割参数: 内容长度=%d, 每行字符数=%d, 每行高度=%d, 最大行数=%d\n", 
		len(content), charsPerLine, lineHeight, maxLines)

	// 分割文本
	lines := p.splitTextIntoLines(content, charsPerLine)
	fmt.Printf("分割后行数: %d\n", len(lines))
	
	if len(lines) <= maxLines {
		fmt.Printf("不需要分割，行数 %d <= 最大行数 %d\n", len(lines), maxLines)
		return nil
	}

	// 创建分割后的元素
	var splitElements []Element
	currentLines := 0
	currentContent := ""
	partIndex := 1

	for i, line := range lines {
		if currentLines >= maxLines {
			// 当前部分已满，创建元素
			if currentContent != "" {
				splitElements = append(splitElements, Element{
					Type:    element.Type,
					Content: strings.TrimSpace(currentContent),
				})
				fmt.Printf("创建分割元素 %d: 行数=%d, 内容长度=%d\n", 
					partIndex, currentLines, len(currentContent))
				partIndex++
			}
			currentContent = line
			currentLines = 1
		} else {
			if currentContent != "" {
				currentContent += "\n" + line
			} else {
				currentContent = line
			}
			currentLines++
		}
		
		// 最后一行特殊处理
		if i == len(lines)-1 && currentContent != "" {
			splitElements = append(splitElements, Element{
				Type:    element.Type,
				Content: strings.TrimSpace(currentContent),
			})
			fmt.Printf("创建最后分割元素 %d: 行数=%d, 内容长度=%d\n", 
				partIndex, currentLines, len(currentContent))
		}
	}

	fmt.Printf("分割完成，共创建 %d 个元素\n", len(splitElements))
	return splitElements
}

// calculateTotalHeight 计算多个元素的总高度
func (p *PaginationEngine) calculateTotalHeight(elements []Element) int {
	totalHeight := 0
	for _, element := range elements {
		totalHeight += p.calculateElementHeight(element)
	}
	return totalHeight
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
				Top:    60, // 标准内边距: 60px 50px
				Right:  50,
				Bottom: 60,
				Left:   50,
			},
		},
		Styles: map[ElementType]StyleConfig{
			ElementTypeTitle: {
				FontSize:     64, // 标题: 64px（最大）
				LineHeight:   90, // 1.4倍行高
				MarginTop:    30,        // 标题上间距: 30px（统一标准）
				MarginBottom: 30,        // 标题下方间距: 30px（统一标准）
				Color:        "#333333", // 主标题: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeSubtitle: {
				FontSize:     48, // 副标题: 48px（中等）
				LineHeight:   72, // 1.5倍行高
				MarginTop:    30,        // 副标题上间距: 30px（统一标准）
				MarginBottom: 25,        // 副标题下方: 25px（标准下间距）
				Color:        "#666666", // 副标题: #666666（中灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeBody: {
				FontSize:     36, // 正文: 36px（标准）
				LineHeight:   58, // 1.6倍行高（标准行高）
				MarginTop:    30,        // 正文上间距: 30px（统一标准）
				MarginBottom: 30,        // 正文下方间距: 30px（统一标准）
				Color:        "#333333", // 正文: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeList: {
				FontSize:     36, // 列表: 36px（标准）
				LineHeight:   58, // 1.6倍行高（标准行高）
				MarginTop:    30,        // 列表上间距: 30px（统一标准）
				MarginBottom: 30,        // 列表下方间距: 30px（统一标准）
				Color:        "#333333", // 列表: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeQuote: {
				FontSize:     36, // 引用: 36px（强调）
				LineHeight:   54, // 1.5倍行高（紧凑行高）
				MarginTop:    30,        // 引用上间距: 30px（统一标准）
				MarginBottom: 30,        // 引用下方间距: 30px（统一标准）
				Color:        "#1E90FF", // 引用: #1E90FF（蓝色）
				Align:        "justify", // 两端对齐
			},
			ElementTypeTag: {
				FontSize:     28, // 标签: 28px（最小）
				LineHeight:   42, // 1.5倍行高
				MarginTop:    30,        // 标签上间距: 30px（统一标准）
				MarginBottom: 30,        // 标签下方间距: 30px（统一标准）
				Color:        "#1E90FF", // 标签: #1E90FF（蓝色）
				Align:        "left",    // 左对齐
			},
			ElementTypeNumber: {
				FontSize:     28, // 数字: 28px（最小）
				LineHeight:   42, // 1.5倍行高
				MarginTop:    30,        // 数字上间距: 30px（统一标准）
				MarginBottom: 30,        // 数字下方间距: 30px（统一标准）
				Color:        "#1E90FF", // 数字: #1E90FF（蓝色）
				Align:        "center",  // 居中对齐
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
	return engine.PaginateElements(elements)
}
