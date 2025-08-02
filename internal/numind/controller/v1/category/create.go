package category

import (
	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// Create 创建分类
func (ctrl *CategoryController) Create(c *gin.Context) {
	log.C(c).Infow("Create category function called")

	var r v1.CreateCategoryRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
		return
	}

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 转换为模型
	category := &model.CategoryM{
		Name:  r.Name,
		Color: r.Color,
		Sort:  r.Sort,
	}

	if err := ctrl.b.Categories().Create(c, currentUser.ID, category); err != nil {
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
