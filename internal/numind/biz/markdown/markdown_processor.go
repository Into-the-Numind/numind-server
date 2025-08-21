package markdown

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
)

// MarkdownProcessor Markdown 内容处理器
type MarkdownProcessor struct {
	config *MarkdownConfig
}

// MarkdownConfig Markdown 处理配置
type MarkdownConfig struct {
	MaxCardTextLength int  `json:"max_card_text_length"` // 每卡最大文本长度
	PreserveParagraph bool `json:"preserve_paragraph"`   // 是否保持段落完整性
	MaxListItems      int  `json:"max_list_items"`       // 每卡最大列表项数
}

// MarkdownContent 解析后的 Markdown 内容
type MarkdownContent struct {
	Title         string                 `json:"title"`
	CoverPrompt   string                 `json:"cover_prompt"`
	ContentBlocks []MarkdownContentBlock `json:"content_blocks"`
}

// MarkdownContentBlock Markdown 内容块
type MarkdownContentBlock struct {
	Type    string      `json:"type"`     // heading, paragraph, list, table, quote, code
	Level   int         `json:"level"`    // 标题级别 (1-6)
	Content interface{} `json:"content"`  // 具体内容
	RawText string      `json:"raw_text"` // 原始 Markdown 文本
}

// NewMarkdownProcessor 创建新的 Markdown 处理器
func NewMarkdownProcessor() *MarkdownProcessor {
	return &MarkdownProcessor{
		config: &MarkdownConfig{
			MaxCardTextLength: 2000,
			PreserveParagraph: true,
			MaxListItems:      10,
		},
	}
}

// ParseMarkdown 解析 Markdown 内容
func (mp *MarkdownProcessor) ParseMarkdown(markdown string) (*MarkdownContent, error) {
	if strings.TrimSpace(markdown) == "" {
		return nil, fmt.Errorf("markdown content is empty")
	}

	content := &MarkdownContent{
		ContentBlocks: []MarkdownContentBlock{},
	}

	// 提取标题 (第一个 # 开头的行)
	content.Title = mp.extractTitle(markdown)

	// 提取封面提示词 (第一个 ![cover] 或类似格式)
	content.CoverPrompt = mp.extractCoverPrompt(markdown)

	// 解析内容块
	blocks, err := mp.parseContentBlocks(markdown)
	if err != nil {
		return nil, fmt.Errorf("failed to parse content blocks: %v", err)
	}
	content.ContentBlocks = blocks

	return content, nil
}

// extractTitle 提取标题
func (mp *MarkdownProcessor) extractTitle(markdown string) string {
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") && len(line) > 2 {
			return strings.TrimSpace(line[2:])
		}
	}
	return "无标题"
}

// extractCoverPrompt 提取封面提示词
func (mp *MarkdownProcessor) extractCoverPrompt(markdown string) string {
	log.C(context.Background()).Infow("开始提取封面提示词",
		"markdown_length", len(markdown),
		"markdown_preview", truncateString(markdown, 300))

	// 首先尝试匹配HTML注释格式的image_prompt
	htmlCommentPattern := regexp.MustCompile(`<!--\s*image_prompt\s+(.+?)\s*-->`)
	htmlMatches := htmlCommentPattern.FindStringSubmatch(markdown)

	log.C(context.Background()).Infow("HTML注释格式匹配结果",
		"pattern", `<!--\s*image_prompt\s+(.+?)\s*-->`,
		"matches_count", len(htmlMatches),
		"matches", htmlMatches)

	if len(htmlMatches) > 1 {
		extractedPrompt := strings.TrimSpace(htmlMatches[1])
		log.C(context.Background()).Infow("成功提取HTML注释格式封面提示词",
			"extracted_prompt", extractedPrompt,
			"prompt_length", len(extractedPrompt))
		return extractedPrompt
	}

	// 如果没有找到HTML注释格式，尝试匹配 ![cover](提示词) 格式
	coverPattern := regexp.MustCompile(`!\[cover\]\(([^)]+)\)`)
	matches := coverPattern.FindStringSubmatch(markdown)

	log.C(context.Background()).Infow("Markdown格式匹配结果",
		"pattern", `!\[cover\]\(([^)]+)\)`,
		"matches_count", len(matches),
		"matches", matches)

	if len(matches) > 1 {
		extractedPrompt := strings.TrimSpace(matches[1])
		log.C(context.Background()).Infow("成功提取Markdown格式封面提示词",
			"extracted_prompt", extractedPrompt,
			"prompt_length", len(extractedPrompt))
		return extractedPrompt
	}

	// 如果没有找到，使用默认的封面提示词生成逻辑
	title := mp.extractTitle(markdown)
	defaultPrompt := fmt.Sprintf("根据标题'%s'生成精美的封面图片", title)

	log.C(context.Background()).Warnw("未找到封面提示词，使用默认提示词",
		"title", title,
		"default_prompt", defaultPrompt)

	return defaultPrompt
}

