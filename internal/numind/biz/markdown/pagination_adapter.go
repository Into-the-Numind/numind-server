package markdown

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
)

// MarkdownPaginationAdapter Markdown 分页适配器
type MarkdownPaginationAdapter struct {
	processor        *MarkdownProcessor
	paginationEngine pagination.PaginationBiz
	config           *MarkdownPaginationConfig
}

// MarkdownPaginationConfig Markdown 分页配置
type MarkdownPaginationConfig struct {
	MaxCardTextLength   int     `json:"max_card_text_length"`    // 每卡最大文本长度
	MaxCardElements     int     `json:"max_card_elements"`       // 每卡最大元素数
	PreserveParagraph   bool    `json:"preserve_paragraph"`      // 保持段落完整性
	PreserveListItems   bool    `json:"preserve_list_items"`     // 保持列表项完整性
	MaxListItemsPerCard int     `json:"max_list_items_per_card"` // 每卡最大列表项数
	HeadingBreakCard    bool    `json:"heading_break_card"`      // 标题是否自动分卡
	MinCardHeight       int     `json:"min_card_height"`         // 最小卡片高度
	MaxCardHeight       int     `json:"max_card_height"`         // 最大卡片高度
	EstimateRatio       float64 `json:"estimate_ratio"`          // 文本长度估算比例
}

// NewMarkdownPaginationAdapter 创建新的 Markdown 分页适配器
func NewMarkdownPaginationAdapter() *MarkdownPaginationAdapter {
	return &MarkdownPaginationAdapter{
		processor:        NewMarkdownProcessor(),
		paginationEngine: pagination.NewPaginationBiz(),
		config: &MarkdownPaginationConfig{
			MaxCardTextLength:   1800, // 每卡最多1800字符
			MaxCardElements:     8,    // 每卡最多8个元素
			PreserveParagraph:   true,
			PreserveListItems:   true,
			MaxListItemsPerCard: 6,    // 每卡最多6个列表项
			HeadingBreakCard:    true, // 标题自动分卡
			MinCardHeight:       720,  // 最小高度
			MaxCardHeight:       1440, // 最大高度
			EstimateRatio:       1.5,  // 中文字符估算比例
		},
	}
}

// PaginateMarkdownContent 对 Markdown 内容进行分页
func (mpa *MarkdownPaginationAdapter) PaginateMarkdownContent(markdownText string) ([]*MarkdownCardContent, error) {
	// 1. 解析 Markdown 内容
	content, err := mpa.processor.ParseMarkdown(markdownText)
	if err != nil {
		return nil, fmt.Errorf("failed to parse markdown: %v", err)
	}

	log.C(context.Background()).Infow("开始智能分页",
		"title", content.Title,
		"total_blocks", len(content.ContentBlocks))

	// 2. 创建封面卡片（第一张卡片）
	coverCard := &MarkdownCardContent{
		CardIndex:     1,
		Title:         content.Title,
		CoverPrompt:   content.CoverPrompt,
		ContentBlocks: []MarkdownContentBlock{}, // 封面卡片不需要内容块
		IsCoverCard:   true,                     // 标记为封面卡片
	}

	// 3. 基于文字高度进行智能分页（从第二张卡片开始）
	contentCardGroups := mpa.smartPaginateByHeight(content.ContentBlocks)

	// 4. 为每组生成内容卡片
	var cards []*MarkdownCardContent
	cards = append(cards, coverCard) // 添加封面卡片

	for i, group := range contentCardGroups {
		card := &MarkdownCardContent{
			CardIndex:     i + 2, // 从第2张开始
			Title:         content.Title,
			CoverPrompt:   content.CoverPrompt,
			ContentBlocks: group,
			IsCoverCard:   false, // 标记为内容卡片
		}
		cards = append(cards, card)
	}

	log.C(context.Background()).Infow("智能分页完成",
		"total_cards", len(cards),
		"cover_card", true,
		"content_cards", len(contentCardGroups))

	return cards, nil
}

