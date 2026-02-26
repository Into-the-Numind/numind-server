package book

import (
	"strconv"

	"github.com/gin-gonic/gin"

	bookbiz "numind-server/internal/numind/biz/book"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// GeneratePaginatedImagesRequest 生成分页图片的请求结构
type GeneratePaginatedImagesRequest struct {
	TemplateID string `json:"template_id,omitempty"` // 可选：模板ID
}

// GeneratePaginatedImages 生成分页图片
func (ctrl *BookController) GeneratePaginatedImages(c *gin.Context) {
	log.C(c).Infow("Generate paginated images function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid book ID"), nil)
		return
	}

	var req GeneratePaginatedImagesRequest
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

	// 异步生成分页图片
	cards, err := asyncProcessor.GeneratePaginatedImagesAsync(c, uint(bookID), book.ProcessedText, req.TemplateID)
	if err != nil {
		log.C(c).Errorw("Failed to generate paginated images", "book_id", bookID, "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to generate paginated images: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Paginated images generation started", "book_id", bookID, "card_count", len(cards))
	core.WriteResponse(c, nil, gin.H{
		"message":    "Paginated images generation started",
		"book_id":    bookID,
		"card_count": len(cards),
		"cards":      cards,
	})
}
