package template

import (
	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// Create 创建模板
func (ctrl *TemplateController) Create(c *gin.Context) {
	log.C(c).Infow("Create template function called")

	var r v1.CreateTemplateRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
		return
	}

	// 转换为模型
	template := &model.Template{
		Name: r.Name,
		File: r.File,
	}

	if err := ctrl.b.Templates().Create(c, template); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 转换为响应格式
	var resp v1.TemplateResponse
	_ = copier.Copy(&resp, template)
	resp.CreatedAt = template.CreatedAt.Format("2006-01-02 15:04:05")
	resp.UpdatedAt = template.UpdatedAt.Format("2006-01-02 15:04:05")

	core.WriteResponse(c, nil, resp)
}