// smartPaginateByHeight 基于文字高度的智能分页
func (mpa *MarkdownPaginationAdapter) smartPaginateByHeight(blocks []MarkdownContentBlock) [][]MarkdownContentBlock {
	if len(blocks) == 0 {
		return [][]MarkdownContentBlock{}
	}

	var groups [][]MarkdownContentBlock
	var currentGroup []MarkdownContentBlock
	var currentHeight int

	// 获取分页配置
	config := mpa.getPaginationConfig()
	maxCardHeight := config.MaxCardHeight
	minCardHeight := config.MinCardHeight

	log.C(context.Background()).Infow("开始高度计算分页",
		"max_card_height", maxCardHeight,
		"min_card_height", minCardHeight,
		"total_blocks", len(blocks))

	for i, block := range blocks {
		// 计算当前块的高度
		blockHeight := mpa.calculateBlockHeight(block, config)

		log.C(context.Background()).Debugw("计算块高度",
			"block_index", i,
			"block_type", block.Type,
			"block_height", blockHeight,
			"current_height", currentHeight)

		// 检查是否需要分页
		needNewCard := false

		// 1. 如果当前高度加上新块高度超过最大高度，需要分页
		if currentHeight+blockHeight > maxCardHeight && len(currentGroup) > 0 {
			needNewCard = true
		}

		// 2. 如果是标题且当前组不为空，强制分页
		if block.Type == "heading" && len(currentGroup) > 0 {
			needNewCard = true
		}

		// 3. 如果当前组已经达到最小高度且新块是标题，分页
		if currentHeight >= minCardHeight && block.Type == "heading" {
			needNewCard = true
		}

		if needNewCard {
			// 保存当前组
			if len(currentGroup) > 0 {
				groups = append(groups, currentGroup)
				log.C(context.Background()).Infow("创建新卡片",
					"card_index", len(groups),
					"blocks_count", len(currentGroup),
					"total_height", currentHeight)
			}

			// 开始新组
			currentGroup = []MarkdownContentBlock{block}
			currentHeight = blockHeight
		} else {
			// 添加到当前组
			currentGroup = append(currentGroup, block)
			currentHeight += blockHeight
		}
	}

	// 添加最后一组
	if len(currentGroup) > 0 {
		groups = append(groups, currentGroup)
		log.C(context.Background()).Infow("创建最后一张卡片",
			"card_index", len(groups),
			"blocks_count", len(currentGroup),
			"total_height", currentHeight)
	}

	return groups
}

// calculateBlockHeight 计算单个内容块的高度
func (mpa *MarkdownPaginationAdapter) calculateBlockHeight(block MarkdownContentBlock, config *PaginationConfig) int {
	baseHeight := 0

	switch block.Type {
	case "heading":
		level := block.Level
		if level < 1 || level > 6 {
			level = 1
		}

		// 根据标题级别计算高度
		switch level {
		case 1: // h1
			baseHeight = config.Styles.Title.FontSize + config.Styles.Title.MarginTop + config.Styles.Title.MarginBottom
		case 2: // h2
			baseHeight = config.Styles.Subtitle.FontSize + config.Styles.Subtitle.MarginTop + config.Styles.Subtitle.MarginBottom
		default: // h3-h6
			baseHeight = config.Styles.Body.FontSize + config.Styles.Body.MarginTop + config.Styles.Body.MarginBottom
		}

	case "paragraph":
		content := fmt.Sprintf("%v", block.Content)
		lines := mpa.estimateTextLines(content, config.Styles.Body.FontSize, config.Styles.Body.LineHeight)
		baseHeight = lines*config.Styles.Body.LineHeight + config.Styles.Body.MarginTop + config.Styles.Body.MarginBottom

	case "list":
		if items, ok := block.Content.([]string); ok {
			for _, item := range items {
				lines := mpa.estimateTextLines(item, config.Styles.List.FontSize, config.Styles.List.LineHeight)
				baseHeight += lines * config.Styles.List.LineHeight
			}
			baseHeight += config.Styles.List.MarginTop + config.Styles.List.MarginBottom
		}

	case "quote":
		content := fmt.Sprintf("%v", block.Content)
		lines := mpa.estimateTextLines(content, config.Styles.Quote.FontSize, config.Styles.Quote.LineHeight)
		baseHeight = lines*config.Styles.Quote.LineHeight + config.Styles.Quote.MarginTop + config.Styles.Quote.MarginBottom

	case "code":
		if codeData, ok := block.Content.(map[string]interface{}); ok {
			if code, exists := codeData["code"].(string); exists {
				lines := mpa.estimateTextLines(code, config.Styles.Body.FontSize, config.Styles.Body.LineHeight)
				baseHeight = lines*config.Styles.Body.LineHeight + config.Styles.Body.MarginTop + config.Styles.Body.MarginBottom
			}
		}

	default:
		// 默认按段落处理
		content := fmt.Sprintf("%v", block.Content)
		lines := mpa.estimateTextLines(content, config.Styles.Body.FontSize, config.Styles.Body.LineHeight)
		baseHeight = lines*config.Styles.Body.LineHeight + config.Styles.Body.MarginTop + config.Styles.Body.MarginBottom
	}

	return baseHeight
}

