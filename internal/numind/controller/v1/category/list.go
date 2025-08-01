package category

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type ListCategoryRequest struct {
	Offset int `form:"offset"`
	Limit  int `form:"limit"`
}

type ListCategoryResponse struct {
	TotalCount int64              `json:"totalCount"`
	Categories []*model.CategoryM `json:"categories"`
}

// List 返回分类列表
func (ctrl *CategoryController) List(c *gin.Context) {
	log.C(c).Infow("List category function called")

	var r ListCategoryRequest
	if err := c.ShouldBindQuery(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	total, categories, err := ctrl.b.Categories().List(c, r.Offset, r.Limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	resp := &ListCategoryResponse{
		TotalCount: total,
		Categories: categories,
	}
	core.WriteResponse(c, nil, resp)
}
