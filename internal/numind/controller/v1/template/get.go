package template

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// Get 获取模板详情
func (ctrl *TemplateController) Get(c *gin.Context) {
	log.C(c).Infow("Get template function called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	template, err := ctrl.b.Templates().GetByID(c, uint(id))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, template)
}
