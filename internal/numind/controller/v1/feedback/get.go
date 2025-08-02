package feedback

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
)

// Get 获取反馈详情
func (ctrl *FeedbackController) Get(c *gin.Context) {
	log.C(c).Infow("Get feedback function called")

	idStr := c.Param("id")
	feedbackID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	feedback, err := ctrl.b.Feedbacks().GetByID(c, uint(feedbackID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, feedback)
}
