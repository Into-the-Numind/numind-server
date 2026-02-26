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
	FontSize        int    `json:"fontSize"`
	LineHeight      int    `json:"lineHeight"`
	MarginTop       int    `json:"marginTop"`
	MarginBottom    int    `json:"marginBottom"`
	Indent          int    `json:"indent,omitempty"`
	Color           string `json:"color,omitempty"`
	Align           string `json:"align,omitempty"`
	FirstLineIndent int    `json:"firstLineIndent,omitempty"`
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

// PageNumberConfig 定义页码配置
type PageNumberConfig struct {
	Enabled    bool   `json:"enabled"`     // 是否启用页码显示
	FontSize   int    `json:"font_size"`   // 页码字号
	Color      string `json:"color"`       // 页码颜色
	FontWeight string `json:"font_weight"` // 页码粗细：normal, bold
	Position   struct {
		Bottom int `json:"bottom"` // 距离底部边距
		Right  int `json:"right"`  // 距离右侧边距
	} `json:"position"`
	Format string `json:"format"` // 页码格式：{current}当前页，{total}总页数
}

// PaginationConfig 定义分页配置
type PaginationConfig struct {
	Card       CardConfig                  `json:"card"`
	Styles     map[ElementType]StyleConfig `json:"styles"`
	PageNumber PageNumberConfig            `json:"page_number"` // 页码配置
	// 新增可配置参数
	CharWidthFactor          float64 `json:"char_width_factor"`          // 字符宽度系数
	OverflowTolerance        float64 `json:"overflow_tolerance"`         // 溢出容错比例
	HighUtilizationThreshold float64 `json:"high_utilization_threshold"` // 高利用率阈值
	MinCharsPerLine          int     `json:"min_chars_per_line"`         // 最小每行字符数
	ListItemSpacing          int     `json:"list_item_spacing"`          // 列表项间距
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
	charWidth := float64(style.FontSize) * p.config.CharWidthFactor // 使用配置的字符宽度系数
	charsPerLine := int(float64(availableWidth) / charWidth)

	// 计算行数
	lines := p.splitTextIntoLines(text, charsPerLine)

	// 计算总高度：每行的实际高度 + 行间距
	// 行高应该基于字体大小，而不是固定的像素值
	lineHeight := int(float64(style.FontSize) * 1.6) // 1.6倍行高
	totalHeight := len(lines) * lineHeight

	// 添加调试信息
	fmt.Printf("🔍 文本高度计算: 文本长度=%d, 每行字符数=%d, 行数=%d, 行高=%d, 总高度=%d\n",
		len(text), charsPerLine, len(lines), lineHeight, totalHeight)

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
		fmt.Printf("🔍 调试：计算列表元素高度，项目数: %d\n", len(v))
		for i, item := range v {
			itemHeight := p.calculateTextHeight(item, style)
			totalHeight += itemHeight
			fmt.Printf("🔍 调试：列表项 %d 高度: %d，累计: %d，内容: %s\n", i, itemHeight, totalHeight, item[:min(len(item), 50)]+"...")
			// 列表项之间添加间距（除了最后一项）
			if i < len(v)-1 {
				totalHeight += p.config.ListItemSpacing // 使用配置的列表项间距
				fmt.Printf("🔍 调试：添加列表项间距，新累计: %d\n", totalHeight)
			}
		}
		finalHeight := totalHeight + style.MarginTop + style.MarginBottom
		fmt.Printf("🔍 调试：列表元素最终高度: %d (内容: %d + 边距: %d)\n", finalHeight, totalHeight, style.MarginTop+style.MarginBottom)
		return finalHeight
	default:
		content = fmt.Sprintf("%v", v)
	}

	textHeight := p.calculateTextHeight(content, style)
	return textHeight + style.MarginTop + style.MarginBottom
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
		fmt.Printf("🔍 调试：开始处理元素 %d，类型: %s\n", i+1, element.Type)

		// 如果是列表类型，打印列表内容
		if element.Type == ElementTypeList {
			if items, ok := element.Content.([]string); ok {
				fmt.Printf("🔍 调试：列表元素包含 %d 项\n", len(items))
				for j, item := range items {
					fmt.Printf("🔍 调试：列表项 %d: %s\n", j, item[:min(len(item), 50)]+"...")
				}
			}
		}

		// 计算元素高度
		elementHeight := p.calculateElementHeight(element)

		fmt.Printf("🔍 调试：元素 %d [%s]: 高度=%d, 当前总高度=%d, 可用高度=%d\n",
			i+1, element.Type, elementHeight, currentHeight, availableHeight)

		// 检查是否需要创建新卡片或分割元素
		// 使用更精确的分页逻辑，尽量充分利用可用空间

		// 计算当前利用率
		currentUtilization := float64(currentHeight) / float64(availableHeight) * 100

		// 预测添加当前元素后的利用率
		predictedUtilization := float64(currentHeight+elementHeight) / float64(availableHeight) * 100

		fmt.Printf("🔍 调试：元素 %d 利用率分析 - 当前: %.1f%%, 预测: %.1f%%\n",
			i+1, currentUtilization, predictedUtilization)

		// 检查是否需要创建新卡片
		// 允许小幅度的超出，以便更好地利用空间
		overflowTolerance := int(float64(availableHeight) * p.config.OverflowTolerance) // 使用配置的容错比例

		if currentHeight+elementHeight > availableHeight+overflowTolerance {
			// 检查是否可以尝试更精确的分页
			if len(currentCardElements) > 0 && elementHeight <= availableHeight {
				// 当前元素可以单独放入新卡片，先保存当前卡片
				cards = append(cards, Card{Elements: currentCardElements})
				fmt.Printf("🔍 调试：创建新卡片 %d，元素数: %d, 总高度: %d (利用率: %.1f%%)\n",
					len(cards), len(currentCardElements), currentHeight,
					float64(currentHeight)/float64(availableHeight)*100)

				// 重置当前卡片，添加当前元素
				currentCardElements = []Element{element}
				currentHeight = elementHeight
				fmt.Printf("🔍 调试：重置当前卡片，添加元素 %d，新高度: %d\n", i+1, currentHeight)
			} else if currentUtilization >= p.config.HighUtilizationThreshold && elementHeight <= availableHeight {
				// 当前卡片利用率已经很高（>=85%），且当前元素可以单独放入新卡片
				cards = append(cards, Card{Elements: currentCardElements})
				fmt.Printf("🔍 调试：高利用率创建新卡片 %d，元素数: %d, 总高度: %d (利用率: %.1f%%)\n",
					len(cards), len(currentCardElements), currentHeight, currentUtilization)

				// 重置当前卡片，添加当前元素
				currentCardElements = []Element{element}
				currentHeight = elementHeight
				fmt.Printf("🔍 调试：重置当前卡片，添加元素 %d，新高度: %d\n", i+1, currentHeight)
			} else {
				// 检查当前元素是否需要分割
				if elementHeight > availableHeight {
					fmt.Printf("🔍 调试：元素 %d 高度 %d 超出可用高度 %d，尝试分割\n", i+1, elementHeight, availableHeight)
					splitElements := p.splitLongElement(element, availableHeight)
					if len(splitElements) > 0 {
						fmt.Printf("🔍 调试：元素分割成功，产生 %d 个子元素\n", len(splitElements))
						// 分割成功，将分割后的元素按顺序处理
						for j, splitElement := range splitElements {
							splitHeight := p.calculateElementHeight(splitElement)
							fmt.Printf("🔍 调试：处理分割元素 %d/%d，高度: %d\n", j+1, len(splitElements), splitHeight)

							if currentHeight+splitHeight > availableHeight && len(currentCardElements) > 0 {
								// 需要新卡片
								cards = append(cards, Card{Elements: currentCardElements})
								fmt.Printf("🔍 调试：分割元素创建新卡片 %d，利用率: %.1f%%\n",
									len(cards), float64(currentHeight)/float64(availableHeight)*100)
								currentCardElements = []Element{splitElement}
								currentHeight = splitHeight
							} else {
								// 添加到当前卡片
								currentCardElements = append(currentCardElements, splitElement)
								currentHeight += splitHeight
							}
						}
					} else {
						// 分割失败，强制添加
						fmt.Printf("🔍 调试：元素分割失败，强制添加到新卡片\n")
						currentCardElements = []Element{element}
						currentHeight = elementHeight
					}
				} else {
					// 元素不需要分割，直接添加到新卡片
					fmt.Printf("🔍 调试：添加元素 %d 到新卡片\n", i+1)
					currentCardElements = []Element{element}
					currentHeight = elementHeight
				}
			}
		} else {
			// 添加到当前卡片
			currentCardElements = append(currentCardElements, element)
			currentHeight += elementHeight
			fmt.Printf("🔍 调试：添加元素 %d 到当前卡片，当前卡片元素数: %d，新总高度: %d (利用率: %.1f%%)\n",
				i+1, len(currentCardElements), currentHeight,
				float64(currentHeight)/float64(availableHeight)*100)
		}
	}

	// 处理最后一个卡片
	fmt.Printf("🔍 调试：开始处理最后一个卡片，剩余元素数: %d，当前高度: %d\n", len(currentCardElements), currentHeight)

	if len(currentCardElements) > 0 {
		// 打印最后一个卡片中的所有元素
		fmt.Printf("🔍 调试：最后一个卡片包含的元素:\n")
		for j, elem := range currentCardElements {
			fmt.Printf("🔍 调试：  元素 %d: 类型=%s\n", j, elem.Type)
			if elem.Type == ElementTypeList {
				if items, ok := elem.Content.([]string); ok {
					fmt.Printf("🔍 调试：    列表包含 %d 项\n", len(items))
					for k, item := range items {
						fmt.Printf("🔍 调试：      项 %d: %s\n", k, item[:min(len(item), 30)]+"...")
					}
				}
			}
		}

		// 直接添加最后一个卡片（分页逻辑已在循环中处理）
		cards = append(cards, Card{Elements: currentCardElements})
		fmt.Printf("🔍 调试：最后一个卡片已添加，元素数: %d, 总高度: %d\n", len(currentCardElements), currentHeight)
	} else {
		fmt.Printf("🔍 调试：没有剩余元素需要处理\n")
	}

	fmt.Printf("分页完成 - 总卡片数: %d\n", len(cards))
	return &PaginatedContent{Cards: cards}, nil
}

