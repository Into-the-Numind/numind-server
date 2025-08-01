package category

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Update 更新分类信息
func (ctrl *CategoryController) Update(c *gin.Context) {
	log.C(c).Infow("Update category function called")

	idStr := c.Param("id")
	categoryID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid category ID"), nil)
		return
	}

	var r model.CategoryM
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	r.ID = uint(categoryID)

	if err := ctrl.b.Categories().Update(c, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
