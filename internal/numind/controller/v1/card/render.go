package card

import (
	"fmt"
	"strconv"

	cardRenderer "numind-server/internal/numind/biz/card"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// RenderCardRequest 渲染卡片请求
type RenderCardRequest struct {
	CardID uint `json:"card_id" binding:"required"`
}

// RenderCardResponse 渲染卡片响应
type RenderCardResponse struct {
	CardID    uint   `json:"card_id"`
	ImageURL  string `json:"image_url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SortOrder int    `json:"sort_order"`
}

// RenderCard 渲染单个卡片（使用优化渲染器）
func (ctrl *CardController) RenderCard(c *gin.Context) {
	log.C(c).Infow("Render card function called with optimization")

	var req RenderCardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 获取卡片信息
	card, err := ctrl.b.Cards().GetByID(c, req.CardID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 创建优化卡片协调器（集成动态高度、大内容支持、封面优化等功能）
	coordinator := cardRenderer.NewOptimizedCardCoordinator()

	// 使用优化渲染器渲染卡片
	renderedCard, err := coordinator.RenderOptimizedCard(c, card)
	if err != nil {
		log.C(c).Errorw("Failed to render card with optimization", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage(fmt.Sprintf("Failed to render card: %v", err)), nil)
		return
	}

	// 更新卡片记录
	card.RenderedImage = renderedCard.ImageURL
	if err := ctrl.b.Cards().Update(c, card); err != nil {
		log.C(c).Errorw("Failed to update card with rendered image", "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	response := &RenderCardResponse{
		CardID:    renderedCard.CardID,
		ImageURL:  renderedCard.ImageURL,
		Width:     renderedCard.Width,
		Height:    renderedCard.Height, // 现在支持动态高度
		SortOrder: renderedCard.SortOrder,
	}

	// 记录优化信息
	log.C(c).Infow("Card rendered with optimization",
		"card_id", renderedCard.CardID,
		"dynamic_height", renderedCard.Height,
		"optimization_summary", coordinator.GetOptimizationSummary())

	core.WriteResponse(c, nil, response)
}

// RenderBookCards 渲染书籍的所有卡片（使用优化渲染器）
func (ctrl *CardController) RenderBookCards(c *gin.Context) {
	log.C(c).Infow("Render book cards function called with optimization")

	bookIDStr := c.Param("book_id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 获取书籍信息
	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 获取书籍的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 创建优化卡片协调器（支持大内容量、动态高度、封面优化）
	coordinator := cardRenderer.NewOptimizedCardCoordinator()

	// 检查内容量
	contentLimits := coordinator.GetContentLimits()
	log.C(c).Infow("Content capacity limits", "limits", contentLimits)

	// 使用优化渲染器批量渲染整本书
	optimizedCards, err := coordinator.RenderOptimizedBook(c, book, cards)
	if err != nil {
		log.C(c).Errorw("Failed to render book with optimization", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage(fmt.Sprintf("Failed to render book: %v", err)), nil)
		return
	}

	// 批量更新卡片记录并构建响应
	var renderedCards []*RenderCardResponse

	for i, optimizedCard := range optimizedCards {
		// 查找对应的原始卡片记录进行更新
		var targetCard *model.CardM
		if i < len(cards) {
			targetCard = cards[i]
		} else {
			// 这是动态分页产生的新卡片，可能需要创建新记录
			continue
		}

		// 更新卡片记录
		targetCard.RenderedImage = optimizedCard.ImageURL
		if err := ctrl.b.Cards().Update(c, targetCard); err != nil {
			log.C(c).Warnw("Failed to update card with rendered image",
				"error", err.Error(),
				"card_id", targetCard.ID)
		}

		renderedCards = append(renderedCards, &RenderCardResponse{
			CardID:    optimizedCard.CardID,
			ImageURL:  optimizedCard.ImageURL,
			Width:     optimizedCard.Width,
			Height:    optimizedCard.Height, // 动态高度
			SortOrder: optimizedCard.SortOrder,
		})
	}

	log.C(c).Infow("Book cards rendered with optimization",
		"book_id", bookID,
		"original_cards", len(cards),
		"optimized_cards", len(optimizedCards),
		"optimization_summary", coordinator.GetOptimizationSummary())

	core.WriteResponse(c, nil, renderedCards)
}
