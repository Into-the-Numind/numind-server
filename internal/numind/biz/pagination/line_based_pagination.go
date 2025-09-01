package pagination

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// LineBasedPaginationEngine 按行分页引擎
type LineBasedPaginationEngine struct {
	config *PaginationConfig
}

// NewLineBasedPaginationEngine 创建新的按行分页引擎
func NewLineBasedPaginationEngine(config *PaginationConfig) *LineBasedPaginationEngine {
	return &LineBasedPaginationEngine{
		config: config,
	}
}

// LineInfo 行信息
type LineInfo struct {
	Text      string
	Width     float64
	Height    int
	CharCount int
}

// ElementLineBreakdown 元素行分解
type ElementLineBreakdown struct {
	Element     Element
	Lines       []LineInfo
	TotalHeight int
	CanSplit    bool
}

// PaginateByLines 按行进行分页
func (l *LineBasedPaginationEngine) PaginateByLines(elements []Element) (*PaginatedContent, error) {
	if len(elements) == 0 {
		return &PaginatedContent{Cards: []Card{}}, nil
	}

	fmt.Printf("🚀 开始按行分页，元素总数：%d\n", len(elements))

	// 计算可用高度
	availableHeight := l.config.Card.Height - l.config.Card.Padding.Top - l.config.Card.Padding.Bottom
	fmt.Printf("📏 可用高度：%dpx (卡片高度: %d - 上边距: %d - 下边距: %d)\n",
		availableHeight, l.config.Card.Height, l.config.Card.Padding.Top, l.config.Card.Padding.Bottom)

	// 分解所有元素为行
	lineBreakdowns := l.breakdownElementsToLines(elements)

	// 按行进行分页
	return l.paginateLines(lineBreakdowns, availableHeight)
}

// breakdownElementsToLines 将元素分解为行
func (l *LineBasedPaginationEngine) breakdownElementsToLines(elements []Element) []ElementLineBreakdown {
	var breakdowns []ElementLineBreakdown

	for i, element := range elements {
		fmt.Printf("🔍 分解元素 %d [%s]\n", i+1, element.Type)

		breakdown := ElementLineBreakdown{
			Element: element,
			Lines:   []LineInfo{},
		}

		// 获取样式配置
		style, exists := l.config.Styles[element.Type]
		if !exists {
			style = StyleConfig{
				FontSize:     36,
				LineHeight:   58,
				MarginTop:    30,
				MarginBottom: 30,
			}
		}

		// 计算行高
		lineHeight := int(float64(style.FontSize) * 1.6)

		// 处理不同类型的内容
		switch v := element.Content.(type) {
		case string:
			// 文本内容，按行分解
			lines := l.splitTextIntoLines(v, style)
			for _, line := range lines {
				breakdown.Lines = append(breakdown.Lines, LineInfo{
					Text:      line,
					Width:     l.calculateLineWidth(line, style),
					Height:    lineHeight,
					CharCount: utf8.RuneCountInString(line),
				})
			}
			breakdown.TotalHeight = len(lines)*lineHeight + style.MarginTop + style.MarginBottom
			breakdown.CanSplit = true

		case []string:
			// 列表内容，每个项目单独处理
			for j, item := range v {
				itemLines := l.splitTextIntoLines(item, style)
				for _, line := range itemLines {
					breakdown.Lines = append(breakdown.Lines, LineInfo{
						Text:      line,
						Width:     l.calculateLineWidth(line, style),
						Height:    lineHeight,
						CharCount: utf8.RuneCountInString(line),
					})
				}
				// 列表项之间添加间距（除了最后一项）
				if j < len(v)-1 {
					breakdown.Lines = append(breakdown.Lines, LineInfo{
						Text:      "", // 空行作为间距
						Width:     0,
						Height:    8, // 列表项间距
						CharCount: 0,
					})
				}
			}
			breakdown.TotalHeight = len(breakdown.Lines)*lineHeight + style.MarginTop + style.MarginBottom
			breakdown.CanSplit = true

		default:
			// 其他类型，转换为字符串处理
			text := fmt.Sprintf("%v", v)
			lines := l.splitTextIntoLines(text, style)
			for _, line := range lines {
				breakdown.Lines = append(breakdown.Lines, LineInfo{
					Text:      line,
					Width:     l.calculateLineWidth(line, style),
					Height:    lineHeight,
					CharCount: utf8.RuneCountInString(line),
				})
			}
			breakdown.TotalHeight = len(lines)*lineHeight + style.MarginTop + style.MarginBottom
			breakdown.CanSplit = true
		}

		fmt.Printf("📊 元素 %d 分解完成：%d 行，总高度：%dpx\n", i+1, len(breakdown.Lines), breakdown.TotalHeight)
		breakdowns = append(breakdowns, breakdown)
	}

	return breakdowns
}

