package book

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Delete 删除卡册
func (ctrl *BookController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete book function called")

	idStr := c.Param("id")
	bookID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	if err := ctrl.b.Books().Delete(c, uint(bookID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
