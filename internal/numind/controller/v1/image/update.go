package image

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// Update 更新图片信息
func (ctrl *ImageController) Update(c *gin.Context) {
	log.C(c).Infow("Update image function called")

	var r model.ImageM
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if err := ctrl.b.Images().Update(c, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
