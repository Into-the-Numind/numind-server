package category

import (
	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// Create 创建分类
func (ctrl *CategoryController) Create(c *gin.Context) {
	log.C(c).Infow("Create category function called")

	var r model.CategoryM
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
		return
	}

	if err := ctrl.b.Categories().Create(c, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, r)
}
