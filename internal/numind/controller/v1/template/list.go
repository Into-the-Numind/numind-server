package template

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// List 获取模板列表
func (ctrl *TemplateController) List(c *gin.Context) {
	log.C(c).Infow("List templates function called")

	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10000"))
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	count, templates, err := ctrl.b.Templates().List(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 转换为响应格式
	templateResponses := make([]*v1.TemplateResponse, 0, len(templates))
	for _, template := range templates {
		var resp v1.TemplateResponse
		_ = copier.Copy(&resp, template)

		// 格式化时间
		resp.CreatedAt = template.CreatedAt.Format("2006-01-02 15:04:05")
		resp.UpdatedAt = template.UpdatedAt.Format("2006-01-02 15:04:05")

		templateResponses = append(templateResponses, &resp)
	}

	response := &v1.ListTemplateResponse{
		TotalCount: count,
		Templates:  templateResponses,
	}

	core.WriteResponse(c, nil, response)
}
