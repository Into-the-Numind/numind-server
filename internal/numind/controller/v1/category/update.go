package category

import (
	"strconv"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	v1 "numind-server/pkg/api/numind/v1"
)

// Update 更新分类
func (ctrl *CategoryController) Update(c *gin.Context) {
	log.C(c).Infow("Update category function called")

	var r v1.UpdateCategoryRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
		return
	}

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

	// 获取现有分类
	category, err := ctrl.b.Categories().GetByID(c, uint(categoryID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 更新字段
	if r.Name != nil {
		category.Name = *r.Name
	}
	if r.Color != nil {
		category.Color = *r.Color
	}
	if r.Sort != nil {
		category.Sort = *r.Sort
	}

	if err := ctrl.b.Categories().Update(c, currentUser.ID, category); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
