package book

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type ListBookRequest struct {
	UserID uint `form:"user_id"`
	Offset int  `form:"offset"`
	Limit  int  `form:"limit"`
}

type ListBookResponse struct {
	TotalCount int64          `json:"totalCount"`
	Books      []*model.BookM `json:"books"`
}

// List 返回卡册列表
func (ctrl *BookController) List(c *gin.Context) {
	log.C(c).Infow("List book function called")

	var r ListBookRequest
	if err := c.ShouldBindQuery(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	total, books, err := ctrl.b.Books().ListByUser(c, r.UserID, r.Offset, r.Limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	resp := &ListBookResponse{
		TotalCount: total,
		Books:      books,
	}
	core.WriteResponse(c, nil, resp)
}
