package feedback

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// Delete 删除反馈
func (ctrl *FeedbackController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete feedback function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	idStr := c.Param("id")
	feedbackID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 删除反馈（只能删除自己的反馈）
	if err := ctrl.b.Feedbacks().Delete(c, currentUser.ID, uint(feedbackID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
