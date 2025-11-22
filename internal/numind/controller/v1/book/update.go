package book

import (
	"strconv"
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

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

// UpdateBookTypeRequest 更新笔记类型的请求结构
type UpdateBookTypeRequest struct {
	BookType string `json:"book_type" binding:"required"`
}

// UpdateBookType 更新笔记类型（用于 todo 打钩变为 done）
func (ctrl *BookController) UpdateBookType(c *gin.Context) {
	log.C(c).Infow("Update book type function called")

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
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的笔记ID"), nil)
		return
	}

	// 绑定请求参数
	var req UpdateBookTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
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
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("无权操作此笔记"), nil)
		return
	}

	// 验证类型转换的合法性（只允许 todo -> done 或 done -> todo）
	validTransitions := map[string][]string{
		model.BookTypeTodo: {model.BookTypeDone},
		model.BookTypeDone: {model.BookTypeTodo},
	}

	// 验证类型转换是否合法
	allowedTypes, exists := validTransitions[book.BookType]
	if exists {
		isValid := false
		for _, allowedType := range allowedTypes {
			if req.BookType == allowedType {
				isValid = true
				break
			}
		}
		if !isValid {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不允许的类型转换"), nil)
			return
		}
	} else {
		// 如果不是 todo/done 类型，不允许修改为 todo/done
		if req.BookType == model.BookTypeTodo || req.BookType == model.BookTypeDone {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("只有 todo 类型笔记可以切换为 done"), nil)
			return
		}
		// 其他类型之间的转换（如 text 和 text_with_image）允许
		validTypes := []string{
			model.BookTypeText,
			model.BookTypeTextWithImage,
		}
		isValid := false
		for _, validType := range validTypes {
			if req.BookType == validType {
				isValid = true
				break
			}
		}
		if !isValid {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的笔记类型"), nil)
			return
		}
	}

	// 记录旧类型
	oldType := book.BookType

	// 更新类型
	book.BookType = req.BookType
	if err := ctrl.b.Books().Update(c, book); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	log.C(c).Infow("Book type updated successfully",
		"book_id", bookID,
		"old_type", oldType,
		"new_type", req.BookType)

	core.WriteResponse(c, nil, book)
}
