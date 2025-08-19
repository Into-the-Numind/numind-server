package book

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// EnhancedRendererIntegration 增强版渲染器集成器
// 将新的增强版渲染系统集成到现有的book创建流程中
type EnhancedRendererIntegration struct {
	coordinator *card.CardRendererCoordinator
	biz         BizInterface
}

// NewEnhancedRendererIntegration 创建增强版渲染器集成器
func NewEnhancedRendererIntegration(biz BizInterface, config *pagination.PaginationConfig) *EnhancedRendererIntegration {
	return &EnhancedRendererIntegration{
		coordinator: card.NewCardRendererCoordinator(config),
		biz:         biz,
	}
}

// ProcessBookWithEnhancedRendering 使用增强版渲染器处理整本书
// 这是替代原来渲染逻辑的新方法
func (e *EnhancedRendererIntegration) ProcessBookWithEnhancedRendering(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
) error {
	log.C(ctx).Infow("开始增强版渲染处理", "book_id", book.ID, "user_id", userID, "elements_count", len(structuredTextArray))

	// 1. 自动选择最佳渲染策略
	strategy, options := e.coordinator.GetOptimalStrategy(structuredTextArray)
	log.C(ctx).Infow("选择渲染策略", "book_id", book.ID, "strategy", strategy, "options", options)

	// 2. 使用增强版渲染器进行渲染
	renderedCards, err := e.coordinator.RenderBookWithStrategy(
		book,
		structuredTextArray,
		imagePromptURL,
		options,
	)
	if err != nil {
		log.C(ctx).Errorw("增强版渲染失败", "book_id", book.ID, "error", err.Error())
		return fmt.Errorf("增强版渲染失败: %v", err)
	}

	// 3. 验证渲染结果
	if err := e.coordinator.ValidateRenderingResult(renderedCards); err != nil {
		log.C(ctx).Errorw("渲染结果验证失败", "book_id", book.ID, "error", err.Error())
		return fmt.Errorf("渲染结果验证失败: %v", err)
	}

	// 4. 保存渲染结果到数据库
	if err := e.saveRenderedCards(ctx, book.ID, userID, renderedCards, structuredTextArray); err != nil {
		log.C(ctx).Errorw("保存渲染结果失败", "book_id", book.ID, "error", err.Error())
		return fmt.Errorf("保存渲染结果失败: %v", err)
	}

	// 5. 更新book统计信息
	book.CardCount = len(renderedCards)
	if err := e.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新book统计失败", "book_id", book.ID, "error", err.Error())
		// 统计更新失败不影响主流程
	}

	// 6. 更新用户卡片统计
	if err := e.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("更新用户卡片统计失败", "book_id", book.ID, "error", err.Error())
		// 统计更新失败不影响主流程
	}

	log.C(ctx).Infow("增强版渲染处理完成", "book_id", book.ID, "rendered_cards", len(renderedCards))
	return nil
}

// saveRenderedCards 保存渲染结果到数据库
func (e *EnhancedRendererIntegration) saveRenderedCards(
	ctx context.Context,
	bookID uint,
	userID uint,
	renderedCards []*card.RenderedCard,
	structuredTextArray []pagination.Element,
) error {
	log.C(ctx).Infow("开始保存渲染卡片", "book_id", bookID, "card_count", len(renderedCards))

	// 重新构建结构化数据，为每张卡片分配正确的内容
	cardElementsMapping := e.buildCardElementsMapping(structuredTextArray, len(renderedCards))

	for i, renderedCard := range renderedCards {
		// 获取当前卡片的元素内容
		var cardElements []map[string]interface{}

		if i == 0 {
			// 第一张卡片包含title和阿里文生图信息
			titleContent := e.extractTitleContent(structuredTextArray)
			cardElements = []map[string]interface{}{
				{
					"type":    "title",
					"content": titleContent,
				},
				{
					"type":    "image_prompt",
					"content": "首张卡片特殊布局", // 标记这是第一张特殊卡片
				},
			}
		} else {
			// 后续卡片使用映射的元素
			if cardIndex := i - 1; cardIndex < len(cardElementsMapping) {
				for _, element := range cardElementsMapping[cardIndex] {
					cardElements = append(cardElements, map[string]interface{}{
						"type":    element.Type,
						"content": element.Content,
					})
				}
			}
		}

		// 将元素内容转换为JSON
		cardJSONStr, err := json.Marshal(cardElements)
		if err != nil {
			log.C(ctx).Errorw("序列化卡片JSON失败", "book_id", bookID, "card_index", i, "error", err.Error())
			continue
		}

		// 创建卡片记录
		cardRecord := &model.CardM{
			UserID:        userID,
			BookID:        bookID,
			ProcessedText: string(cardJSONStr),
			RenderedImage: renderedCard.ImageURL,
			SortOrder:     renderedCard.SortOrder,
		}

		if err := e.biz.Cards().Create(ctx, cardRecord); err != nil {
			log.C(ctx).Errorw("创建卡片记录失败", "book_id", bookID, "card_index", i, "error", err.Error())
			continue
		}

		log.C(ctx).Infow("卡片记录创建成功",
			"book_id", bookID,
			"card_id", cardRecord.ID,
			"sort_order", cardRecord.SortOrder,
			"image_url", cardRecord.RenderedImage)
	}

	log.C(ctx).Infow("所有卡片保存完成", "book_id", bookID)
	return nil
}