// parseContentBlocks 解析内容块
func (mp *MarkdownProcessor) parseContentBlocks(markdown string) ([]MarkdownContentBlock, error) {
	var blocks []MarkdownContentBlock
	lines := strings.Split(markdown, "\n")

	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])

		// 跳过空行
		if line == "" {
			i++
			continue
		}

		// 解析不同类型的内容块
		switch {
		case mp.isHeading(line):
			block := mp.parseHeading(line)
			blocks = append(blocks, block)
			i++

		case mp.isImage(line):
			block := mp.parseImage(line)
			blocks = append(blocks, block)
			i++

		case mp.isList(line):
			block, consumed := mp.parseList(lines, i)
			blocks = append(blocks, block)
			i += consumed

		case mp.isTable(lines, i):
			block, consumed := mp.parseTable(lines, i)
			blocks = append(blocks, block)
			i += consumed

		case mp.isQuote(line):
			block, consumed := mp.parseQuote(lines, i)
			blocks = append(blocks, block)
			i += consumed

		case mp.isCodeBlock(line):
			block, consumed := mp.parseCodeBlock(lines, i)
			blocks = append(blocks, block)
			i += consumed

		default:
			// 普通段落
			block, consumed := mp.parseParagraph(lines, i)
			blocks = append(blocks, block)
			i += consumed
		}
	}

	return blocks, nil
}

// isHeading 检查是否为标题
func (mp *MarkdownProcessor) isHeading(line string) bool {
	return regexp.MustCompile(`^#{1,6}\s+.+`).MatchString(line)
}

// isImage 检查是否为图片
func (mp *MarkdownProcessor) isImage(line string) bool {
	return regexp.MustCompile(`^!\[.*\]\(.*\)$`).MatchString(strings.TrimSpace(line))
}

// parseHeading 解析标题
func (mp *MarkdownProcessor) parseHeading(line string) MarkdownContentBlock {
	level := 0
	content := line

	// 计算标题级别
	for i, char := range line {
		if char == '#' {
			level++
		} else if char == ' ' {
			content = strings.TrimSpace(line[i:])
			break
		} else {
			break
		}
	}

	return MarkdownContentBlock{
		Type:    "heading",
		Level:   level,
		Content: content,
		RawText: line,
	}
}

// parseImage 解析图片
func (mp *MarkdownProcessor) parseImage(line string) MarkdownContentBlock {
	// 匹配 ![alt](url) 格式
	imagePattern := regexp.MustCompile(`^!\[(.*?)\]\((.*?)\)$`)
	matches := imagePattern.FindStringSubmatch(strings.TrimSpace(line))

	if len(matches) >= 3 {
		imageURL := matches[2]

		return MarkdownContentBlock{
			Type:    "image",
			Content: imageURL,
			RawText: line,
		}
	}

	// 如果解析失败，返回原始内容
	return MarkdownContentBlock{
		Type:    "image",
		Content: line,
		RawText: line,
	}
}

// isList 检查是否为列表
func (mp *MarkdownProcessor) isList(line string) bool {
	return regexp.MustCompile(`^[\s]*[-*+]\s+.+|^[\s]*\d+\.\s+.+`).MatchString(line)
}

// parseList 解析列表
func (mp *MarkdownProcessor) parseList(lines []string, start int) (MarkdownContentBlock, int) {
	var items []string
	consumed := 0

	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			// 空行，检查下一行是否还是列表项
			if i+1 < len(lines) && mp.isList(strings.TrimSpace(lines[i+1])) {
				consumed++
				continue
			} else {
				break
			}
		}

		if mp.isList(line) {
			// 提取列表项内容
			content := mp.extractListItemContent(line)
			items = append(items, content)
			consumed++
		} else {
			break
		}
	}

	var rawText strings.Builder
	for i := start; i < start+consumed; i++ {
		rawText.WriteString(lines[i] + "\n")
	}

	return MarkdownContentBlock{
		Type:    "list",
		Content: items,
		RawText: strings.TrimSpace(rawText.String()),
	}, consumed
}

