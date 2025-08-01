package template

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// List 获取模板列表
func (ctrl *TemplateController) List(c *gin.Context) {
	log.C(c).Infow("List templates function called")

	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	count, templates, err := ctrl.b.Templates().List(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": count,
		"items": templates,
	})
}
