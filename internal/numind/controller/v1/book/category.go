package book

import (
	"strconv"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// SetCategoryRequest 设置分类请求结构
type SetCategoryRequest struct {
	CategoryID *uint `json:"category_id"` // 分类ID，null表示移除分类
}

// SetCategory 设置卡册分类
func (ctrl *BookController) SetCategory(c *gin.Context) {
	log.C(c).Infow("Set book category function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取book ID
	bookIDStr := c.Param("id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 解析请求体
	var req SetCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 设置分类
	err = ctrl.b.Books().SetCategory(c, uint(bookID), currentUser.ID, req.CategoryID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
