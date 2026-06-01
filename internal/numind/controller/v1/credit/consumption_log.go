package credit

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// ListConsumptionLog GET /v1/credits/consumption-log — C 用户查看自己的积分消耗流水
// （平账后真实记录，每动作一行）。Query: page(默认1) / page_size(默认20,上限100)。
// user_id 仅取自 auth 上下文，绝不接受客户端传入 → 杜绝越权。
func (c *CreditController) ListConsumptionLog(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	page, _ := strconv.Atoi(ctx.Query("page"))          // 解析失败=0 → biz 归一化为 1
	pageSize, _ := strconv.Atoi(ctx.Query("page_size")) // 解析失败=0 → biz 归一化为 20

	items, total, err := c.creditSvc.ListConsumptionLog(ctx, user.ID, page, pageSize)
	if err != nil {
		log.C(ctx).Errorw("ListConsumptionLog failed", "user_id", user.ID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil) // 不向 C 端泄露内部 err 细节
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": items, "total": total})
}
