package card

import (
	"strconv"

	cardRenderer "numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

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

// RenderCard 渲染单个卡片
func (ctrl *CardController) RenderCard(c *gin.Context) {
	log.C(c).Infow("Render card function called")

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

	// 创建无头浏览器渲染器
	renderer := cardRenderer.NewSimpleHeadlessRenderer(pagination.GetDefaultConfig())

	// 渲染卡片
	renderedCard, err := renderer.RenderCardToImage(card)
	if err != nil {
		log.C(c).Errorw("Failed to render card", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to render card: "+err.Error()), nil)
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
		Height:    renderedCard.Height,
		SortOrder: renderedCard.SortOrder,
	}

	core.WriteResponse(c, nil, response)
}

// RenderBookCards 渲染书籍的所有卡片
func (ctrl *CardController) RenderBookCards(c *gin.Context) {
	log.C(c).Infow("Render book cards function called")

	bookIDStr := c.Param("book_id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 获取书籍的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 创建无头浏览器渲染器
	renderer := cardRenderer.NewSimpleHeadlessRenderer(pagination.GetDefaultConfig())

	var renderedCards []*RenderCardResponse

	// 渲染每个卡片
	for _, card := range cards {
		renderedCard, err := renderer.RenderCardToImage(card)
		if err != nil {
			log.C(c).Errorw("Failed to render card", "error", err.Error(), "card_id", card.ID)
			continue // 跳过失败的卡片
		}

		// 更新卡片记录
		card.RenderedImage = renderedCard.ImageURL
		if err := ctrl.b.Cards().Update(c, card); err != nil {
			log.C(c).Errorw("Failed to update card with rendered image", "error", err.Error(), "card_id", card.ID)
			continue
		}

		renderedCards = append(renderedCards, &RenderCardResponse{
			CardID:    renderedCard.CardID,
			ImageURL:  renderedCard.ImageURL,
			Width:     renderedCard.Width,
			Height:    renderedCard.Height,
			SortOrder: renderedCard.SortOrder,
		})
	}

	core.WriteResponse(c, nil, renderedCards)
}
