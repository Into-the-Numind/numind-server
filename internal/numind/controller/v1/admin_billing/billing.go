package admin_billing

import (
	"math"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// AdminBillingController 管理员计费控制器
type AdminBillingController struct {
	ds store.IStore
}

// New 创建管理员计费控制器
func New(ds store.IStore) *AdminBillingController {
	return &AdminBillingController{ds: ds}
}

// GetOverview 获取用量概览统计
func (ctrl *AdminBillingController) GetOverview(c *gin.Context) {
	log.C(c).Infow("Admin get billing overview called")

	result, err := ctrl.ds.Billing().GetUsageOverview(c)
	if err != nil {
		log.C(c).Errorw("Failed to get usage overview", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	byServiceType := make([]v1.AdminServiceTypeStat, len(result.ByServiceType))
	for i, s := range result.ByServiceType {
		byServiceType[i] = v1.AdminServiceTypeStat{
			ServiceType:  s.ServiceType,
			CallCount:    s.CallCount,
			CostCents:    s.CostCents,
			RevenueCents: s.RevenueCents,
			TotalTokens:  s.TotalTokens,
		}
	}
	byOperation := make([]v1.AdminOperationStat, len(result.ByOperation))
	for i, o := range result.ByOperation {
		byOperation[i] = v1.AdminOperationStat{
			Operation:    o.Operation,
			CallCount:    o.CallCount,
			CostCents:    o.CostCents,
			RevenueCents: o.RevenueCents,
		}
	}
	byProvider := make([]v1.AdminProviderStat, len(result.ByProvider))
	for i, p := range result.ByProvider {
		byProvider[i] = v1.AdminProviderStat{
			Provider:     p.Provider,
			CallCount:    p.CallCount,
			CostCents:    p.CostCents,
			RevenueCents: p.RevenueCents,
		}
	}

	core.WriteResponse(c, nil, v1.AdminBillingOverviewResponse{
		TodayCostCents:    result.TodayCostCents,
		MonthCostCents:    result.MonthCostCents,
		TotalCostCents:    result.TotalCostCents,
		TodayRevenueCents: result.TodayRevenueCents,
		MonthRevenueCents: result.MonthRevenueCents,
		TotalRevenueCents: result.TotalRevenueCents,
		TodayCallCount:    result.TodayCallCount,
		MonthCallCount:    result.MonthCallCount,
		TotalCallCount:    result.TotalCallCount,
		ByServiceType:     byServiceType,
		ByOperation:       byOperation,
		ByProvider:        byProvider,
	})
}

// ListUsageRecords 获取用量记录列表
func (ctrl *AdminBillingController) ListUsageRecords(c *gin.Context) {
	log.C(c).Infow("Admin list usage records called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	filter := store.UsageRecordFilter{
		Offset:      offset,
		Limit:       limit,
		ServiceType: c.Query("service_type"),
		Provider:    c.Query("provider"),
		Operation:   c.Query("operation"),
	}

	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if uid, err := strconv.ParseUint(userIDStr, 10, 32); err == nil {
			filter.UserID = uint(uid)
		}
	}
	if dateFrom := c.Query("date_from"); dateFrom != "" {
		if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
			filter.DateFrom = &t
		}
	}
	if dateTo := c.Query("date_to"); dateTo != "" {
		if t, err := time.Parse("2006-01-02", dateTo); err == nil {
			end := t.AddDate(0, 0, 1) // inclusive
			filter.DateTo = &end
		}
	}

	records, total, err := ctrl.ds.Billing().ListUsageRecords(c, filter)
	if err != nil {
		log.C(c).Errorw("Failed to list usage records", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	items := make([]v1.AdminUsageRecordItem, 0, len(records))
	for _, r := range records {
		items = append(items, v1.AdminUsageRecordItem{
			ID:               r.ID,
			UserID:           r.UserID,
			ServiceType:      r.ServiceType,
			Provider:         r.Provider,
			Model:            r.Model,
			Operation:        r.Operation,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			TotalTokens:      r.TotalTokens,
			ReasoningTokens:  r.ReasoningTokens,
			BytesUploaded:    r.BytesUploaded,
			ItemCount:        r.ItemCount,
			CostCents:        r.CostCents,
			RevenueCents:     r.RevenueCents,
			BizRefType:       r.BizRefType,
			BizRefID:         r.BizRefID,
			IsFallback:       r.IsFallback,
			CreatedAt:        r.CreatedAt,
		})
	}

	totalPages := int64(math.Ceil(float64(total) / float64(limit)))

	core.WriteResponse(c, nil, v1.AdminListUsageRecordsResponse{
		Total:      total,
		TotalPages: totalPages,
		Records:    items,
	})
}

// GetUserConsumption 获取用户消费排行
func (ctrl *AdminBillingController) GetUserConsumption(c *gin.Context) {
	log.C(c).Infow("Admin get user consumption called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	now := time.Now()
	period := c.DefaultQuery("period", "month")
	var from time.Time
	if period == "all" {
		from = time.Date(2020, 1, 1, 0, 0, 0, 0, now.Location())
	} else {
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	}

	items, total, err := ctrl.ds.Billing().GetUserConsumptionRanking(c, from, now, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to get user consumption ranking", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	users := make([]v1.AdminUserConsumptionItem, 0, len(items))
	for _, item := range items {
		users = append(users, v1.AdminUserConsumptionItem{
			UserID:    item.UserID,
			Username:  item.Username,
			Nickname:  item.Nickname,
			CostCents: item.CostCents,
			CallCount: item.CallCount,
		})
	}

	totalPages := int64(math.Ceil(float64(total) / float64(limit)))

	core.WriteResponse(c, nil, v1.AdminUserConsumptionResponse{
		Total:      total,
		TotalPages: totalPages,
		Users:      users,
	})
}

// ListPricingRules 获取定价规则列表
func (ctrl *AdminBillingController) ListPricingRules(c *gin.Context) {
	log.C(c).Infow("Admin list pricing rules called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	rules, total, err := ctrl.ds.Billing().ListPricingRules(c, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to list pricing rules", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	items := make([]v1.AdminPricingRuleItem, 0, len(rules))
	for _, r := range rules {
		items = append(items, v1.AdminPricingRuleItem{
			ID:                     r.ID,
			ServiceType:            r.ServiceType,
			Provider:               r.Provider,
			Model:                  r.Model,
			InputPricePerMTok:      r.InputPricePerMTok,
			OutputPricePerMTok:     r.OutputPricePerMTok,
			PricePerCall:           r.PricePerCall,
			PricePerGB:             r.PricePerGB,
			SellInputPricePerMTok:  r.SellInputPricePerMTok,
			SellOutputPricePerMTok: r.SellOutputPricePerMTok,
			SellPricePerCall:       r.SellPricePerCall,
			SellPricePerGB:         r.SellPricePerGB,
			IsActive:               r.IsActive,
			CreatedAt:              r.CreatedAt,
			UpdatedAt:              r.UpdatedAt,
		})
	}

	totalPages := int64(math.Ceil(float64(total) / float64(limit)))

	core.WriteResponse(c, nil, v1.AdminListPricingRulesResponse{
		Total:      total,
		TotalPages: totalPages,
		Rules:      items,
	})
}

// CreatePricingRule 创建定价规则
func (ctrl *AdminBillingController) CreatePricingRule(c *gin.Context) {
	log.C(c).Infow("Admin create pricing rule called")

	var req v1.AdminCreatePricingRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	rule := &model.PricingRule{
		ServiceType:            req.ServiceType,
		Provider:               req.Provider,
		Model:                  req.Model,
		InputPricePerMTok:      req.InputPricePerMTok,
		OutputPricePerMTok:     req.OutputPricePerMTok,
		PricePerCall:           req.PricePerCall,
		PricePerGB:             req.PricePerGB,
		SellInputPricePerMTok:  req.SellInputPricePerMTok,
		SellOutputPricePerMTok: req.SellOutputPricePerMTok,
		SellPricePerCall:       req.SellPricePerCall,
		SellPricePerGB:         req.SellPricePerGB,
		IsActive:               isActive,
	}

	if err := ctrl.ds.Billing().CreatePricingRule(c, rule); err != nil {
		log.C(c).Errorw("Failed to create pricing rule", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, v1.AdminPricingRuleItem{
		ID:                     rule.ID,
		ServiceType:            rule.ServiceType,
		Provider:               rule.Provider,
		Model:                  rule.Model,
		InputPricePerMTok:      rule.InputPricePerMTok,
		OutputPricePerMTok:     rule.OutputPricePerMTok,
		PricePerCall:           rule.PricePerCall,
		PricePerGB:             rule.PricePerGB,
		SellInputPricePerMTok:  rule.SellInputPricePerMTok,
		SellOutputPricePerMTok: rule.SellOutputPricePerMTok,
		SellPricePerCall:       rule.SellPricePerCall,
		SellPricePerGB:         rule.SellPricePerGB,
		IsActive:               rule.IsActive,
		CreatedAt:              rule.CreatedAt,
		UpdatedAt:              rule.UpdatedAt,
	})
}

// UpdatePricingRule 更新定价规则
func (ctrl *AdminBillingController) UpdatePricingRule(c *gin.Context) {
	log.C(c).Infow("Admin update pricing rule called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的规则ID"), nil)
		return
	}

	var req v1.AdminUpdatePricingRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	update := store.PricingRuleUpdate{
		ServiceType:            req.ServiceType,
		Provider:               req.Provider,
		Model:                  req.Model,
		InputPricePerMTok:      req.InputPricePerMTok,
		OutputPricePerMTok:     req.OutputPricePerMTok,
		PricePerCall:           req.PricePerCall,
		PricePerGB:             req.PricePerGB,
		SellInputPricePerMTok:  req.SellInputPricePerMTok,
		SellOutputPricePerMTok: req.SellOutputPricePerMTok,
		SellPricePerCall:       req.SellPricePerCall,
		SellPricePerGB:         req.SellPricePerGB,
		IsActive:               req.IsActive,
	}

	if update.IsEmpty() {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("没有需要更新的字段"), nil)
		return
	}

	if err := ctrl.ds.Billing().UpdatePricingRule(c, uint(id), update); err != nil {
		log.C(c).Errorw("Failed to update pricing rule", "error", err, "rule_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// DeletePricingRule 删除定价规则
func (ctrl *AdminBillingController) DeletePricingRule(c *gin.Context) {
	log.C(c).Infow("Admin delete pricing rule called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的规则ID"), nil)
		return
	}

	if err := ctrl.ds.Billing().DeletePricingRule(c, uint(id)); err != nil {
		log.C(c).Errorw("Failed to delete pricing rule", "error", err, "rule_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("删除失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
