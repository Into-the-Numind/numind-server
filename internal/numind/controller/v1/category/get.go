package category

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
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

	core.WriteResponse(c, nil, category)
}
