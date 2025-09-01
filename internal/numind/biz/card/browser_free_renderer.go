package card

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// BrowserFreeRenderer 无浏览器依赖的卡片渲染器
// 这是主要的集成类，完全替代chromedp依赖的渲染系统
type BrowserFreeRenderer struct {
	lightweightRenderer *LightweightRenderer
	templateEngine      *OptimizedHTMLTemplate
	errorHandler        *ErrorHandler
	config              *pagination.PaginationConfig
}

// NewBrowserFreeRenderer 创建无浏览器渲染器
func NewBrowserFreeRenderer() (*BrowserFreeRenderer, error) {
	// 获取配置
	config := pagination.GetDefaultConfig()

	// 创建轻量级渲染器
	lightweightRenderer, err := NewLightweightRenderer(config)
	if err != nil {
		return nil, fmt.Errorf("创建轻量级渲染器失败: %v", err)
	}

	// 创建模板引擎
	templateEngine := NewOptimizedHTMLTemplate(config)

	// 创建错误处理器
	errorHandler := NewErrorHandler()

	renderer := &BrowserFreeRenderer{
		lightweightRenderer: lightweightRenderer,
		templateEngine:      templateEngine,
		errorHandler:        errorHandler,
		config:              config,
	}

	return renderer, nil
}

// RenderBookToImages 将整本书渲染为图片列表（主要接口）
func (r *BrowserFreeRenderer) RenderBookToImages(ctx context.Context, book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	log.C(ctx).Infow("开始无浏览器渲染", "bookID", book.ID, "title", book.Title, "cardCount", len(cards))

	start := time.Now()
	defer func() {
		log.C(ctx).Infow("无浏览器渲染完成", "duration", time.Since(start))
	}()

	// 验证环境
	if err := r.errorHandler.ValidateRenderEnvironment(ctx); err != nil {
		log.C(ctx).Errorw("渲染环境验证失败", "error", err)
		return nil, err
	}

	// 预处理卡片数据
	processedCards, err := r.preprocessCards(ctx, cards)
	if err != nil {
		return nil, fmt.Errorf("预处理卡片失败: %v", err)
	}

	// 使用错误处理器进行重试渲染
	var renderedCards []*RenderedCard
	err = r.errorHandler.RetryWithBackoff(ctx, func() error {
		var renderErr error
		renderedCards, renderErr = r.performRendering(ctx, book, processedCards)
		return renderErr
	}, r.errorHandler.maxRetries)

	if err != nil {
		log.C(ctx).Errorw("渲染失败，尝试降级处理", "error", err)
		return r.handleRenderFailure(ctx, book, cards, err)
	}

	log.C(ctx).Infow("渲染成功", "resultCount", len(renderedCards))
	return renderedCards, nil
}

// performRendering 执行实际渲染
func (r *BrowserFreeRenderer) performRendering(ctx context.Context, book *model.BookM, cards []*EnhancedCardData) ([]*RenderedCard, error) {
	// 1. 生成优化的HTML
	fullHTML, err := r.generateOptimizedHTML(book, cards)
	if err != nil {
		return nil, fmt.Errorf("生成HTML失败: %v", err)
	}

	// 2. 使用wkhtmltoimage渲染为超长图
	longImageData, err := r.lightweightRenderer.renderLongImage(fullHTML)
	if err != nil {
		return nil, fmt.Errorf("渲染超长图失败: %v", err)
	}

	// 3. 切分超长图
	splitImages, err := r.lightweightRenderer.splitLongImage(longImageData)
	if err != nil {
		return nil, fmt.Errorf("切分图片失败: %v", err)
	}

	// 4. 保存图片并生成结果
	renderedCards := make([]*RenderedCard, len(splitImages))
	for i, imageData := range splitImages {
		var cardID uint
		var sortOrder int
		if i < len(cards) {
			cardID = cards[i].OriginalCard.ID
			sortOrder = cards[i].OriginalCard.SortOrder
		} else {
			cardID = 0 // 额外的切分图片
			sortOrder = i + 1
		}

		imageURL, err := r.lightweightRenderer.saveImage(imageData, cardID)
		if err != nil {
			return nil, fmt.Errorf("保存图片失败: %v", err)
		}

		renderedCards[i] = &RenderedCard{
			CardID:    cardID,
			ImageURL:  imageURL,
			Width:     r.config.Card.Width,
			Height:    r.config.Card.Height,
			SortOrder: sortOrder,
		}
	}

	return renderedCards, nil
}

// EnhancedCardData 增强的卡片数据
type EnhancedCardData struct {
	OriginalCard *model.CardM         `json:"original_card"`
	Elements     []pagination.Element `json:"elements"`
}

// preprocessCards 预处理卡片数据
func (r *BrowserFreeRenderer) preprocessCards(ctx context.Context, cards []*model.CardM) ([]*EnhancedCardData, error) {
	processedCards := make([]*EnhancedCardData, len(cards))

	for i, card := range cards {
		// 解析卡片的ProcessedText
		var elements []pagination.Element
		if card.ProcessedText != "" {
			if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
				log.C(ctx).Warnw("解析卡片数据失败，使用原始文本", "cardID", card.ID, "error", err)
				// 如果解析失败，将原始文本作为body元素
				elements = []pagination.Element{
					{
						Type:    pagination.ElementTypeBody,
						Content: card.ProcessedText,
					},
				}
			}
		} else {
			// 空内容处理
			elements = []pagination.Element{
				{
					Type:    pagination.ElementTypeBody,
					Content: "内容为空",
				},
			}
		}

		processedCards[i] = &EnhancedCardData{
			OriginalCard: card,
			Elements:     elements,
		}
	}

	log.C(ctx).Infow("卡片预处理完成", "count", len(processedCards))
	return processedCards, nil
}

