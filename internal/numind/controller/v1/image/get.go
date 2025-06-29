package image

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Get 获取一张图片的详细信息
func (ctrl *ImageController) Get(c *gin.Context) {
	log.C(c).Infow("Get image function called")

	idStr := c.Param("id")
	imageID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	image, err := ctrl.b.Images().GetByID(c, uint(imageID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, image)
}
