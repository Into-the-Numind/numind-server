package image

import (
	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// BatchCreate 批量上传图片
func (ctrl *ImageController) BatchCreate(c *gin.Context) {
	log.C(c).Infow("Batch create images function called")

	var req []*model.ImageM
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	for _, img := range req {
		if _, err := govalidator.ValidateStruct(img); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
			return
		}
	}

	if err := ctrl.b.Images().BatchCreate(c, req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
