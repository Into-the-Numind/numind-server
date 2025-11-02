package book

import (
	"strconv"
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// UpdateBookRequest 更新卡册的请求结构
type UpdateBookRequest struct {
	Title string `json:"title" binding:"required,min=1,max=255"`
	Text  string `json:"text"` // 用户输入的文字内容，用于更新processed_text
}

// Update 更新卡册信息
// 支持更新title和text字段，text字段用于更新processed_text
func (ctrl *BookController) Update(c *gin.Context) {
	log.C(c).Infow("Update book function called")

	// 获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取book ID
	bookIDStr := c.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid book ID"), nil)
		return
	}

	// 绑定请求参数
	var req UpdateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid request parameters"), nil)
		return
	}

	// 获取现有的book信息
	book, err := ctrl.b.Books().GetByID(c, uint(bookID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 验证用户权限：只能更新自己的book
	if book.UserID != currentUser.ID {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("You can only update your own books"), nil)
		return
	}

	// 更新title字段
	book.Title = req.Title

	// 更新text字段（如果提供了）
	if req.Text != "" {
		book.ProcessedText = req.Text
		log.C(c).Infow("Updated processed_text", "book_id", bookID, "text_length", len(req.Text))
	}

	// 更新ViewTime字段为当前时间
	now := time.Now()
	book.ViewTime = &now

	// 保存更新
	if err := ctrl.b.Books().Update(c, book); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
