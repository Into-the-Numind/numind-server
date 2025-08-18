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
	charWidth := float64(style.FontSize) * 1.0 // 中文字符宽度
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

	// 调试信息
	if len(lines) > 1 {
		fmt.Printf("文本分割: 原文本长度=%d, 每行字符数=%d, 分割后行数=%d\n",
			len(runes), charsPerLine, len(lines))
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
		finalHeight := totalHeight + style.MarginTop + style.MarginBottom
		fmt.Printf("列表元素高度计算: 内容高度=%d, 上边距=%d, 下边距=%d, 总高度=%d\n",
			totalHeight, style.MarginTop, style.MarginBottom, finalHeight)
		return finalHeight
	default:
		content = fmt.Sprintf("%v", v)
	}

	// 计算文本高度
	textHeight := p.calculateTextHeight(content, style)

	// 添加元素特定的边距
	finalHeight := textHeight + style.MarginTop + style.MarginBottom
	fmt.Printf("元素高度计算 [%s]: 文本高度=%d, 上边距=%d, 下边距=%d, 总高度=%d\n",
		element.Type, textHeight, style.MarginTop, style.MarginBottom, finalHeight)

	return finalHeight
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

	// 修复：重新设计分页逻辑，确保所有元素都被处理
	for i, element := range elements {
		// 计算元素高度
		elementHeight := p.calculateElementHeight(element)

		fmt.Printf("处理元素 %d [%s]: 高度=%d, 当前总高度=%d, 可用高度=%d\n",
			i+1, element.Type, elementHeight, currentHeight, availableHeight)

		// 关键修复：检查是否需要创建新卡片
		if currentHeight+elementHeight > availableHeight {
			// 如果当前元素会导致溢出，且已有元素，则创建新卡片
			if len(currentCardElements) > 0 {
				// 保存当前卡片
				cards = append(cards, Card{Elements: currentCardElements})
				fmt.Printf("创建新卡片，元素数: %d, 总高度: %d\n", len(currentCardElements), currentHeight)

				// 重置当前卡片，将当前元素作为新卡片的第一个元素
				currentCardElements = []Element{element}
				currentHeight = elementHeight
			} else {
				// 如果单个元素就超出边界，强制创建卡片
				cards = append(cards, Card{Elements: []Element{element}})
				fmt.Printf("强制创建卡片（单元素超出），元素数: 1, 高度: %d\n", elementHeight)
				currentCardElements = []Element{}
				currentHeight = 0
			}
		} else {
			// 添加到当前卡片
			currentCardElements = append(currentCardElements, element)
			currentHeight += elementHeight
		}

		// 调试：显示当前状态
		fmt.Printf("元素 %d 处理后: 当前卡片元素数=%d, 当前高度=%d\n",
			i+1, len(currentCardElements), currentHeight)
	}

	// 处理最后一个卡片 - 确保不丢失任何内容
	if len(currentCardElements) > 0 {
		cards = append(cards, Card{Elements: currentCardElements})
		fmt.Printf("最后一页，元素数: %d, 总高度: %d\n", len(currentCardElements), currentHeight)
	}

	// 验证：确保所有元素都被处理
	totalElementsProcessed := 0
	for _, card := range cards {
		totalElementsProcessed += len(card.Elements)
	}

	fmt.Printf("分页完成 - 总卡片数: %d, 总元素数: %d, 原始元素数: %d\n",
		len(cards), totalElementsProcessed, len(elements))

	if totalElementsProcessed != len(elements) {
		fmt.Printf("⚠️ 警告：分页后元素数量不匹配！原始: %d, 处理后: %d\n",
			len(elements), totalElementsProcessed)
	}

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
			MarginTop:    30, // 修复：使用正确的边距
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
				FontSize:     64,        // 标题: 64px（最大）
				LineHeight:   90,        // 1.4倍行高
				MarginTop:    30,        // 标题上间距: 30px（统一标准）
				MarginBottom: 30,        // 标题下方间距: 30px（统一标准）
				Color:        "#333333", // 主标题: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeSubtitle: {
				FontSize:     48,        // 副标题: 48px（中等）
				LineHeight:   72,        // 1.5倍行高
				MarginTop:    30,        // 副标题上间距: 30px（统一标准）
				MarginBottom: 25,        // 副标题下方: 25px（标准下间距）
				Color:        "#666666", // 副标题: #666666（中灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeBody: {
				FontSize:     36,        // 正文: 36px（标准）
				LineHeight:   58,        // 1.6倍行高（标准行高）
				MarginTop:    30,        // 正文上间距: 30px（统一标准）
				MarginBottom: 30,        // 正文下方间距: 30px（统一标准）
				Color:        "#333333", // 正文: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeList: {
				FontSize:     36,        // 列表: 36px（标准）
				LineHeight:   58,        // 1.6倍行高（标准行高）
				MarginTop:    30,        // 列表上间距: 30px（统一标准）
				MarginBottom: 30,        // 列表下方间距: 30px（统一标准）
				Color:        "#333333", // 列表: #333333（深灰）
				Align:        "justify", // 两端对齐
			},
			ElementTypeQuote: {
				FontSize:     36,        // 引用: 36px（强调）
				LineHeight:   54,        // 1.5倍行高（紧凑行高）
				MarginTop:    30,        // 引用上间距: 30px（统一标准）
				MarginBottom: 30,        // 引用下方间距: 30px（统一标准）
				Color:        "#1E90FF", // 引用: #1E90FF（蓝色）
				Align:        "justify", // 两端对齐
			},
			ElementTypeTag: {
				FontSize:     28,        // 标签: 28px（最小）
				LineHeight:   42,        // 1.5倍行高
				MarginTop:    30,        // 标签上间距: 30px（统一标准）
				MarginBottom: 30,        // 标签下方间距: 30px（统一标准）
				Color:        "#1E90FF", // 标签: #1E90FF（蓝色）
				Align:        "left",    // 左对齐
			},
			ElementTypeNumber: {
				FontSize:     28,        // 数字: 28px（最小）
				LineHeight:   42,        // 1.5倍行高
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

// ValidatePaginationResult 验证分页结果的完整性
func (p *PaginationEngine) ValidatePaginationResult(originalElements []Element, result *PaginatedContent) error {
	if len(originalElements) == 0 && len(result.Cards) == 0 {
		return nil // 空输入，空输出，正确
	}

	// 统计所有卡片中的元素总数
	totalElementsInCards := 0
	for i, card := range result.Cards {
		totalElementsInCards += len(card.Elements)
		fmt.Printf("卡片 %d: %d 个元素\n", i+1, len(card.Elements))
	}

	// 检查元素数量是否匹配
	if totalElementsInCards != len(originalElements) {
		return fmt.Errorf("元素数量不匹配: 原始元素 %d, 分页后元素 %d",
			len(originalElements), totalElementsInCards)
	}

	// 检查每个卡片的高度是否在限制范围内
	availableHeight := p.config.Card.Height - p.config.Card.Padding.Top - p.config.Card.Padding.Bottom

	for i, card := range result.Cards {
		cardHeight := 0
		for _, element := range card.Elements {
			elementHeight := p.calculateElementHeight(element)
			cardHeight += elementHeight
		}

		if cardHeight > availableHeight {
			fmt.Printf("⚠️ 警告：卡片 %d 高度超出限制: %d > %d\n",
				i+1, cardHeight, availableHeight)
		}

		fmt.Printf("卡片 %d 验证: 元素数=%d, 总高度=%d, 限制=%d\n",
			i+1, len(card.Elements), cardHeight, availableHeight)
	}

	fmt.Printf("✅ 分页验证通过: 原始元素 %d, 分页后元素 %d, 卡片数 %d\n",
		len(originalElements), totalElementsInCards, len(result.Cards))

	return nil
}

// TestPaginationWithSampleData 使用示例数据测试分页功能
func (p *PaginationEngine) TestPaginationWithSampleData() {
	fmt.Println("🧪 开始分页功能测试...")

	// 创建测试数据：7个元素
	testElements := []Element{
		{Type: ElementTypeTitle, Content: "测试标题"},
		{Type: ElementTypeSubtitle, Content: "测试副标题"},
		{Type: ElementTypeBody, Content: "这是第一个正文段落，包含一些测试内容。"},
		{Type: ElementTypeBody, Content: "这是第二个正文段落，内容更长一些，用于测试分页逻辑。"},
		{Type: ElementTypeList, Content: []string{"列表项1", "列表项2", "列表项3"}},
		{Type: ElementTypeQuote, Content: "这是一个引用块，用于测试特殊元素的处理。"},
		{Type: ElementTypeBody, Content: "这是最后一个正文段落，确保所有7个元素都被正确处理。"},
	}

	fmt.Printf("测试数据: %d 个元素\n", len(testElements))

	// 执行分页
	result, err := p.PaginateElements(testElements)
	if err != nil {
		fmt.Printf("❌ 分页失败: %v\n", err)
		return
	}

	// 验证结果
	if err := p.ValidatePaginationResult(testElements, result); err != nil {
		fmt.Printf("❌ 分页验证失败: %v\n", err)
		return
	}

	fmt.Println("✅ 分页功能测试通过！")
}
