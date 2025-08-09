package book

import (
	"strconv"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/gin-gonic/gin"
)

type ListBookResponse struct {
	TotalCount int64          `json:"total_count"`
	Books      []*model.BookM `json:"books"`
}

// List 返回卡册列表
func (ctrl *BookController) List(c *gin.Context) {
	log.C(c).Infow("List book function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取分页参数
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "10")
	categoryIDStr := c.Query("category_id")

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var total int64
	var books []*model.BookM

	// 如果指定了分类ID，按分类查询；否则按用户查询
	if categoryIDStr != "" {
		categoryID, err := strconv.ParseUint(categoryIDStr, 10, 64)
		if err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter, nil)
			return
		}
		total, books, err = ctrl.b.Books().ListByCategory(c, uint(categoryID), offset, limit)
	} else {
		total, books, err = ctrl.b.Books().ListByUser(c, currentUser.ID, offset, limit)
	}

	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 统一展示规则：列表中的 image_url 也去掉 /opt 前缀
	for _, b := range books {
		if b != nil {
			b.ImageUrl = util.GetDisplayURL(b.ImageUrl)
		}
	}

	resp := &ListBookResponse{
		TotalCount: total,
		Books:      books,
	}
	core.WriteResponse(c, nil, resp)
}
