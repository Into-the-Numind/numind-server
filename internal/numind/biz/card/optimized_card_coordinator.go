package card

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// OptimizedCardCoordinator 优化卡片协调器 - 集成所有优化功能
type OptimizedCardCoordinator struct {
	dynamicRenderer *EnhancedDynamicRenderer
	coverOptimizer  *CoverOptimizer
	config          *pagination.DynamicPaginationConfig
}

// NewOptimizedCardCoordinator 创建优化卡片协调器
func NewOptimizedCardCoordinator() *OptimizedCardCoordinator {
	config := pagination.GetDynamicConfig()

	return &OptimizedCardCoordinator{
		dynamicRenderer: NewEnhancedDynamicRenderer(config),
		coverOptimizer:  NewCoverOptimizer(),
		config:          config,
	}
}

// RenderOptimizedBook 渲染优化书籍（支持大内容量）
func (c *OptimizedCardCoordinator) RenderOptimizedBook(ctx context.Context, book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	log.C(ctx).Infow("开始优化卡片渲染",
		"book_id", book.ID,
		"book_title", book.Title,
		"card_count", len(cards),
		"optimization_mode", "enhanced_dynamic")

	// 验证输入参数
	if book == nil {
		return nil, fmt.Errorf("书籍信息不能为空")
	}

	if len(cards) == 0 {
		log.C(ctx).Infow("卡片列表为空，跳过渲染")
		return []*RenderedCard{}, nil
	}

	// 统计内容量
	totalImages, totalTextLength := c.analyzeContent(cards)
	log.C(ctx).Infow("内容量分析",
		"total_images", totalImages,
		"total_text_length", totalTextLength,
		"support_limit_images", c.config.MaxImagesPerCard,
		"support_limit_text", c.config.MaxTextLength)

	// 检查是否超出处理能力
	if totalImages > c.config.MaxImagesPerCard*len(cards) {
		log.C(ctx).Warnw("图片数量可能过多，将使用分页处理", "total_images", totalImages)
	}

	// 优化封面图（如果存在）
	var coverInfo *CoverImageInfo
	if book.ImageUrl != "" {
		var err error
		coverInfo, err = c.optimizeCoverImage(ctx, book.ImageUrl)
		if err != nil {
			log.C(ctx).Warnw("封面图优化失败，使用原图", "error", err.Error())
		} else {
			log.C(ctx).Infow("封面图优化成功", "optimized_size", coverInfo.OptimizedSize)
		}
	}

	// 使用增强动态渲染器进行渲染
	startTime := time.Now()
	renderedCards, err := c.dynamicRenderer.RenderBookToImages(book, cards)
	if err != nil {
		log.C(ctx).Errorw("增强动态渲染失败", "error", err.Error())
		return nil, fmt.Errorf("优化渲染失败: %v", err)
	}

	duration := time.Since(startTime)
	log.C(ctx).Infow("优化卡片渲染完成",
		"rendered_cards", len(renderedCards),
		"duration_ms", duration.Milliseconds(),
		"avg_ms_per_card", duration.Milliseconds()/int64(len(renderedCards)))

	// 验证渲染结果
	if err := c.validateRenderedCards(renderedCards); err != nil {
		log.C(ctx).Warnw("渲染结果验证失败", "error", err.Error())
	}

	return renderedCards, nil
}

// RenderOptimizedCard 渲染优化单个卡片
func (c *OptimizedCardCoordinator) RenderOptimizedCard(ctx context.Context, card *model.CardM) (*RenderedCard, error) {
	log.C(ctx).Infow("开始优化单个卡片渲染", "card_id", card.ID)

	if card == nil {
		return nil, fmt.Errorf("卡片信息不能为空")
	}

	// 分析单个卡片内容
	contentAnalysis := c.analyzeSingleCard(card)
	log.C(ctx).Infow("单卡片内容分析",
		"card_id", card.ID,
		"elements_count", contentAnalysis.ElementsCount,
		"images_count", contentAnalysis.ImagesCount,
		"text_length", contentAnalysis.TextLength,
		"estimated_height", contentAnalysis.EstimatedHeight)

	// 使用增强动态渲染器
	renderedCard, err := c.dynamicRenderer.RenderCardToImage(card)
	if err != nil {
		log.C(ctx).Errorw("优化单卡片渲染失败", "card_id", card.ID, "error", err.Error())
		return nil, fmt.Errorf("优化渲染失败: %v", err)
	}

	log.C(ctx).Infow("优化单卡片渲染完成",
		"card_id", renderedCard.CardID,
		"image_url", renderedCard.ImageURL,
		"dynamic_height", renderedCard.Height)

	return renderedCard, nil
}

// CardContentAnalysis 卡片内容分析结果
type CardContentAnalysis struct {
	ElementsCount   int `json:"elements_count"`
	ImagesCount     int `json:"images_count"`
	TextLength      int `json:"text_length"`
	EstimatedHeight int `json:"estimated_height"`
}

