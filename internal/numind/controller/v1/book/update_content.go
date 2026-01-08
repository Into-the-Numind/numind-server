package book

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// UpdateContentRequest 更新笔记内容的请求结构
type UpdateContentRequest struct {
	ProcessedText string `json:"processed_text" binding:"required"` // 用户编辑后的markdown内容
}

// UpdateContent 更新笔记内容
// 注意：此接口仅更新内容，如需同时更新标题请使用 PUT /v1/books/:id
func (ctrl *BookController) UpdateContent(c *gin.Context) {
	log.C(c).Infow("Update book content function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid book ID"), nil)
		return
	}

	var req UpdateContentRequest
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

	// 更新ProcessedText字段
	book.ProcessedText = req.ProcessedText
	if err := ctrl.b.Books().Update(c, book); err != nil {
		log.C(c).Errorw("Failed to update book content", "book_id", bookID, "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to update book content"), nil)
		return
	}

	log.C(c).Infow("Book content updated successfully", "book_id", bookID)
	core.WriteResponse(c, nil, gin.H{
		"message": "Book content updated successfully",
		"book_id": bookID,
	})
}