// buildCardElementsMapping 构建卡片元素映射关系
// 根据渲染策略和分页结果，将结构化文本正确分配到各张卡片
func (e *EnhancedRendererIntegration) buildCardElementsMapping(
	structuredTextArray []pagination.Element,
	totalCards int,
) [][]pagination.Element {
	// 过滤掉第一个title（已用于第一张卡片）
	remainingElements := e.filterOutFirstTitle(structuredTextArray)

	if len(remainingElements) == 0 {
		return [][]pagination.Element{}
	}

	// 简单的平均分配策略
	// 实际应该根据渲染器的分页算法来分配，但这里先用简单方法
	elementsPerCard := (len(remainingElements) + totalCards - 2) / max(1, totalCards-1) // 减1是因为第一张卡片特殊处理

	var mapping [][]pagination.Element
	for i := 0; i < len(remainingElements); i += elementsPerCard {
		end := i + elementsPerCard
		if end > len(remainingElements) {
			end = len(remainingElements)
		}

		if i < len(remainingElements) {
			mapping = append(mapping, remainingElements[i:end])
		}
	}

	return mapping
}

// extractTitleContent 提取标题内容
func (e *EnhancedRendererIntegration) extractTitleContent(elements []pagination.Element) string {
	for _, element := range elements {
		if element.Type == pagination.ElementTypeTitle {
			if content, ok := element.Content.(string); ok {
				return content
			}
		}
	}
	return "默认标题"
}

// filterOutFirstTitle 过滤掉第一个title，返回剩余元素
func (e *EnhancedRendererIntegration) filterOutFirstTitle(elements []pagination.Element) []pagination.Element {
	var remaining []pagination.Element
	titleUsed := false

	for _, element := range elements {
		if element.Type == pagination.ElementTypeTitle && !titleUsed {
			titleUsed = true
			continue
		}
		remaining = append(remaining, element)
	}

	return remaining
}

// CreateCoverCardWithEnhanced 使用增强版渲染器创建封面卡片
// 这是可选的封面卡片处理方法，如果需要特殊的封面设计
func (e *EnhancedRendererIntegration) CreateCoverCardWithEnhanced(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	coverBackground string,
) (*model.CardM, error) {
	log.C(ctx).Infow("创建增强版封面卡片", "book_id", book.ID)

	// 使用现有的封面渲染器，这部分保持不变
	// 因为封面有特殊的背景处理需求
	paginationBiz := pagination.NewPaginationBiz()
	coverRenderer := card.NewCoverRenderer(paginationBiz.GetConfig())

	if err := coverRenderer.SetTemplateBackground(coverBackground); err != nil {
		log.C(ctx).Warnw("设置封面背景失败", "book_id", book.ID, "background", coverBackground, "error", err.Error())
	}

	// 创建封面卡片记录
	coverCardRecord := &model.CardM{
		UserID:    userID,
		BookID:    book.ID,
		SortOrder: 0, // 封面卡片排序为0
	}

	// 构造封面数据
	var coverElements []map[string]interface{}
	coverElements = append(coverElements, map[string]interface{}{"type": "title", "content": book.Title})
	if book.ImageUrl != "" {
		coverElements = append(coverElements, map[string]interface{}{"type": "image", "content": book.ImageUrl})
	}
	if coverBackground != "" {
		coverElements = append(coverElements, map[string]interface{}{"type": "background", "content": coverBackground})
	}

	if b, err := json.Marshal(coverElements); err == nil {
		coverCardRecord.ProcessedText = string(b)
	}

	// 先创建记录
	if err := e.biz.Cards().Create(ctx, coverCardRecord); err != nil {
		return nil, fmt.Errorf("创建封面卡片记录失败: %v", err)
	}

	// 渲染封面卡片
	renderedCoverCard, err := coverRenderer.RenderCoverCardToImage(coverCardRecord)
	if err != nil {
		log.C(ctx).Errorw("渲染封面卡片失败", "book_id", book.ID, "error", err.Error())
		return coverCardRecord, nil // 返回记录，即使渲染失败
	}

	// 更新渲染图片
	coverCardRecord.RenderedImage = renderedCoverCard.ImageURL
	if err := e.biz.Cards().Update(ctx, coverCardRecord); err != nil {
		log.C(ctx).Errorw("更新封面卡片渲染图片失败", "book_id", book.ID, "error", err.Error())
	}

	// 更新book的image_url为封面卡片的渲染图片
	book.ImageUrl = coverCardRecord.RenderedImage
	if err := e.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新book封面图片失败", "book_id", book.ID, "error", err.Error())
	}

	log.C(ctx).Infow("增强版封面卡片创建完成", "book_id", book.ID, "image_url", coverCardRecord.RenderedImage)
	return coverCardRecord, nil
}

// max 辅助函数
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