// estimateTextLines 估算文本行数
func (mpa *MarkdownPaginationAdapter) estimateTextLines(text string, fontSize, lineHeight int) int {
	// 估算每行字符数（基于字体大小和卡片宽度）
	cardWidth := 1080 - 100                        // 卡片宽度减去左右边距
	charsPerLine := cardWidth / (fontSize * 2 / 3) // 中文字符宽度约为字体大小的2/3

	if charsPerLine <= 0 {
		charsPerLine = 30 // 默认值
	}

	// 计算行数
	textLength := len([]rune(text))                         // 使用rune计算中文字符
	lines := (textLength + charsPerLine - 1) / charsPerLine // 向上取整

	if lines < 1 {
		lines = 1
	}

	return lines
}

// getPaginationConfig 获取分页配置
func (mpa *MarkdownPaginationAdapter) getPaginationConfig() *PaginationConfig {
	return &PaginationConfig{
		MaxCardHeight: 1440,
		MinCardHeight: 720,
		Styles: &PaginationStyles{
			Title: &ElementStyle{
				FontSize:     64,
				LineHeight:   90,
				MarginTop:    30,
				MarginBottom: 30,
			},
			Subtitle: &ElementStyle{
				FontSize:     48,
				LineHeight:   72,
				MarginTop:    30,
				MarginBottom: 25,
			},
			Body: &ElementStyle{
				FontSize:     36,
				LineHeight:   58,
				MarginTop:    30,
				MarginBottom: 30,
			},
			List: &ElementStyle{
				FontSize:     36,
				LineHeight:   58,
				MarginTop:    30,
				MarginBottom: 30,
			},
			Quote: &ElementStyle{
				FontSize:     36,
				LineHeight:   54,
				MarginTop:    30,
				MarginBottom: 30,
			},
		},
	}
}

// PaginationConfig 分页配置
type PaginationConfig struct {
	MaxCardHeight int               `json:"max_card_height"`
	MinCardHeight int               `json:"min_card_height"`
	Styles        *PaginationStyles `json:"styles"`
}

// PaginationStyles 分页样式
type PaginationStyles struct {
	Title    *ElementStyle `json:"title"`
	Subtitle *ElementStyle `json:"subtitle"`
	Body     *ElementStyle `json:"body"`
	List     *ElementStyle `json:"list"`
	Quote    *ElementStyle `json:"quote"`
}

// ElementStyle 元素样式
type ElementStyle struct {
	FontSize     int `json:"font_size"`
	LineHeight   int `json:"line_height"`
	MarginTop    int `json:"margin_top"`
	MarginBottom int `json:"margin_bottom"`
}

