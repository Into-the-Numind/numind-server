package book

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// LightweightRendererIntegration 轻量级渲染器集成器
// 专门用于将轻量级渲染器集成到book创建流程中
type LightweightRendererIntegration struct {
	biz      BizInterface
	renderer *card.BrowserFreeRenderer
	config   *pagination.PaginationConfig
}

// NewLightweightRendererIntegration 创建轻量级渲染器集成器
func NewLightweightRendererIntegration(biz BizInterface, config *pagination.PaginationConfig) (*LightweightRendererIntegration, error) {
	// 创建轻量级渲染器
	renderer, err := card.NewBrowserFreeRenderer()
	if err != nil {
		return nil, fmt.Errorf("创建轻量级渲染器失败: %v", err)
	}

	return &LightweightRendererIntegration{
		biz:      biz,
		renderer: renderer,
		config:   config,
	}, nil
}

// ProcessBookWithLightweightRendering 使用轻量级渲染器处理整本书
func (l *LightweightRendererIntegration) ProcessBookWithLightweightRendering(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	elements []pagination.Element,
	coverImageURL string,
) error {
	log.C(ctx).Infow("🚀 开始轻量级渲染器处理", "book_id", book.ID, "elements_count", len(elements))

	start := time.Now()
	defer func() {
		log.C(ctx).Infow("⏱️  轻量级渲染器处理完成", "book_id", book.ID, "duration", time.Since(start))
	}()

	// 1. 验证环境
	if err := l.renderer.ValidateConfiguration(); err != nil {
		return fmt.Errorf("轻量级渲染器环境验证失败: %v", err)
	}

	// 2. 分页处理 - 使用按行分页引擎
	paginationBiz := pagination.NewPaginationBiz()
	paginatedContent, err := paginationBiz.PaginateElementsByLines(elements)
	if err != nil {
		return fmt.Errorf("按行分页处理失败: %v", err)
	}

	log.C(ctx).Infow("📄 分页完成", "book_id", book.ID, "pages", len(paginatedContent.Cards))

	// 3. 为每个分页后的卡片创建数据库记录
	var allCards []*model.CardM
	for i, cardContent := range paginatedContent.Cards {
		// 将卡片内容转换为JSON格式
		var cardElements []map[string]interface{}
		for _, element := range cardContent.Elements {
			cardElements = append(cardElements, map[string]interface{}{
				"type":    element.Type,
				"content": element.Content,
			})
		}

		// 将JSON数据转换为字符串
		cardJSONStr, err := json.Marshal(cardElements)
		if err != nil {
			log.C(ctx).Errorw("卡片JSON序列化失败", "book_id", book.ID, "card_index", i, "error", err.Error())
			continue
		}

		// 创建卡片记录
		cardRecord := &model.CardM{
			UserID:        userID,
			BookID:        book.ID,
			ProcessedText: string(cardJSONStr),
			SortOrder:     i + 1, // 从1开始计数，0是封面卡片
		}

		if err := l.biz.Cards().Create(ctx, cardRecord); err != nil {
			log.C(ctx).Errorw("创建卡片记录失败", "book_id", book.ID, "card_index", i, "error", err.Error())
			continue
		}

		allCards = append(allCards, cardRecord)
		log.C(ctx).Infow("✅ 卡片记录创建成功", "book_id", book.ID, "card_id", cardRecord.ID, "sort_order", cardRecord.SortOrder)
	}

	if len(allCards) == 0 {
		return fmt.Errorf("没有成功创建任何卡片记录")
	}

	// 4. 使用轻量级渲染器批量渲染
	log.C(ctx).Infow("🎨 开始轻量级批量渲染", "book_id", book.ID, "card_count", len(allCards))

	renderedCards, err := l.renderer.RenderBookToImages(ctx, book, allCards)
	if err != nil {
		return fmt.Errorf("轻量级渲染失败: %v", err)
	}

	log.C(ctx).Infow("✅ 轻量级渲染成功", "book_id", book.ID, "rendered_count", len(renderedCards))

	// 5. 更新卡片记录的图片URL
	for i, renderedCard := range renderedCards {
		if i < len(allCards) {
			allCards[i].RenderedImage = renderedCard.ImageURL
			if err := l.biz.Cards().Update(ctx, allCards[i]); err != nil {
				log.C(ctx).Errorw("更新卡片图片URL失败", "card_id", allCards[i].ID, "error", err.Error())
			} else {
				log.C(ctx).Infow("🖼️  卡片图片URL更新成功", "card_id", allCards[i].ID, "image_url", renderedCard.ImageURL)
			}
		}
	}

	// 6. 更新书籍统计
	book.CardCount = len(allCards)
	if err := l.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新书籍卡片数量失败", "book_id", book.ID, "error", err.Error())
	}

	return nil
}

// CreateCoverCardWithLightweight 使用轻量级渲染器创建封面卡片
func (l *LightweightRendererIntegration) CreateCoverCardWithLightweight(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	coverImageURL string,
) (*model.CardM, error) {
	log.C(ctx).Infow("📚 开始创建轻量级封面卡片", "book_id", book.ID)

	// 创建封面卡片记录
	coverCard := &model.CardM{
		UserID:    userID,
		BookID:    book.ID,
		SortOrder: 0, // 封面卡片排序为0
	}

	// 设置封面卡片内容（标题）
	coverElements := []map[string]interface{}{
		{
			"type":    "title",
			"content": book.Title,
		},
	}

	if coverImageURL != "" {
		// 如果有封面图片，添加图片元素
		coverElements = append([]map[string]interface{}{
			{
				"type":    "image",
				"content": coverImageURL,
			},
		}, coverElements...)
	}

	// 将封面内容转换为JSON
	coverJSONStr, err := json.Marshal(coverElements)
	if err != nil {
		return nil, fmt.Errorf("封面卡片JSON序列化失败: %v", err)
	}

	coverCard.ProcessedText = string(coverJSONStr)

	// 创建封面卡片记录
	if err := l.biz.Cards().Create(ctx, coverCard); err != nil {
		return nil, fmt.Errorf("创建封面卡片记录失败: %v", err)
	}

	// 使用轻量级渲染器渲染封面
	renderedCover, err := l.renderer.RenderSingleCard(ctx, coverCard)
	if err != nil {
		log.C(ctx).Errorw("轻量级封面渲染失败", "book_id", book.ID, "error", err.Error())
		// 封面渲染失败不影响主流程，但记录错误
		return coverCard, nil
	}

	// 更新封面卡片的图片URL
	coverCard.RenderedImage = renderedCover.ImageURL
	if err := l.biz.Cards().Update(ctx, coverCard); err != nil {
		log.C(ctx).Errorw("更新封面卡片图片URL失败", "card_id", coverCard.ID, "error", err.Error())
	} else {
		log.C(ctx).Infow("📚 轻量级封面卡片创建成功", "book_id", book.ID, "cover_url", renderedCover.ImageURL)
	}

	return coverCard, nil
}

// GetStats 获取轻量级渲染器统计信息
func (l *LightweightRendererIntegration) GetStats() map[string]interface{} {
	if l.renderer != nil {
		return l.renderer.GetStats()
	}
	return map[string]interface{}{
		"error": "renderer not initialized",
	}
}

// Cleanup 清理资源
func (l *LightweightRendererIntegration) Cleanup() error {
	if l.renderer != nil {
		return l.renderer.Cleanup()
	}
	return nil
}
