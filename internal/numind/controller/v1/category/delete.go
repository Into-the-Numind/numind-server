package category

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// Delete 删除分类
func (ctrl *CategoryController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete category function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	idStr := c.Param("id")
	categoryID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 删除分类（只能删除自己的分类）
	if err := ctrl.b.Categories().Delete(c, currentUser.ID, uint(categoryID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