// MarkdownCardContent Markdown 卡片内容
type MarkdownCardContent struct {
	CardIndex     int                    `json:"card_index"`
	Title         string                 `json:"title"`
	CoverPrompt   string                 `json:"cover_prompt"`
	ContentBlocks []MarkdownContentBlock `json:"content_blocks"`
	EstimatedSize int                    `json:"estimated_size"`
	IsCoverCard   bool                   `json:"is_cover_card"` // 标记是否为封面卡片
}

// groupContentBlocks 将内容块按逻辑分组
func (mpa *MarkdownPaginationAdapter) groupContentBlocks(blocks []MarkdownContentBlock) [][]MarkdownContentBlock {
	if len(blocks) == 0 {
		return [][]MarkdownContentBlock{}
	}

	log.C(context.Background()).Infow("开始分组内容块",
		"total_blocks", len(blocks),
		"max_card_text_length", mpa.config.MaxCardTextLength,
		"max_card_elements", mpa.config.MaxCardElements)

	var groups [][]MarkdownContentBlock
	var currentGroup []MarkdownContentBlock
	var currentSize int

	for i, block := range blocks {
		blockSize := mpa.estimateBlockSize(block)

		log.C(context.Background()).Debugw("处理内容块",
			"block_index", i,
			"block_type", block.Type,
			"block_size", blockSize,
			"current_group_size", len(currentGroup),
			"current_total_size", currentSize)

		// 检查是否需要开始新卡片
		needNewCard := mpa.shouldStartNewCard(currentGroup, currentSize, block, blockSize, i)

		if needNewCard && len(currentGroup) > 0 {
			// 保存当前组并开始新组
			log.C(context.Background()).Infow("创建新卡片组",
				"group_index", len(groups),
				"blocks_count", len(currentGroup),
				"total_size", currentSize)
			groups = append(groups, currentGroup)
			currentGroup = []MarkdownContentBlock{block}
			currentSize = blockSize
		} else {
			// 添加到当前组
			currentGroup = append(currentGroup, block)
			currentSize += blockSize
		}
	}

	// 添加最后一组
	if len(currentGroup) > 0 {
		log.C(context.Background()).Infow("添加最后一组",
			"group_index", len(groups),
			"blocks_count", len(currentGroup),
			"total_size", currentSize)
		groups = append(groups, currentGroup)
	}

	log.C(context.Background()).Infow("内容块分组完成",
		"total_groups", len(groups))

	// 后处理：优化分组
	return mpa.optimizeGroups(groups)
}

// shouldStartNewCard 判断是否应该开始新卡片
func (mpa *MarkdownPaginationAdapter) shouldStartNewCard(
	currentGroup []MarkdownContentBlock,
	currentSize int,
	block MarkdownContentBlock,
	blockSize int,
	blockIndex int,
) bool {
	// 第一个元素，不需要新卡片
	if len(currentGroup) == 0 {
		return false
	}

	// 标题自动分卡
	if mpa.config.HeadingBreakCard && block.Type == "heading" && block.Level <= 2 {
		return true
	}

	// 超过最大文本长度
	if currentSize+blockSize > mpa.config.MaxCardTextLength {
		return true
	}

	// 超过最大元素数
	if len(currentGroup) >= mpa.config.MaxCardElements {
		return true
	}

	// 列表项数量控制
	if block.Type == "list" {
		listItemCount := mpa.countListItems(currentGroup) + mpa.countBlockListItems(block)
		if listItemCount > mpa.config.MaxListItemsPerCard {
			return true
		}
	}

	return false
}