// generateOptimizedHTML 生成优化的HTML
func (r *BrowserFreeRenderer) generateOptimizedHTML(book *model.BookM, cards []*EnhancedCardData) (string, error) {
	// 将增强的卡片数据转换为模板需要的格式
	templateCards := make([]*model.CardM, len(cards))
	for i, enhancedCard := range cards {
		// 重新编码元素为JSON字符串
		elementsJSON, err := json.Marshal(enhancedCard.Elements)
		if err != nil {
			return "", fmt.Errorf("编码卡片元素失败: %v", err)
		}

		// 创建模板用的卡片副本
		templateCard := *enhancedCard.OriginalCard
		templateCard.ProcessedText = string(elementsJSON)
		templateCards[i] = &templateCard
	}

	return r.templateEngine.GenerateFullBookHTML(book, templateCards)
}

// handleRenderFailure 处理渲染失败
func (r *BrowserFreeRenderer) handleRenderFailure(ctx context.Context, book *model.BookM, cards []*model.CardM, originalErr error) ([]*RenderedCard, error) {
	log.C(ctx).Errorw("尝试创建降级图片", "error", originalErr)

	// 为每张卡片创建降级图片
	renderedCards := make([]*RenderedCard, len(cards))
	for i, card := range cards {
		fallbackImageData, err := r.errorHandler.CreateFallbackImage(ctx, card, originalErr.Error())
		if err != nil {
			log.C(ctx).Errorw("创建降级图片失败", "cardID", card.ID, "error", err)
			// 如果降级也失败，返回空结果
			return nil, fmt.Errorf("渲染和降级都失败: 原始错误=%v, 降级错误=%v", originalErr, err)
		}

		imageURL, err := r.lightweightRenderer.saveImage(fallbackImageData, card.ID)
		if err != nil {
			return nil, fmt.Errorf("保存降级图片失败: %v", err)
		}

		renderedCards[i] = &RenderedCard{
			CardID:    card.ID,
			ImageURL:  imageURL,
			Width:     r.config.Card.Width,
			Height:    r.config.Card.Height,
			SortOrder: card.SortOrder,
		}
	}

	log.C(ctx).Warnw("使用降级图片完成渲染", "count", len(renderedCards))
	return renderedCards, nil
}

// RenderSingleCard 渲染单张卡片（兼容接口）
func (r *BrowserFreeRenderer) RenderSingleCard(ctx context.Context, card *model.CardM) (*RenderedCard, error) {
	// 创建虚拟书籍
	virtualBook := &model.BookM{
		Title: fmt.Sprintf("Card %d", card.ID),
		Tags:  "single-card",
	}
	virtualBook.ID = 0

	// 渲染
	results, err := r.RenderBookToImages(ctx, virtualBook, []*model.CardM{card})
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("渲染结果为空")
	}

	return results[0], nil
}

// GetCapabilities 获取渲染器能力信息
func (r *BrowserFreeRenderer) GetCapabilities() map[string]interface{} {
	return map[string]interface{}{
		"name":              "BrowserFreeRenderer",
		"version":           "1.0.0",
		"browser_free":      true,
		"dependencies":      []string{"wkhtmltoimage", "imaging"},
		"supported_formats": []string{"png"},
		"features": []string{
			"html_to_image_conversion",
			"long_image_splitting",
			"automatic_padding",
			"error_recovery",
			"retry_mechanism",
			"fallback_generation",
		},
		"config": r.config,
	}
}

// ValidateConfiguration 验证配置
func (r *BrowserFreeRenderer) ValidateConfiguration() error {
	if r.config == nil {
		return fmt.Errorf("配置为空")
	}

	if r.config.Card.Width <= 0 || r.config.Card.Height <= 0 {
		return fmt.Errorf("卡片尺寸无效: %dx%d", r.config.Card.Width, r.config.Card.Height)
	}

	// 验证模板
	if err := r.templateEngine.ValidateHTMLTemplate(); err != nil {
		return fmt.Errorf("HTML模板验证失败: %v", err)
	}

	return nil
}

// Cleanup 清理资源
func (r *BrowserFreeRenderer) Cleanup() error {
	if r.lightweightRenderer != nil {
		return r.lightweightRenderer.Cleanup()
	}
	return nil
}

// GetStats 获取统计信息
func (r *BrowserFreeRenderer) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"renderer_type":  "browser_free",
		"implementation": "wkhtmltoimage_based",
		"performance": map[string]interface{}{
			"memory_efficient": true,
			"cpu_efficient":    true,
			"io_efficient":     true,
		},
		"reliability": map[string]interface{}{
			"error_handling":   true,
			"retry_mechanism":  true,
			"fallback_support": true,
		},
		"configuration": r.config,
	}
}

// generateSystemReport 生成系统报告
func (r *BrowserFreeRenderer) GenerateSystemReport(ctx context.Context) (map[string]interface{}, error) {
	report := map[string]interface{}{
		"timestamp": time.Now(),
		"renderer":  r.GetCapabilities(),
		"stats":     r.GetStats(),
	}

	// 验证环境
	envErr := r.errorHandler.ValidateRenderEnvironment(ctx)
	report["environment_valid"] = envErr == nil
	if envErr != nil {
		report["environment_error"] = envErr.Error()
	}

	// 验证配置
	configErr := r.ValidateConfiguration()
	report["configuration_valid"] = configErr == nil
	if configErr != nil {
		report["configuration_error"] = configErr.Error()
	}

	return report, nil
}