// extractListItemContent 提取列表项内容
func (mp *MarkdownProcessor) extractListItemContent(line string) string {
	// 移除列表标记 (-, *, +, 1., 2., etc.)
	re := regexp.MustCompile(`^[\s]*(?:[-*+]|\d+\.)\s+`)
	return re.ReplaceAllString(line, "")
}

// isTable 检查是否为表格
func (mp *MarkdownProcessor) isTable(lines []string, start int) bool {
	if start >= len(lines) {
		return false
	}

	line := strings.TrimSpace(lines[start])
	// 简单检查：包含 | 字符且不是单独的 |
	return strings.Contains(line, "|") && len(strings.Split(line, "|")) > 2
}

// parseTable 解析表格
func (mp *MarkdownProcessor) parseTable(lines []string, start int) (MarkdownContentBlock, int) {
	var tableRows [][]string
	consumed := 0

	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			break
		}

		if strings.Contains(line, "|") {
			// 解析表格行
			cells := strings.Split(line, "|")
			var cleanCells []string
			for _, cell := range cells {
				cell = strings.TrimSpace(cell)
				if cell != "" {
					cleanCells = append(cleanCells, cell)
				}
			}
			if len(cleanCells) > 0 {
				tableRows = append(tableRows, cleanCells)
			}
			consumed++
		} else {
			break
		}
	}

	var rawText strings.Builder
	for i := start; i < start+consumed; i++ {
		rawText.WriteString(lines[i] + "\n")
	}

	return MarkdownContentBlock{
		Type:    "table",
		Content: tableRows,
		RawText: strings.TrimSpace(rawText.String()),
	}, consumed
}

// isQuote 检查是否为引用
func (mp *MarkdownProcessor) isQuote(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), ">")
}

// parseQuote 解析引用
func (mp *MarkdownProcessor) parseQuote(lines []string, start int) (MarkdownContentBlock, int) {
	var quoteLines []string
	consumed := 0

	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			// 空行，检查下一行是否还是引用
			if i+1 < len(lines) && mp.isQuote(strings.TrimSpace(lines[i+1])) {
				consumed++
				continue
			} else {
				break
			}
		}

		if strings.HasPrefix(line, ">") {
			content := strings.TrimSpace(line[1:])
			quoteLines = append(quoteLines, content)
			consumed++
		} else {
			break
		}
	}

	var rawText strings.Builder
	for i := start; i < start+consumed; i++ {
		rawText.WriteString(lines[i] + "\n")
	}

	return MarkdownContentBlock{
		Type:    "quote",
		Content: strings.Join(quoteLines, "\n"),
		RawText: strings.TrimSpace(rawText.String()),
	}, consumed
}

// isCodeBlock 检查是否为代码块
func (mp *MarkdownProcessor) isCodeBlock(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// parseCodeBlock 解析代码块
func (mp *MarkdownProcessor) parseCodeBlock(lines []string, start int) (MarkdownContentBlock, int) {
	consumed := 1 // 开始的 ``` 行
	var codeLines []string
	language := ""

	// 提取语言标识
	startLine := strings.TrimSpace(lines[start])
	if len(startLine) > 3 {
		language = strings.TrimSpace(startLine[3:])
	}

	// 查找结束的 ```
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			consumed++
			break
		}
		codeLines = append(codeLines, line)
		consumed++
	}

	var rawText strings.Builder
	for i := start; i < start+consumed; i++ {
		rawText.WriteString(lines[i] + "\n")
	}

	codeContent := map[string]interface{}{
		"language": language,
		"code":     strings.Join(codeLines, "\n"),
	}

	return MarkdownContentBlock{
		Type:    "code",
		Content: codeContent,
		RawText: strings.TrimSpace(rawText.String()),
	}, consumed
}

// parseParagraph 解析段落
func (mp *MarkdownProcessor) parseParagraph(lines []string, start int) (MarkdownContentBlock, int) {
	var paragraphLines []string
	consumed := 0

	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		// 遇到空行或特殊格式行则停止
		if line == "" || mp.isHeading(line) || mp.isList(line) ||
			mp.isQuote(line) || mp.isCodeBlock(line) {
			break
		}

		paragraphLines = append(paragraphLines, line)
		consumed++
	}

	content := strings.Join(paragraphLines, " ")
	rawText := strings.Join(paragraphLines, "\n")

	return MarkdownContentBlock{
		Type:    "paragraph",
		Content: content,
		RawText: rawText,
	}, consumed
}

