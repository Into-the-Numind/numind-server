package credit

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	creditbiz "numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// CreditController 积分控制器
type CreditController struct {
	creditBiz       creditbiz.ICreditBiz
	creditSvc       creditbiz.ICreditService
	promptEstimator creditbiz.IPromptEstimator
	ds              store.IStore
	membershipSvc   *membership.MembershipService
}

// New 创建积分控制器实例
// Phase 2 Task 2.0: creditSvc 引入，GetBalance 改走 ICreditService.GetBalance
// Phase 2 Task 2.3: 追加 promptEstimator + ds 用于 Estimate handler
// T9: ListPackages handler deleted; ds 字段保留供 Estimate 使用
func New(creditBiz creditbiz.ICreditBiz, creditSvc creditbiz.ICreditService, promptEstimator creditbiz.IPromptEstimator, ds store.IStore) *CreditController {
	return &CreditController{
		creditBiz:       creditBiz,
		creditSvc:       creditSvc,
		promptEstimator: promptEstimator,
		ds:              ds,
	}
}

// WithMembershipSvc attaches a MembershipService to the controller, enabling
// the B2B2C grant-membership endpoint (Task 10 / §5.1 + §5.7).
func (c *CreditController) WithMembershipSvc(svc *membership.MembershipService) *CreditController {
	c.membershipSvc = svc
	return c
}

