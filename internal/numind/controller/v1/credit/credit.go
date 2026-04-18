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
	creditSvc creditbiz.ICreditService
}

// New 创建积分控制器实例
// Phase 2 Task 2.0: creditSvc 引入，GetBalance 改走 ICreditService.GetBalance
// 按 billing_mode 分发（credits 双档 / legacy_tier 次数视角）
func New(creditBiz creditbiz.ICreditBiz, creditSvc creditbiz.ICreditService) *CreditController {
	return &CreditController{creditBiz: creditBiz, creditSvc: creditSvc}
}

// GetBalance GET /v1/credits/balance — C 用户查看额度余额及分布
// 扩展返回字段（spec §2.11.1 + §4.5）：billing_mode / remaining_runs / monthly_limit /
// sub_expires_at / booster_earliest_expires_at；老字段 balance/sub_*/booster_* 保留向后兼容
func (c *CreditController) GetBalance(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	// ICreditService.GetBalance 按 billing_mode 分发
	bb, err := c.creditSvc.GetBalance(ctx, user)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	// 历史 balance 字段（向后兼容，web-v3 credits.ts 消费）
	balance, err := c.creditBiz.GetBalance(ctx, user.ID)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	// 构建响应：老字段（balance）+ 新字段（billing_mode 等）
	resp := gin.H{
		"balance":        balance,
		"sub_total":      bb.SubTotal,
		"sub_remain":     bb.SubRemain,
		"booster_total": bb.BoosterTotal,
		"booster_remain": bb.BoosterRemain,
		"billing_mode":   bb.BillingMode,
	}
	if bb.SubExpiresAt != nil {
		resp["sub_expires_at"] = bb.SubExpiresAt
	}
	if bb.BoosterEarliestExpiresAt != nil {
		resp["booster_earliest_expires_at"] = bb.BoosterEarliestExpiresAt
	}
	if bb.RemainingRuns != nil {
		resp["remaining_runs"] = *bb.RemainingRuns
	}
	if bb.MonthlyLimit != nil {
		resp["monthly_limit"] = *bb.MonthlyLimit
	}
	core.WriteResponse(ctx, nil, resp)
}
