package card

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Delete 删除卡片
func (ctrl *CardController) Delete(c *gin.Context) {
	log.C(c).Infow("Delete card function called")

	idStr := c.Param("id")
	cardID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	if err := ctrl.b.Cards().Delete(c, uint(cardID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
