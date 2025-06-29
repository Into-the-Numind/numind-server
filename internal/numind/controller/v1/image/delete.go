package image

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Delete 删除图片
func (ctrl *ImageController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete image function called")

	idStr := c.Param("id")
	imageID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	if err := ctrl.b.Images().Delete(c, uint(imageID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
