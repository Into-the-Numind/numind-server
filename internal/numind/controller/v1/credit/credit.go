package credit

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	creditbiz "numind-server/internal/numind/biz/credit"
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
}

// New 创建积分控制器实例
// Phase 2 Task 2.0: creditSvc 引入，GetBalance 改走 ICreditService.GetBalance
// Phase 2 Task 2.3: 追加 promptEstimator + ds 用于 Estimate / ListPackages handler
func New(creditBiz creditbiz.ICreditBiz, creditSvc creditbiz.ICreditService, promptEstimator creditbiz.IPromptEstimator, ds store.IStore) *CreditController {
	return &CreditController{
		creditBiz:       creditBiz,
		creditSvc:       creditSvc,
		promptEstimator: promptEstimator,
		ds:              ds,
	}
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
		"booster_total":  bb.BoosterTotal,
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

// ListPackages GET /v1/credits/packages — C 用户查看自己的 credit_package 列表
// 契约（spec §4.1.1）：分页 + filter（status/type）+ sort（expires_at / created_at）
// 安全：必须按 user_id 过滤，不得跨用户
func (c *CreditController) ListPackages(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	var req ListPackagesReq
	if err := ctx.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 20
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	// 排序字段白名单（防注入）
	orderClause, err := parsePackageSort(req.Sort)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
		return
	}

	// 类型/状态白名单（防注入）
	if req.Type != "" && !isValidPackageType(req.Type) {
		core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("invalid type filter: %s", req.Type), nil)
		return
	}
	if req.Status != "" && !isValidPackageStatus(req.Status) {
		core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("invalid status filter: %s", req.Status), nil)
		return
	}

	offset := (req.Page - 1) * req.PageSize
	limit := req.PageSize

	// 显式 WHERE user_id = :current_user_id（安全基线）
	db := c.ds.DB().WithContext(ctx).Model(&model.CreditPackage{}).Where("user_id = ?", user.ID)
	if req.Status != "" {
		db = db.Where("status = ?", req.Status)
	}
	if req.Type != "" {
		db = db.Where("type = ?", req.Type)
	}

	// count（独立查询对象避免 GORM 状态污染）
	var total int64
	countDB := c.ds.DB().WithContext(ctx).Model(&model.CreditPackage{}).Where("user_id = ?", user.ID)
	if req.Status != "" {
		countDB = countDB.Where("status = ?", req.Status)
	}
	if req.Type != "" {
		countDB = countDB.Where("type = ?", req.Type)
	}
	if err := countDB.Count(&total).Error; err != nil {
		log.C(ctx).Errorw("count packages failed", "user_id", user.ID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	// list
	var packages []model.CreditPackage
	if err := db.Order(orderClause).Offset(offset).Limit(limit).Find(&packages).Error; err != nil {
		log.C(ctx).Errorw("list packages failed", "user_id", user.ID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	// 规范化为 CreditPackageItem
	items := make([]CreditPackageItem, 0, len(packages))
	for i := range packages {
		items = append(items, toCreditPackageItem(&packages[i]))
	}

	core.WriteResponse(ctx, nil, ListPackagesResp{List: items, Total: total})
}

// parsePackageSort parses a "field:direction" sort spec into a safe ORDER BY clause.
// Whitelist to avoid SQL injection: only expires_at / created_at, only asc / desc.
func parsePackageSort(sort string) (string, error) {
	if sort == "" {
		return "expires_at ASC", nil
	}
	parts := strings.SplitN(sort, ":", 2)
	field := parts[0]
	dir := "asc"
	if len(parts) == 2 {
		dir = strings.ToLower(parts[1])
	}
	allowedFields := map[string]bool{
		"expires_at": true,
		"created_at": true,
	}
	allowedDirs := map[string]string{
		"asc":  "ASC",
		"desc": "DESC",
	}
	if !allowedFields[field] {
		return "", &badSortError{msg: "sort field must be one of: expires_at, created_at"}
	}
	sqlDir, ok := allowedDirs[dir]
	if !ok {
		return "", &badSortError{msg: "sort direction must be asc or desc"}
	}
	return field + " " + sqlDir, nil
}

type badSortError struct{ msg string }

func (e *badSortError) Error() string { return e.msg }

// isValidPackageType returns true for allowed credit_package.type values.
func isValidPackageType(t string) bool {
	switch t {
	case model.CreditTypeTrial, model.CreditTypeSubscription, model.CreditTypeBooster:
		return true
	}
	return false
}

// isValidPackageStatus returns true for user-facing status filter values.
// Spec §4.1.1 lists active/expired/revoked; we support the existing status
// constants plus "revoked" as an alias for exhausted (prod has no 'revoked' state,
// but frontend contract names it that way).
func isValidPackageStatus(s string) bool {
	switch s {
	case model.CreditPackageActive, model.CreditPackageExpired,
		model.CreditPackageExhausted, model.CreditPackagePending, "revoked":
		return true
	}
	return false
}

// toCreditPackageItem maps the DB row into the wire-level item. Time fields use
// RFC3339 so the frontend TypeScript contract stays single-string.
func toCreditPackageItem(p *model.CreditPackage) CreditPackageItem {
	return CreditPackageItem{
		ID:            p.ID,
		Type:          p.Type,
		TotalCredits:  p.TotalCredits,
		RemainCredits: p.RemainCredits,
		ActivatedAt:   p.ActivatedAt.Format(time.RFC3339),
		ExpiresAt:     p.ExpiresAt.Format(time.RFC3339),
		Status:        p.Status,
		OrderID:       p.OrderID,
		CreatedAt:     p.CreatedAt.Format(time.RFC3339),
	}
}