// GetBalance GET /v1/credits/balance — C 用户查看额度余额及分布
//
// credits-only billing (legacy_tier removed 2026-05): all users go through
// the new membership service. If membershipSvc is not wired we fail loud
// rather than fall back to the deleted legacy path.
func (c *CreditController) GetBalance(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	if c.membershipSvc == nil {
		log.C(ctx).Errorw("GetBalance called but membershipSvc not wired", "user_id", user.ID)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	c.getBalanceFromMembership(ctx, user)
}

// getBalanceFromMembership 从新 membership 系统读取 credits 制用户的余额和状态。
//
// Audit P2#4/#5 cleanup: removed hardcoded billing_mode='credits' (meaningless
// post-legacy-deprecation) and the backward-compat fields balance/sub_total/
// sub_remain/booster_remain (replaced by trial_remaining/cycle_remaining/
// booster_usable in T2; frontend reads were already migrated).
func (c *CreditController) getBalanceFromMembership(ctx *gin.Context, user *model.User) {
	now := time.Now().UTC()
	view, err := c.membershipSvc.GetBalance(ctx, uint64(user.ID), now)
	if err != nil {
		log.C(ctx).Errorw("membershipSvc.GetBalance failed", "user_id", user.ID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	resp := gin.H{
		"membership_state": view.MembershipState,
		"trial_remaining":  view.TrialRemaining,
		"cycle_remaining":  view.CycleRemaining,
		"booster_total":    view.BoosterTotal,
		"booster_usable":   view.BoosterUsable,
	}
	if view.SubExpiresAt != nil {
		resp["sub_expires_at"] = view.SubExpiresAt
	}
	if view.TrialExpiresAt != nil {
		resp["trial_expires_at"] = view.TrialExpiresAt
	}
	if view.CycleEnd != nil {
		resp["cycle_end"] = view.CycleEnd
	}
	core.WriteResponse(ctx, nil, resp)
}

// Estimate POST /v1/credits/estimate — C 用户运行前估算消耗
// 契约（spec §3.11 + §4.3）：
//   - req 不含 prompt_chars，后端调 promptEstimator.Estimate(op, ref_id) 渲染
//   - SOP 场景 (sop_run)：total_estimated_credits = 遍历所有 node 求和，first_node_estimate = 首 node，node_count = N
//   - 非 SOP 场景：total_estimated_credits = first_node_estimate，node_count = 1
//   - ErrInsufficientCredits 仍返回 200 + sufficient=false，由前端拦截器处理
func (c *CreditController) Estimate(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	var req EstimateReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	// 后端渲染整单 prompt（sop_run 时 chars 是所有 node 字符之和）
	chars, modelName, provider, err := c.promptEstimator.Estimate(ctx, req.Operation, req.ReferenceID)
	if err != nil {
		log.C(ctx).Warnw("promptEstimator.Estimate failed", "op", req.Operation, "ref", req.ReferenceID, "err", err)
		core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("估算输入无效: %s", err.Error()), nil)
		return
	}

	// 主估算：CheckAndEstimate 接受整单 prompt_chars，返回 total 估算
	pre, err := c.creditSvc.CheckAndEstimate(ctx, user, creditbiz.Operation(req.Operation), creditbiz.EstimationInput{
		PromptChars: chars,
		Model:       modelName,
		Provider:    provider,
	})
	// ErrInsufficientCredits 语义化：仍返回 200 + sufficient=false
	if err != nil && !errors.Is(err, creditbiz.ErrInsufficientCredits) {
		log.C(ctx).Errorw("CheckAndEstimate failed", "user_id", user.ID, "op", req.Operation, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer.SetMessage("估算失败: %s", err.Error()), nil)
		return
	}
	if pre == nil {
		// 防御性编程（理论上 ErrInsufficientCredits 时 pre 也非 nil）
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	// SOP 场景：额外计算 node_count + first_node_estimate（§4.3 聚合口径）
	total := pre.EstimatedCredits
	firstNode := pre.EstimatedCredits
	nodeCount := 1

	if req.Operation == string(creditbiz.OpSopRun) && !pre.SkipDeduction {
		if fn, nc, ok := c.firstNodeAndCount(ctx, user, req.ReferenceID); ok {
			firstNode = fn
			nodeCount = nc
		}
	}

	resp := EstimateResp{
		TotalEstimatedCredits: total,
		FirstNodeEstimate:     &firstNode,
		NodeCount:             &nodeCount,
		Sufficient:            pre.Sufficient,
		SkipDeduction:         pre.SkipDeduction,
		Reason:                pre.Reason,
		Balance:               pre.Balance,
		CoefficientID:         pre.CoefficientID,
	}
	core.WriteResponse(ctx, nil, resp)
}

// firstNodeAndCount 计算首节点估算 + 节点总数（sop_run 专用）
// 失败/为空返回 ok=false；调用方应 fallback 到 total
func (c *CreditController) firstNodeAndCount(ctx *gin.Context, user *model.User, referenceID string) (int64, int, bool) {
	templateID, err := strconv.ParseUint(referenceID, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	nodes, err := c.ds.Sop().ListNodesByTemplate(uint(templateID))
	if err != nil || len(nodes) == 0 {
		return 0, 0, false
	}
	// 首 node 独立估算：字符 = name + description + prompt（与 estimateSopRun 口径一致）
	firstNode := nodes[0]
	firstChars := runeCountMany(firstNode.Prompt, firstNode.Description, firstNode.Name)
	pre, err := c.creditSvc.CheckAndEstimate(ctx, user, creditbiz.OpSopRun, creditbiz.EstimationInput{
		PromptChars: firstChars,
		Model:       firstNode.ModelName,
		Provider:    creditbiz.ProviderFromModel(firstNode.ModelName),
	})
	if err != nil && !errors.Is(err, creditbiz.ErrInsufficientCredits) {
		// 首 node 估算失败不阻塞主响应；返回 total 作为 fallback
		return 0, 0, false
	}
	if pre == nil {
		return 0, 0, false
	}
	return pre.EstimatedCredits, len(nodes), true
}

// runeCountMany 统计多个字符串的 utf-8 字符数（与 estimateSopRun 保持一致）
func runeCountMany(ss ...string) int {
	n := 0
	for _, s := range ss {
		n += len([]rune(s))
	}
	return n
}

// T9: ListPackages (GET /v1/credits/packages) deleted — credit_package dead route removed.
