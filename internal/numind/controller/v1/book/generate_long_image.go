package book

import (
	"strconv"

	"github.com/gin-gonic/gin"

	bookbiz "numind-server/internal/numind/biz/book"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// GenerateLongImageRequest 生成长图的请求结构
type GenerateLongImageRequest struct {
	TemplateID string `json:"template_id,omitempty"` // 可选：模板ID
}

// GenerateLongImage 生成长图
func (ctrl *BookController) GenerateLongImage(c *gin.Context) {
	log.C(c).Infow("Generate long image function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid book ID"), nil)
		return
	}

	var req GenerateLongImageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 获取book记录
	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("Book not found"), nil)
		return
	}

	// 检查ProcessedText是否为空
	if book.ProcessedText == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Book has no processed text"), nil)
		return
	}

	// 创建适配器来包装biz接口
	bizAdapter := &BookBizAdapter{biz: ctrl.b}

	// 创建异步处理器
	asyncProcessor := bookbiz.NewAsyncBookProcessor(bizAdapter)

	// 异步生成长图
	card, err := asyncProcessor.GenerateLongImageAsync(c, uint(bookID), book.ProcessedText, req.TemplateID)
	if err != nil {
		log.C(c).Errorw("Failed to generate long image", "book_id", bookID, "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to generate long image: "+err.Error()), nil)
		return
	}

	log.C(c).Infow("Long image generation started", "book_id", bookID, "card_id", card.ID)
	core.WriteResponse(c, nil, gin.H{
		"message": "Long image generation started",
		"book_id": bookID,
		"card_id": card.ID,
	})
}
