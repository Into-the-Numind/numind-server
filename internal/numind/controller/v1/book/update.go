package book

import (
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// Update 更新卡册信息
func (ctrl *BookController) Update(c *gin.Context) {
	log.C(c).Infow("Update book function called")

	var r model.BookM
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// Update the ViewTime field to current time
	now := time.Now()
	r.ViewTime = &now

	if err := ctrl.b.Books().Update(c, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