// splitLongElement 分割长文本元素
func (p *PaginationEngine) splitLongElement(element Element, maxHeight int) []Element {
	// 处理列表类型
	if element.Type == ElementTypeList {
		return p.splitListElement(element, maxHeight)
	}

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
	charWidth := float64(style.FontSize) * p.config.CharWidthFactor
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

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
				Bottom: 10, // 减少底部边距，减少空白
				Left:   50,
			},
		},
		PageNumber: PageNumberConfig{
			Enabled:    true,      // 默认启用页码
			FontSize:   24,        // 默认字号
			Color:      "#666666", // 默认颜色
			FontWeight: "normal",  // 默认粗细
			Position: struct {
				Bottom int `json:"bottom"`
				Right  int `json:"right"`
			}{
				Bottom: 30, // 距离底部边距
				Right:  30, // 距离右侧边距
			},
			Format: "{current}/{total}", // 默认格式
		},
		// 新增可配置参数的默认值
		CharWidthFactor:          1.05, // 字符宽度系数
		OverflowTolerance:        0.05, // 溢出容错比例（5%）
		HighUtilizationThreshold: 85.0, // 高利用率阈值（85%）
		MinCharsPerLine:          20,   // 最小每行字符数
		ListItemSpacing:          8,    // 列表项间距
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

