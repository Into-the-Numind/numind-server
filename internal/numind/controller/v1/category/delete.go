package category

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Delete 删除分类
func (ctrl *CategoryController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete category function called")

	idStr := c.Param("id")
	categoryID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid category ID"), nil)
		return
	}

	if err := ctrl.b.Categories().Delete(c, uint(categoryID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
