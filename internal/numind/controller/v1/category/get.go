package category

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// Get 获取分类详情
func (ctrl *CategoryController) Get(c *gin.Context) {
	log.C(c).Infow("Get category function called")

	idStr := c.Param("id")
	categoryID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	category, err := ctrl.b.Categories().GetByID(c, uint(categoryID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 转换为响应格式
	var resp v1.CategoryResponse
	_ = copier.Copy(&resp, category)
	resp.CreatedAt = category.CreatedAt.Format("2006-01-02 15:04:05")
	resp.UpdatedAt = category.UpdatedAt.Format("2006-01-02 15:04:05")

	core.WriteResponse(c, nil, resp)
}
