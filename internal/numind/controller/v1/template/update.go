package template

import (
	"strconv"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// Update 更新模板
func (ctrl *TemplateController) Update(c *gin.Context) {
	log.C(c).Infow("Update template function called")

	var r v1.UpdateTemplateRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
		return
	}

	idStr := c.Param("id")
	templateID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 获取现有模板
	template, err := ctrl.b.Templates().GetByID(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 更新字段
	if r.Name != nil {
		template.Name = *r.Name
	}
	if r.File != nil {
		template.File = *r.File
	}
	if r.Preview != nil {
		template.Preview = *r.Preview
	}
	if r.IsMemberOnly != nil {
		template.IsMemberOnly = *r.IsMemberOnly
	}

	if err := ctrl.b.Templates().Update(c, template); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
