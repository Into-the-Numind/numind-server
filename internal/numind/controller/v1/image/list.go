package image

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type ListImageRequest struct {
	UserID uint `form:"user_id"`
	Offset int  `form:"offset"`
	Limit  int  `form:"limit"`
}

type ListImageResponse struct {
	TotalCount int64           `json:"totalCount"`
	Images     []*model.ImageM `json:"images"`
}

// List 返回图片列表
func (ctrl *ImageController) List(c *gin.Context) {
	log.C(c).Infow("List image function called")

	var r ListImageRequest
	if err := c.ShouldBindQuery(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	total, images, err := ctrl.b.Images().ListByUser(c, r.UserID, r.Offset, r.Limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	resp := &ListImageResponse{
		TotalCount: total,
		Images:     images,
	}
	core.WriteResponse(c, nil, resp)
}