// estimateBlockSize 估算内容块大小
func (mpa *MarkdownPaginationAdapter) estimateBlockSize(block MarkdownContentBlock) int {
	switch block.Type {
	case "heading":
		// 标题占用更多空间
		return int(float64(len(block.RawText)) * 2.0 * mpa.config.EstimateRatio)
	case "list":
		// 列表项需要额外的间距
		listItems := mpa.countBlockListItems(block)
		return int(float64(len(block.RawText))*1.3*mpa.config.EstimateRatio) + listItems*20
	case "table":
		// 表格占用较多空间
		return int(float64(len(block.RawText)) * 2.5 * mpa.config.EstimateRatio)
	case "code":
		// 代码块占用更多空间
		return int(float64(len(block.RawText)) * 1.8 * mpa.config.EstimateRatio)
	case "quote":
		// 引用有额外的边距
		return int(float64(len(block.RawText)) * 1.4 * mpa.config.EstimateRatio)
	default:
		// 普通段落
		return int(float64(len(block.RawText)) * mpa.config.EstimateRatio)
	}
}

// countListItems 统计当前组中的列表项数量
func (mpa *MarkdownPaginationAdapter) countListItems(group []MarkdownContentBlock) int {
	count := 0
	for _, block := range group {
		if block.Type == "list" {
			count += mpa.countBlockListItems(block)
		}
	}
	return count
}

// countBlockListItems 统计单个内容块中的列表项数量
func (mpa *MarkdownPaginationAdapter) countBlockListItems(block MarkdownContentBlock) int {
	if block.Type != "list" {
		return 0
	}

	if items, ok := block.Content.([]string); ok {
		return len(items)
	}

	return 0
}

// optimizeGroups 优化分组结果
func (mpa *MarkdownPaginationAdapter) optimizeGroups(groups [][]MarkdownContentBlock) [][]MarkdownContentBlock {
	if len(groups) <= 1 {
		return groups
	}

	var optimized [][]MarkdownContentBlock

	for i, group := range groups {
		// 检查是否可以与下一组合并
		if i < len(groups)-1 {
			nextGroup := groups[i+1]
			if mpa.canMergeGroups(group, nextGroup) {
				// 合并组
				merged := append(group, nextGroup...)
				optimized = append(optimized, merged)
				i++ // 跳过下一组
			} else {
				optimized = append(optimized, group)
			}
		} else {
			optimized = append(optimized, group)
		}
	}

	return optimized
}

// canMergeGroups 检查两个组是否可以合并
func (mpa *MarkdownPaginationAdapter) canMergeGroups(group1, group2 []MarkdownContentBlock) bool {
	// 计算合并后的大小
	totalSize := 0
	totalElements := len(group1) + len(group2)

	for _, block := range append(group1, group2...) {
		totalSize += mpa.estimateBlockSize(block)
	}

	// 检查是否超过限制
	if totalSize > mpa.config.MaxCardTextLength {
		return false
	}

	if totalElements > mpa.config.MaxCardElements {
		return false
	}

	// 检查第二组是否以重要标题开始
	if len(group2) > 0 && group2[0].Type == "heading" && group2[0].Level <= 2 {
		return false
	}

	return true
}

// ConvertToJsonElements 将 Markdown 卡片内容转换为 JSON 元素格式（兼容现有渲染器）
func (mpa *MarkdownPaginationAdapter) ConvertToJsonElements(card *MarkdownCardContent) ([]pagination.Element, error) {
	return mpa.processor.ConvertToPaginationElements(&MarkdownContent{
		Title:         card.Title,
		CoverPrompt:   card.CoverPrompt,
		ContentBlocks: card.ContentBlocks,
	})
}

// ConvertToJsonString 将 Markdown 卡片内容转换为 JSON 字符串（用于数据库存储）
func (mpa *MarkdownPaginationAdapter) ConvertToJsonString(card *MarkdownCardContent) (string, error) {
	return mpa.processor.ConvertBlocksToJSON(card.ContentBlocks)
}

