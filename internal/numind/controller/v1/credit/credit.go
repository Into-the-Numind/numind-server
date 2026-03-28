package credit

import (
	"github.com/gin-gonic/gin"

	creditbiz "numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// CreditController 积分控制器
type CreditController struct {
	creditBiz creditbiz.ICreditBiz
}

// New 创建积分控制器实例
func New(creditBiz creditbiz.ICreditBiz) *CreditController {
	return &CreditController{creditBiz: creditBiz}
}

// GetBalance GET /v1/credits/balance — C 用户查看额度余额及分布
func (c *CreditController) GetBalance(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	balance, err := c.creditBiz.GetBalance(ctx, user.ID)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	subTotal, subRemain, boosterTotal, boosterRemain, err := c.creditBiz.GetQuotaBreakdown(ctx, user.ID)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(ctx, nil, map[string]int64{
		"balance":         balance,
		"sub_total":       subTotal,
		"sub_remain":      subRemain,
		"booster_total":   boosterTotal,
		"booster_remain":  boosterRemain,
	})
}
