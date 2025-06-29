package card

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type ListCardRequest struct {
	BookID uint `form:"book_id"`
	Offset int  `form:"offset"`
	Limit  int  `form:"limit"`
}

type ListCardResponse struct {
	TotalCount int64          `json:"totalCount"`
	Cards      []*model.CardM `json:"cards"`
}

// List 返回卡片列表
func (ctrl *CardController) List(c *gin.Context) {
	log.C(c).Infow("List card function called")

	var r ListCardRequest
	if err := c.ShouldBindQuery(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	total, cards, err := ctrl.b.Cards().ListByBook(c, r.BookID, r.Offset, r.Limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	resp := &ListCardResponse{
		TotalCount: total,
		Cards:      cards,
	}
	core.WriteResponse(c, nil, resp)
}
