package feedback

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// List 获取当前用户的反馈列表
func (ctrl *FeedbackController) List(c *gin.Context) {
	log.C(c).Infow("List feedbacks function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取分页参数
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "10")

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 获取反馈列表
	feedbacks, err := ctrl.b.Feedbacks().GetByUserID(c, currentUser.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, feedbacks)
}
