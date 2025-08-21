package markdown

import (
	"context"
	"fmt"

	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/model"
)

// MarkdownIntegrationAdapter Markdown 集成适配器
// 该适配器用于将 Markdown 处理流程集成到现有的系统中，替代 JSON 处理流程
type MarkdownIntegrationAdapter struct {
	asyncProcessor *MarkdownAsyncProcessor
	bizInterface   biz.IBiz
}

// NewMarkdownIntegrationAdapter 创建新的 Markdown 集成适配器
func NewMarkdownIntegrationAdapter(bizInterface biz.IBiz) *MarkdownIntegrationAdapter {
	// 从 bizInterface 获取所需的业务接口
	aliBiz := bizInterface.Ali()
	volcBiz := bizInterface.Volc()

	// 创建适配器包装类来实现接口兼容性
	bookBiz := &BookBizAdapter{biz: bizInterface}
	cardBiz := &CardBizAdapter{biz: bizInterface}

	// 创建异步处理器
	asyncProcessor := NewMarkdownAsyncProcessor(aliBiz, volcBiz, bookBiz, cardBiz)

	return &MarkdownIntegrationAdapter{
		asyncProcessor: asyncProcessor,
		bizInterface:   bizInterface,
	}
}

// CreateBookAsync 异步创建书籍（替代原有的 CreateBookAsync 方法）
func (mia *MarkdownIntegrationAdapter) CreateBookAsync(
	ctx context.Context,
	userID uint,
	inputText string,
	templateID string,
) (*model.BookM, error) {
	// 使用 Markdown 异步处理器处理
	result, err := mia.asyncProcessor.ProcessBookAsync(ctx, userID, inputText, templateID)
	if err != nil {
		return nil, fmt.Errorf("markdown 处理失败: %v", err)
	}

	// 获取创建的书籍记录
	book, err := mia.bizInterface.Books().GetByID(ctx, result.BookID)
	if err != nil {
		return nil, fmt.Errorf("获取书籍记录失败: %v", err)
	}

	return book, nil
}

// BookBizAdapter 书籍业务适配器
type BookBizAdapter struct {
	biz biz.IBiz
}

func (ba *BookBizAdapter) Create(ctx context.Context, book *model.BookM) error {
	return ba.biz.Books().Create(ctx, book)
}

func (ba *BookBizAdapter) Update(ctx context.Context, book *model.BookM) error {
	return ba.biz.Books().Update(ctx, book)
}

func (ba *BookBizAdapter) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	return ba.biz.Books().GetByID(ctx, id)
}

func (ba *BookBizAdapter) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	return ba.biz.Books().UpdateUserBookStatsOnStatusChange(ctx, userID, oldStatus, newStatus)
}

// CardBizAdapter 卡片业务适配器
type CardBizAdapter struct {
	biz biz.IBiz
}

func (ca *CardBizAdapter) Create(ctx context.Context, card *model.CardM) error {
	return ca.biz.Cards().Create(ctx, card)
}

func (ca *CardBizAdapter) Update(ctx context.Context, card *model.CardM) error {
	return ca.biz.Cards().Update(ctx, card)
}

// GetProcessor 获取底层的 Markdown 异步处理器
func (mia *MarkdownIntegrationAdapter) GetProcessor() *MarkdownAsyncProcessor {
	return mia.asyncProcessor
}

// GetStats 获取处理统计信息
func (mia *MarkdownIntegrationAdapter) GetStats(ctx context.Context, bookID uint) (map[string]interface{}, error) {
	// 获取书籍信息
	book, err := mia.bizInterface.Books().GetByID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("获取书籍失败: %v", err)
	}

	// 获取卡片信息
	total, cards, err := mia.bizInterface.Cards().ListByBook(ctx, bookID, 0, 1000)
	if err != nil {
		return nil, fmt.Errorf("获取卡片失败: %v", err)
	}

	stats := map[string]interface{}{
		"book_id":         bookID,
		"title":           book.Title,
		"status":          book.Status,
		"total_cards":     total,
		"card_count":      len(cards),
		"created_at":      book.CreatedAt,
		"updated_at":      book.UpdatedAt,
		"processing_mode": "markdown", // 标识使用 Markdown 处理模式
	}

	// 如果有封面图片，添加到统计信息中
	if book.ImageUrl != "" {
		stats["cover_image"] = book.ImageUrl
	}

	return stats, nil
}

// ValidateMarkdown 验证 Markdown 内容格式
func (mia *MarkdownIntegrationAdapter) ValidateMarkdown(markdownText string) (bool, []string) {
	return mia.asyncProcessor.promptManager.ValidateMarkdownFormat(markdownText)
}

// PreviewMarkdown 预览 Markdown 内容（用于调试和测试）
func (mia *MarkdownIntegrationAdapter) PreviewMarkdown(
	ctx context.Context,
	markdownText string,
) (*MarkdownContent, error) {
	return mia.asyncProcessor.processor.ParseMarkdown(markdownText)
}

// ConvertMarkdownToHTML 将 Markdown 转换为 HTML（用于预览）
func (mia *MarkdownIntegrationAdapter) ConvertMarkdownToHTML(
	markdownText string,
	title string,
) (string, error) {
	return mia.asyncProcessor.htmlConverter.ConvertToStyledHTML(markdownText, title)
}

// TestAIGeneration 测试 AI 生成 Markdown（用于调试）
func (mia *MarkdownIntegrationAdapter) TestAIGeneration(
	ctx context.Context,
	inputText string,
) (string, error) {
	return mia.asyncProcessor.generateMarkdownContent(ctx, inputText)
}

// CleanupBookImages 清理书籍相关的图片文件
func (mia *MarkdownIntegrationAdapter) CleanupBookImages(bookID uint) error {
	return mia.asyncProcessor.renderer.CleanupOldImages(bookID)
}

// UpdateProcessorConfig 更新处理器配置
func (mia *MarkdownIntegrationAdapter) UpdateProcessorConfig(config *ProcessorConfig) {
	mia.asyncProcessor.UpdateConfig(config)
}

// UpdateRendererConfig 更新渲染器配置
func (mia *MarkdownIntegrationAdapter) UpdateRendererConfig(config *RendererConfig) {
	mia.asyncProcessor.renderer.UpdateConfig(config)
}

// UpdatePaginationConfig 更新分页配置
func (mia *MarkdownIntegrationAdapter) UpdatePaginationConfig(config *MarkdownPaginationConfig) {
	mia.asyncProcessor.paginationAdapter.UpdateConfig(config)
}

// UpdateHTMLConfig 更新 HTML 转换配置
func (mia *MarkdownIntegrationAdapter) UpdateHTMLConfig(config *HTMLConfig) {
	mia.asyncProcessor.htmlConverter.UpdateConfig(config)
}

// GetAllConfigs 获取所有配置信息
func (mia *MarkdownIntegrationAdapter) GetAllConfigs() map[string]interface{} {
	return map[string]interface{}{
		"processor_config":  mia.asyncProcessor.GetConfig(),
		"renderer_config":   mia.asyncProcessor.renderer.GetConfig(),
		"pagination_config": mia.asyncProcessor.paginationAdapter.GetConfig(),
		"html_config":       mia.asyncProcessor.htmlConverter.GetConfig(),
	}
}