// analyzeContent 分析内容量
func (c *OptimizedCardCoordinator) analyzeContent(cards []*model.CardM) (totalImages int, totalTextLength int) {
	for _, card := range cards {
		analysis := c.analyzeSingleCard(card)
		totalImages += analysis.ImagesCount
		totalTextLength += analysis.TextLength
	}
	return
}

// analyzeSingleCard 分析单个卡片内容
func (c *OptimizedCardCoordinator) analyzeSingleCard(card *model.CardM) *CardContentAnalysis {
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return &CardContentAnalysis{
			ElementsCount:   0,
			ImagesCount:     0,
			TextLength:      len(card.ProcessedText),
			EstimatedHeight: c.config.Card.Height,
		}
	}

	analysis := &CardContentAnalysis{
		ElementsCount: len(elements),
	}

	for _, element := range elements {
		if element.Type == "image" {
			analysis.ImagesCount++
		} else {
			analysis.TextLength += len(fmt.Sprintf("%v", element.Content))
		}
	}

	// 使用动态分页引擎估算高度
	engine := pagination.NewDynamicPaginationEngine(c.config)
	analysis.EstimatedHeight = engine.GetOptimizedCardHeight(elements)

	return analysis
}

// optimizeCoverImage 优化封面图
func (c *OptimizedCardCoordinator) optimizeCoverImage(ctx context.Context, coverImageURL string) (*CoverImageInfo, error) {
	// 验证封面图URL
	if err := c.coverOptimizer.ValidateCoverImage(coverImageURL); err != nil {
		return nil, fmt.Errorf("封面图验证失败: %v", err)
	}

	// 优化封面图
	coverInfo, err := c.coverOptimizer.OptimizeCoverImage(coverImageURL)
	if err != nil {
		return nil, fmt.Errorf("封面图优化失败: %v", err)
	}

	log.C(ctx).Infow("封面图优化完成",
		"original_url", coverInfo.URL,
		"optimized_url", coverInfo.OptimizedURL,
		"optimized_size", coverInfo.OptimizedSize,
		"aspect_ratio", coverInfo.AspectRatio)

	return coverInfo, nil
}

// validateRenderedCards 验证渲染结果
func (c *OptimizedCardCoordinator) validateRenderedCards(renderedCards []*RenderedCard) error {
	if len(renderedCards) == 0 {
		return fmt.Errorf("渲染结果为空")
	}

	var errors []string

	for i, card := range renderedCards {
		// 验证基本字段
		if card.CardID == 0 {
			errors = append(errors, fmt.Sprintf("卡片%d: CardID不能为0", i+1))
		}

		if card.ImageURL == "" {
			errors = append(errors, fmt.Sprintf("卡片%d: ImageURL不能为空", i+1))
		}

		if card.Width != c.config.Card.Width {
			errors = append(errors, fmt.Sprintf("卡片%d: 宽度异常 (%d != %d)", i+1, card.Width, c.config.Card.Width))
		}

		// 验证高度是否在合理范围内
		if card.Height < c.config.MinHeight || card.Height > c.config.MaxHeight {
			errors = append(errors, fmt.Sprintf("卡片%d: 高度超出范围 (%d not in [%d, %d])",
				i+1, card.Height, c.config.MinHeight, c.config.MaxHeight))
		}

		// 验证高度是否是1440的整数倍
		if card.Height%1440 != 0 {
			errors = append(errors, fmt.Sprintf("卡片%d: 高度不是1440的整数倍 (%d)", i+1, card.Height))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("渲染结果验证失败: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetOptimizationSummary 获取优化摘要信息
func (c *OptimizedCardCoordinator) GetOptimizationSummary() map[string]interface{} {
	return map[string]interface{}{
		"renderer_type":               "enhanced_dynamic",
		"max_images_per_card":         c.config.MaxImagesPerCard,
		"max_text_length":             c.config.MaxTextLength,
		"min_height":                  c.config.MinHeight,
		"max_height":                  c.config.MaxHeight,
		"min_bottom_padding":          c.config.MinBottomPadding,
		"target_width":                c.config.Card.Width,
		"base_height":                 c.config.Card.Height,
		"supports_cover_optimization": true,
		"supports_dynamic_height":     true,
		"supports_large_content":      true,
	}
}

// IsContentSupported 检查内容是否在支持范围内
func (c *OptimizedCardCoordinator) IsContentSupported(imagesCount int, textLength int) bool {
	return imagesCount <= c.config.MaxImagesPerCard && textLength <= c.config.MaxTextLength
}

// GetContentLimits 获取内容限制信息
func (c *OptimizedCardCoordinator) GetContentLimits() map[string]int {
	return map[string]int{
		"max_images_per_card": c.config.MaxImagesPerCard,
		"max_text_length":     c.config.MaxTextLength,
		"min_height":          c.config.MinHeight,
		"max_height":          c.config.MaxHeight,
		"target_width":        c.config.Card.Width,
		"base_height":         c.config.Card.Height,
	}
}
