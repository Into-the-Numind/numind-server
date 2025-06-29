package card

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Get 获取一张卡片的详细信息
func (ctrl *CardController) Get(c *gin.Context) {
	log.C(c).Infow("Get card function called")

	idStr := c.Param("id")
	cardID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	card, err := ctrl.b.Cards().GetByID(c, uint(cardID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, card)
}