// splitTextIntoLines 将文本分割为行
func (l *LineBasedPaginationEngine) splitTextIntoLines(text string, style StyleConfig) []string {
	if strings.TrimSpace(text) == "" {
		return []string{""}
	}

	// 计算可用宽度
	availableWidth := l.config.Card.Width - l.config.Card.Padding.Left - l.config.Card.Padding.Right
	if style.Indent > 0 {
		availableWidth -= style.Indent
	}

	// 计算字符宽度
	charWidth := float64(style.FontSize) * 1.05
	charsPerLine := int(float64(availableWidth) / charWidth)

	if charsPerLine <= 0 {
		charsPerLine = 1
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

// calculateLineWidth 计算行的宽度
func (l *LineBasedPaginationEngine) calculateLineWidth(line string, style StyleConfig) float64 {
	if line == "" {
		return 0
	}

	charWidth := float64(style.FontSize) * 1.05
	totalWidth := 0.0

	for _, char := range line {
		if char < 128 {
			totalWidth += charWidth * 0.6 // 英文字符
		} else {
			totalWidth += charWidth // 中文字符
		}
	}

	return totalWidth
}

// paginateLines 按行进行分页
func (l *LineBasedPaginationEngine) paginateLines(breakdowns []ElementLineBreakdown, availableHeight int) (*PaginatedContent, error) {
	var cards []Card
	var currentHeight int
	var currentLines []LineInfo

	fmt.Printf("📄 开始按行分页，可用高度：%dpx\n", availableHeight)

	for i, breakdown := range breakdowns {
		fmt.Printf("🔍 处理元素 %d [%s]，包含 %d 行\n", i+1, breakdown.Element.Type, len(breakdown.Lines))

		// 如果元素不能分割且当前卡片已有内容，先完成当前卡片
		if !breakdown.CanSplit && len(currentLines) > 0 && breakdown.TotalHeight > availableHeight-currentHeight {
			// 完成当前卡片
			card := l.createCardFromLines(currentLines, breakdown.Element.Type)
			cards = append(cards, card)
			fmt.Printf("✅ 完成卡片 %d，高度：%dpx，行数：%d\n", len(cards), currentHeight, len(currentLines))
			
			// 重置当前卡片
			currentLines = []LineInfo{}
			currentHeight = 0
		}

		// 逐行处理
		for j, line := range breakdown.Lines {
			lineHeight := line.Height
			if lineHeight == 0 {
				lineHeight = int(float64(l.config.Styles[breakdown.Element.Type].FontSize) * 1.6)
			}

			// 检查是否需要新卡片
			if currentHeight+lineHeight > availableHeight && len(currentLines) > 0 {
				// 完成当前卡片
				card := l.createCardFromLines(currentLines, breakdown.Element.Type)
				cards = append(cards, card)
				fmt.Printf("✅ 完成卡片 %d，高度：%dpx，行数：%d\n", len(cards), currentHeight, len(currentLines))

				// 重置当前卡片
				currentLines = []LineInfo{}
				currentHeight = 0
			}

			// 添加当前行
			currentLines = append(currentLines, line)
			currentHeight += lineHeight

			fmt.Printf("📝 添加行 %d/%d：高度=%dpx，当前总高度=%dpx，内容：%s\n",
				j+1, len(breakdown.Lines), lineHeight, currentHeight, 
				truncateString(line.Text, 30))
		}

		// 如果元素不能分割，确保整个元素都在同一卡片中
		if !breakdown.CanSplit && len(currentLines) > 0 {
			// 检查是否需要新卡片来容纳整个元素
			if currentHeight > availableHeight {
				// 移除最后添加的行，完成当前卡片
				currentLines = currentLines[:len(currentLines)-len(breakdown.Lines)]
				if len(currentLines) > 0 {
					card := l.createCardFromLines(currentLines, breakdown.Element.Type)
					cards = append(cards, card)
					fmt.Printf("✅ 完成卡片 %d，高度：%dpx，行数：%d\n", len(cards), currentHeight, len(currentLines))
				}

				// 开始新卡片，添加整个元素
				currentLines = breakdown.Lines
				currentHeight = breakdown.TotalHeight
				fmt.Printf("📄 开始新卡片，添加整个元素 %d，高度：%dpx\n", i+1, currentHeight)
			}
		}
	}

	// 处理最后一张卡片
	if len(currentLines) > 0 {
		card := l.createCardFromLines(currentLines, breakdowns[len(breakdowns)-1].Element.Type)
		cards = append(cards, card)
		fmt.Printf("✅ 完成最后卡片 %d，高度：%dpx，行数：%d\n", len(cards), currentHeight, len(currentLines))
	}

	fmt.Printf("🎉 按行分页完成，共生成 %d 张卡片\n", len(cards))

	// 统计每张卡片的信息
	for i, card := range cards {
		totalHeight := 0
		for _, element := range card.Elements {
			totalHeight += l.calculateElementHeight(element)
		}
		utilization := float64(totalHeight) / float64(availableHeight) * 100
		fmt.Printf("📊 卡片 %d：元素数=%d，高度=%dpx，利用率=%.1f%%\n",
			i+1, len(card.Elements), totalHeight, utilization)
	}

	return &PaginatedContent{Cards: cards}, nil
}

// createCardFromLines 从行信息创建卡片
func (l *LineBasedPaginationEngine) createCardFromLines(lines []LineInfo, elementType ElementType) Card {
	if len(lines) == 0 {
		return Card{Elements: []Element{}}
	}

	// 将行重新组合为元素
	var elements []Element
	var currentElementLines []string
	currentElementType := elementType

	for _, line := range lines {
		if line.Text == "" && line.Height == 8 {
			// 这是列表项间距，跳过
			continue
		}
		currentElementLines = append(currentElementLines, line.Text)
	}

	if len(currentElementLines) > 0 {
		// 过滤空行
		var nonEmptyLines []string
		for _, line := range currentElementLines {
			if strings.TrimSpace(line) != "" {
				nonEmptyLines = append(nonEmptyLines, line)
			}
		}

		if len(nonEmptyLines) > 0 {
			content := strings.Join(nonEmptyLines, "\n")
			elements = append(elements, Element{
				Type:    currentElementType,
				Content: content,
			})
		}
	}

	return Card{Elements: elements}
}

// calculateElementHeight 计算元素高度（复用现有逻辑）
func (l *LineBasedPaginationEngine) calculateElementHeight(element Element) int {
	style, exists := l.config.Styles[element.Type]
	if !exists {
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
			itemHeight := l.calculateTextHeight(item, style)
			totalHeight += itemHeight
			// 列表项之间添加间距（除了最后一项）
			if i < len(v)-1 {
				totalHeight += 8
			}
		}
		return totalHeight + style.MarginTop + style.MarginBottom
	default:
		content = fmt.Sprintf("%v", v)
	}

	textHeight := l.calculateTextHeight(content, style)
	return textHeight + style.MarginTop + style.MarginBottom
}

// calculateTextHeight 计算文本高度（复用现有逻辑）
func (l *LineBasedPaginationEngine) calculateTextHeight(text string, style StyleConfig) int {
	if text == "" {
		return 0
	}

	availableWidth := l.config.Card.Width - l.config.Card.Padding.Left - l.config.Card.Padding.Right
	if style.Indent > 0 {
		availableWidth -= style.Indent
	}

	lines := l.splitTextIntoLines(text, style)
	lineHeight := int(float64(style.FontSize) * 1.6)
	totalHeight := len(lines) * lineHeight

	return totalHeight
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
