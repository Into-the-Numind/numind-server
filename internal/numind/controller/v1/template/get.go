package template

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// Get 获取模板详情
func (ctrl *TemplateController) Get(c *gin.Context) {
	log.C(c).Infow("Get template function called")

	idStr := c.Param("id")
	templateID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	template, err := ctrl.b.Templates().GetByID(c, uint(templateID))
	if err != nil {
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