// SplitLongContent 分割过长的内容
func (mpa *MarkdownPaginationAdapter) SplitLongContent(blocks []MarkdownContentBlock, maxLength int) [][]MarkdownContentBlock {
	var result [][]MarkdownContentBlock
	var currentGroup []MarkdownContentBlock
	var currentLength int

	for _, block := range blocks {
		blockLength := mpa.estimateBlockSize(block)

		// 如果单个块就超过限制，需要进一步分割
		if blockLength > maxLength {
			// 先保存当前组
			if len(currentGroup) > 0 {
				result = append(result, currentGroup)
				currentGroup = []MarkdownContentBlock{}
				currentLength = 0
			}

			// 分割大块
			splitBlocks := mpa.splitLargeBlock(block, maxLength)
			for _, splitBlock := range splitBlocks {
				result = append(result, []MarkdownContentBlock{splitBlock})
			}
		} else if currentLength+blockLength > maxLength && len(currentGroup) > 0 {
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

// splitLargeBlock 分割大的内容块
func (mpa *MarkdownPaginationAdapter) splitLargeBlock(block MarkdownContentBlock, maxLength int) []MarkdownContentBlock {
	switch block.Type {
	case "paragraph":
		return mpa.splitParagraph(block, maxLength)
	case "list":
		return mpa.splitList(block, maxLength)
	default:
		// 其他类型暂时不分割，保持原样
		return []MarkdownContentBlock{block}
	}
}

// splitParagraph 分割段落
func (mpa *MarkdownPaginationAdapter) splitParagraph(block MarkdownContentBlock, maxLength int) []MarkdownContentBlock {
	content := fmt.Sprintf("%v", block.Content)
	sentences := strings.Split(content, "。")

	var result []MarkdownContentBlock
	var currentPart []string
	var currentLength int

	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}

		sentenceLength := len(sentence + "。")

		if currentLength+sentenceLength > maxLength && len(currentPart) > 0 {
			// 创建新的段落块
			newBlock := MarkdownContentBlock{
				Type:    "paragraph",
				Content: strings.Join(currentPart, "。") + "。",
				RawText: strings.Join(currentPart, "。") + "。",
			}
			result = append(result, newBlock)
			currentPart = []string{sentence}
			currentLength = sentenceLength
		} else {
			currentPart = append(currentPart, sentence)
			currentLength += sentenceLength
		}
	}

	// 添加最后一部分
	if len(currentPart) > 0 {
		newBlock := MarkdownContentBlock{
			Type:    "paragraph",
			Content: strings.Join(currentPart, "。") + "。",
			RawText: strings.Join(currentPart, "。") + "。",
		}
		result = append(result, newBlock)
	}

	return result
}

// splitList 分割列表
func (mpa *MarkdownPaginationAdapter) splitList(block MarkdownContentBlock, maxLength int) []MarkdownContentBlock {
	if items, ok := block.Content.([]string); ok {
		var result []MarkdownContentBlock
		var currentItems []string
		var currentLength int

		for _, item := range items {
			itemLength := len(item) * 2 // 列表项占用更多空间

			if currentLength+itemLength > maxLength && len(currentItems) > 0 {
				// 创建新的列表块
				newBlock := MarkdownContentBlock{
					Type:    "list",
					Content: currentItems,
					RawText: mpa.buildListRawText(currentItems),
				}
				result = append(result, newBlock)
				currentItems = []string{item}
				currentLength = itemLength
			} else {
				currentItems = append(currentItems, item)
				currentLength += itemLength
			}
		}

		// 添加最后一部分
		if len(currentItems) > 0 {
			newBlock := MarkdownContentBlock{
				Type:    "list",
				Content: currentItems,
				RawText: mpa.buildListRawText(currentItems),
			}
			result = append(result, newBlock)
		}

		return result
	}

	return []MarkdownContentBlock{block}
}

// buildListRawText 构建列表的原始文本
func (mpa *MarkdownPaginationAdapter) buildListRawText(items []string) string {
	var lines []string
	for _, item := range items {
		lines = append(lines, "- "+item)
	}
	return strings.Join(lines, "\n")
}

// UpdateConfig 更新配置
func (mpa *MarkdownPaginationAdapter) UpdateConfig(config *MarkdownPaginationConfig) {
	mpa.config = config
}

// GetConfig 获取当前配置
func (mpa *MarkdownPaginationAdapter) GetConfig() *MarkdownPaginationConfig {
	return mpa.config
}