// ConvertToPaginationElements 将 Markdown 内容块转换为分页元素
func (mp *MarkdownProcessor) ConvertToPaginationElements(content *MarkdownContent) ([]pagination.Element, error) {
	var elements []pagination.Element

	for _, block := range content.ContentBlocks {
		switch block.Type {
		case "heading":
			elementType := pagination.ElementTypeTitle
			if block.Level > 1 {
				elementType = pagination.ElementTypeSubtitle
			}
			elements = append(elements, pagination.Element{
				Type:    elementType,
				Content: block.Content,
			})

		case "paragraph":
			elements = append(elements, pagination.Element{
				Type:    pagination.ElementTypeBody,
				Content: block.Content,
			})

		case "list":
			elements = append(elements, pagination.Element{
				Type:    pagination.ElementTypeList,
				Content: block.Content,
			})

		case "quote":
			elements = append(elements, pagination.Element{
				Type:    pagination.ElementTypeQuote,
				Content: block.Content,
			})

		case "table", "code":
			// 表格和代码块当作特殊的段落处理
			content := mp.formatComplexContent(block)
			elements = append(elements, pagination.Element{
				Type:    pagination.ElementTypeBody,
				Content: content,
			})
		}
	}

	return elements, nil
}

// formatComplexContent 格式化复杂内容（表格、代码等）
func (mp *MarkdownProcessor) formatComplexContent(block MarkdownContentBlock) string {
	switch block.Type {
	case "table":
		if rows, ok := block.Content.([][]string); ok {
			var formatted strings.Builder
			for i, row := range rows {
				if i == 0 {
					formatted.WriteString("【表格】\n")
				}
				formatted.WriteString(strings.Join(row, " | "))
				formatted.WriteString("\n")
			}
			return formatted.String()
		}
	case "code":
		if codeData, ok := block.Content.(map[string]interface{}); ok {
			var formatted strings.Builder
			if lang, exists := codeData["language"].(string); exists && lang != "" {
				formatted.WriteString(fmt.Sprintf("【%s代码】\n", lang))
			} else {
				formatted.WriteString("【代码】\n")
			}
			if code, exists := codeData["code"].(string); exists {
				formatted.WriteString(code)
			}
			return formatted.String()
		}
	}
	return block.RawText
}

// EstimateTextLength 估算文本长度（考虑中英文差异）
func (mp *MarkdownProcessor) EstimateTextLength(text string) int {
	// 中文字符按2计算，英文字符按1计算
	length := 0
	for _, r := range text {
		if r > 127 {
			length += 2 // 非ASCII字符（包括中文）
		} else {
			length += 1 // ASCII字符
		}
	}
	return length
}

// SplitByLength 按长度分割内容块
func (mp *MarkdownProcessor) SplitByLength(blocks []MarkdownContentBlock, maxLength int) [][]MarkdownContentBlock {
	var result [][]MarkdownContentBlock
	var currentGroup []MarkdownContentBlock
	var currentLength int

	for _, block := range blocks {
		blockLength := mp.EstimateTextLength(fmt.Sprintf("%v", block.Content))

		if currentLength+blockLength > maxLength && len(currentGroup) > 0 {
			// 当前组已满，开始新组
			result = append(result, currentGroup)
			currentGroup = []MarkdownContentBlock{block}
			currentLength = blockLength
		} else {
			// 添加到当前组
			currentGroup = append(currentGroup, block)
			currentLength += blockLength
		}
	}

	// 添加最后一组
	if len(currentGroup) > 0 {
		result = append(result, currentGroup)
	}

	return result
}

// ConvertBlocksToJSON 将内容块转换为JSON（用于数据库存储）
func (mp *MarkdownProcessor) ConvertBlocksToJSON(blocks []MarkdownContentBlock) (string, error) {
	data, err := json.Marshal(blocks)
	if err != nil {
		return "", fmt.Errorf("failed to marshal blocks to JSON: %v", err)
	}
	return string(data), nil
}

// ConvertJSONToBlocks 从JSON恢复内容块
func (mp *MarkdownProcessor) ConvertJSONToBlocks(jsonStr string) ([]MarkdownContentBlock, error) {
	var blocks []MarkdownContentBlock
	if err := json.Unmarshal([]byte(jsonStr), &blocks); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON to blocks: %v", err)
	}
	return blocks, nil
}
