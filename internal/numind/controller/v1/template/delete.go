package template

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// Delete 删除模板
func (ctrl *TemplateController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete template function called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	if err := ctrl.b.Templates().Delete(c, uint(id)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "Template deleted successfully",
	})
}