// splitListElement 分割列表元素
func (p *PaginationEngine) splitListElement(element Element, maxHeight int) []Element {
	fmt.Printf("🔧 开始分割列表元素，最大高度: %d\n", maxHeight)

	// 确保是列表类型
	if element.Type != ElementTypeList {
		fmt.Printf("❌ 不是列表类型，无法分割: %s\n", element.Type)
		return nil
	}

	// 获取列表内容
	var items []string
	switch v := element.Content.(type) {
	case []string:
		items = v
		fmt.Printf("📝 列表内容([]string)，项目数: %d\n", len(items))
	case []interface{}:
		fmt.Printf("📝 列表内容([]interface{})，项目数: %d\n", len(v))
		for i, item := range v {
			if str, ok := item.(string); ok {
				items = append(items, str)
				fmt.Printf("🔄 转换项目 %d: %s\n", i, str[:min(len(str), 30)]+"...")
			} else {
				fmt.Printf("⚠️ 项目 %d 类型不匹配: %T\n", i, item)
			}
		}
	default:
		fmt.Printf("❌ 列表内容类型不支持: %T\n", v)
		return nil
	}

	if len(items) == 0 {
		fmt.Printf("❌ 列表为空，无法分割\n")
		return nil
	}

	// 获取列表样式配置
	style, exists := p.config.Styles[ElementTypeList]
	if !exists {
		style = StyleConfig{
			FontSize:     36,
			LineHeight:   58,
			MarginTop:    30,
			MarginBottom: 30,
		}
	}

	fmt.Printf("🎨 使用样式: 字体=%dpx, 行高=%dpx, 边距=%d+%d\n",
		style.FontSize, style.LineHeight, style.MarginTop, style.MarginBottom)

	// 计算每个列表项的高度
	var itemHeights []int
	totalContentHeight := 0

	for i, item := range items {
		itemHeight := p.calculateTextHeight(item, style)
		itemHeights = append(itemHeights, itemHeight)
		totalContentHeight += itemHeight

		// 添加列表项间距（除了最后一项）
		if i < len(items)-1 {
			totalContentHeight += p.config.ListItemSpacing // 使用配置的列表项间距
		}

		fmt.Printf("📏 项目 %d 高度: %dpx，累计: %dpx\n", i, itemHeight, totalContentHeight)
	}

	// 计算总高度（包含边距）
	totalHeight := totalContentHeight + style.MarginTop + style.MarginBottom
	fmt.Printf("📐 列表总高度: %dpx (内容: %dpx + 边距: %dpx)\n",
		totalHeight, totalContentHeight, style.MarginTop+style.MarginBottom)

	// 如果总高度不超过限制，不需要分割
	if totalHeight <= maxHeight {
		fmt.Printf("✅ 列表高度未超限，无需分割\n")
		return nil
	}

	// 计算可用内容高度
	availableContentHeight := maxHeight - style.MarginTop - style.MarginBottom
	fmt.Printf("📊 可用内容高度: %dpx\n", availableContentHeight)

	// 开始分割：计算第一个卡片能容纳多少项
	var firstCardItems []string
	currentHeight := 0
	splitIndex := 0

	for i, item := range items {
		itemHeight := itemHeights[i]

		// 检查加入这一项后是否超出限制
		testHeight := currentHeight + itemHeight
		if i < len(items)-1 {
			testHeight += 8 // 下一项的间距
		}

		if testHeight <= availableContentHeight {
			// 可以容纳
			firstCardItems = append(firstCardItems, item)
			currentHeight = testHeight
			splitIndex = i + 1
			fmt.Printf("✅ 第一卡片包含项目 %d，当前高度: %dpx\n", i, currentHeight)
		} else {
			// 不能容纳，停止
			fmt.Printf("🛑 项目 %d 会导致超出限制，停止添加\n", i)
			break
		}
	}

	fmt.Printf("📋 第一卡片包含 %d 项，剩余 %d 项\n", len(firstCardItems), len(items)-splitIndex)

	// 如果第一卡片至少包含一项，创建分割结果
	if len(firstCardItems) > 0 && splitIndex < len(items) {
		var result []Element

		// 第一个卡片（部分列表项）
		firstCard := Element{
			Type:    ElementTypeList,
			Content: firstCardItems,
		}
		result = append(result, firstCard)

		// 剩余项目作为第二个卡片
		remainingItems := items[splitIndex:]
		if len(remainingItems) > 0 {
			secondCard := Element{
				Type:    ElementTypeList,
				Content: remainingItems,
			}
			result = append(result, secondCard)
		}

		fmt.Printf("🎯 分割完成: 第一卡片 %d 项，第二卡片 %d 项\n",
			len(firstCardItems), len(remainingItems))

		return result
	}

	// 如果第一个项目就超出限制，无法分割
	fmt.Printf("❌ 无法分割：第一个项目就超出限制\n")
	return nil
}
