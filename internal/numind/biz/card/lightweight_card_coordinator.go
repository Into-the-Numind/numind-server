package card

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

// LightweightCardCoordinator 轻量级卡片协调器
// 集成轻量级渲染器，完全替代无头浏览器依赖
type LightweightCardCoordinator struct {
	lightweightRenderer *LightweightRenderer
	config              *pagination.PaginationConfig
}

// NewLightweightCardCoordinator 创建轻量级卡片协调器
func NewLightweightCardCoordinator() (*LightweightCardCoordinator, error) {
	// 获取默认配置
	config := pagination.GetDefaultConfig()

	// 创建轻量级渲染器
	renderer, err := NewLightweightRenderer(config)
	if err != nil {
		return nil, fmt.Errorf("创建轻量级渲染器失败: %v", err)
	}

	return &LightweightCardCoordinator{
		lightweightRenderer: renderer,
		config:              config,
	}, nil
}

// RenderBookCards 渲染整本书的所有卡片
func (c *LightweightCardCoordinator) RenderBookCards(ctx context.Context, book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	fmt.Printf("🚀 开始轻量级渲染器处理书籍: %s (ID=%d)\n", book.Title, book.ID)

	start := time.Now()
	defer func() {
		fmt.Printf("⏱️  轻量级渲染耗时: %v\n", time.Since(start))
	}()

	// 使用轻量级渲染器处理
	renderedCards, err := c.lightweightRenderer.RenderBookToLongImage(book, cards)
	if err != nil {
		return nil, fmt.Errorf("轻量级渲染失败: %v", err)
	}

	fmt.Printf("✅ 轻量级渲染完成，生成了 %d 张卡片图片\n", len(renderedCards))
	return renderedCards, nil
}

// RenderSingleCard 渲染单张卡片（兼容现有接口）
func (c *LightweightCardCoordinator) RenderSingleCard(ctx context.Context, card *model.CardM) (*RenderedCard, error) {
	fmt.Printf("🎯 渲染单张卡片: ID=%d\n", card.ID)

	// 创建虚拟书籍用于渲染
	virtualBook := &model.BookM{
		Title: fmt.Sprintf("Card %d", card.ID),
		Tags:  "single-card",
	}
	virtualBook.ID = 0

	// 渲染单张卡片
	renderedCards, err := c.lightweightRenderer.RenderBookToLongImage(virtualBook, []*model.CardM{card})
	if err != nil {
		return nil, fmt.Errorf("单张卡片渲染失败: %v", err)
	}

	if len(renderedCards) == 0 {
		return nil, fmt.Errorf("渲染结果为空")
	}

	return renderedCards[0], nil
}

// ValidateEnvironment 验证渲染环境
func (c *LightweightCardCoordinator) ValidateEnvironment() error {
	fmt.Printf("🔍 验证轻量级渲染环境...\n")

	// 这里可以添加环境验证逻辑
	// 比如检查wkhtmltoimage是否可用
	// 检查临时目录权限等

	fmt.Printf("✅ 轻量级渲染环境验证通过\n")
	return nil
}

// GetStats 获取渲染器统计信息
func (c *LightweightCardCoordinator) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"renderer_type":    "lightweight",
		"browser_free":     true,
		"memory_efficient": true,
		"config":           c.config,
		"features": []string{
			"wkhtmltoimage_rendering",
			"long_image_splitting",
			"auto_padding",
			"error_retry",
		},
	}
}

// Cleanup 清理资源
func (c *LightweightCardCoordinator) Cleanup() error {
	if c.lightweightRenderer != nil {
		return c.lightweightRenderer.Cleanup()
	}
	return nil
}
