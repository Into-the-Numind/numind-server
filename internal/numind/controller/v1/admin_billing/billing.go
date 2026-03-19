package admin_billing

import (
	"encoding/json"
	"math"
	"sort"
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
		var meta map[string]string
		if r.Metadata != "" {
			_ = json.Unmarshal([]byte(r.Metadata), &meta)
		}
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
			Metadata:         meta,
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

// percentileInt64 从已排序的切片中计算百分位数（p: 50/90/95）
func percentileInt64(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*float64(p)/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// GetTiers GET /billing/pricing-rules/:id/tiers
func (ctrl *AdminBillingController) GetTiers(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	tiers, err := ctrl.ds.Billing().GetTiersByRuleID(c, uint(id))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	items := make([]v1.AdminPricingRuleTierItem, len(tiers))
	for i, t := range tiers {
		items[i] = v1.AdminPricingRuleTierItem{
			ID: uint64(t.ID), RuleID: uint64(t.RuleID), TokenType: t.TokenType,
			MinTokens: t.MinTokens, MaxTokens: t.MaxTokens,
			CostPerMTok: t.CostPerMTok, SellPerMTok: t.SellPerMTok,
		}
	}
	core.WriteResponse(c, nil, items)
}

// ReplaceTiers PUT /billing/pricing-rules/:id/tiers
func (ctrl *AdminBillingController) ReplaceTiers(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	var req v1.AdminReplaceTiersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	tiers := make([]model.PricingRuleTier, len(req.Tiers))
	for i, t := range req.Tiers {
		tiers[i] = model.PricingRuleTier{
			TokenType: t.TokenType, MinTokens: t.MinTokens,
			MaxTokens: t.MaxTokens, CostPerMTok: t.CostPerMTok,
			SellPerMTok: t.SellPerMTok,
		}
	}
	if err := ctrl.ds.Billing().ReplaceTiers(c, uint(id), tiers); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// Recalculate POST /billing/recalculate
func (ctrl *AdminBillingController) Recalculate(c *gin.Context) {
	var req v1.AdminRecalculateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	from, err := time.Parse("2006-01-02", req.From)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	to, err := time.Parse("2006-01-02", req.To)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	result, err := ctrl.ds.Billing().RecalculateCosts(c, from, to, req.DryRun)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, v1.AdminRecalculateResponse{
		AffectedRecords:   result.AffectedRecords,
		OldTotalCostCents: result.OldTotalCostCents,
		NewTotalCostCents: result.NewTotalCostCents,
		DeltaCents:        result.DeltaCents,
		DryRun:            result.DryRun,
	})
}

// GetAnalytics GET /billing/analytics
func (ctrl *AdminBillingController) GetAnalytics(c *gin.Context) {
	var req v1.AdminAnalyticsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	from, err := time.Parse("2006-01-02", req.From)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}
	to, err := time.Parse("2006-01-02", req.To)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	data, err := ctrl.ds.Billing().GetAnalytics(c, store.AnalyticsFilter{From: from, To: to})
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// run_distribution
	runBuckets := map[string]int64{
		"0-3000": 0, "3001-8000": 0, "8001-16000": 0,
		"16001-32000": 0, "32001-64000": 0, "64001+": 0,
	}
	var runTokensSorted []int64
	var totalRunTokens int64
	for _, r := range data.RunStats {
		runTokensSorted = append(runTokensSorted, r.TotalTokens)
		totalRunTokens += r.TotalTokens
		switch {
		case r.TotalTokens <= 3000:
			runBuckets["0-3000"]++
		case r.TotalTokens <= 8000:
			runBuckets["3001-8000"]++
		case r.TotalTokens <= 16000:
			runBuckets["8001-16000"]++
		case r.TotalTokens <= 32000:
			runBuckets["16001-32000"]++
		case r.TotalTokens <= 64000:
			runBuckets["32001-64000"]++
		default:
			runBuckets["64001+"]++
		}
	}
	sort.Slice(runTokensSorted, func(i, j int) bool { return runTokensSorted[i] < runTokensSorted[j] })

	// user_distribution
	userBuckets := map[string]int64{
		"0-50000": 0, "50001-150000": 0, "150001-300000": 0,
		"300001-500000": 0, "500001-1000000": 0, "1000001+": 0,
	}
	var userCostSorted []int64
	userDetails := make([]v1.AdminAnalyticsUserDetail, 0, len(data.UserStats))
	for _, u := range data.UserStats {
		userCostSorted = append(userCostSorted, u.PeriodCostCents)
		userDetails = append(userDetails, v1.AdminAnalyticsUserDetail{
			UserID: u.UserID, PeriodTokens: u.PeriodTokens, PeriodCostCents: u.PeriodCostCents,
		})
		switch {
		case u.PeriodTokens <= 50000:
			userBuckets["0-50000"]++
		case u.PeriodTokens <= 150000:
			userBuckets["50001-150000"]++
		case u.PeriodTokens <= 300000:
			userBuckets["150001-300000"]++
		case u.PeriodTokens <= 500000:
			userBuckets["300001-500000"]++
		case u.PeriodTokens <= 1000000:
			userBuckets["500001-1000000"]++
		default:
			userBuckets["1000001+"]++
		}
	}
	sort.Slice(userCostSorted, func(i, j int) bool { return userCostSorted[i] < userCostSorted[j] })

	// model_breakdown
	var totalModelTokens int64
	for _, m := range data.ModelStats {
		totalModelTokens += m.PeriodTokens
	}
	modelBreakdown := make([]v1.AdminAnalyticsModelStat, len(data.ModelStats))
	for i, m := range data.ModelStats {
		sharePct := 0
		if totalModelTokens > 0 {
			sharePct = int(m.PeriodTokens * 100 / totalModelTokens)
		}
		modelBreakdown[i] = v1.AdminAnalyticsModelStat{
			Model: m.Model, TokenSharePct: sharePct, PeriodCostCents: m.PeriodCostCents,
		}
	}

	// top users (最多 20 个)
	topN := 20
	if len(data.UserStats) < topN {
		topN = len(data.UserStats)
	}
	topUsers := make([]v1.AdminAnalyticsTopUser, topN)
	for i := 0; i < topN; i++ {
		u := data.UserStats[i]
		topUsers[i] = v1.AdminAnalyticsTopUser{
			UserID: u.UserID, Nickname: u.Nickname,
			PeriodRuns: u.PeriodRuns, PeriodTokens: u.PeriodTokens,
			PeriodCostCents: u.PeriodCostCents,
		}
	}

	// summary
	avgTokensPerRun := int64(0)
	if len(data.RunStats) > 0 {
		avgTokensPerRun = totalRunTokens / int64(len(data.RunStats))
	}

	bucketOrder := []string{"0-3000", "3001-8000", "8001-16000", "16001-32000", "32001-64000", "64001+"}
	runDist := make([]v1.AdminAnalyticsBucket, len(bucketOrder))
	for i, k := range bucketOrder {
		runDist[i] = v1.AdminAnalyticsBucket{Bucket: k, Count: runBuckets[k]}
	}

	userBucketOrder := []string{"0-50000", "50001-150000", "150001-300000", "300001-500000", "500001-1000000", "1000001+"}
	userDist := make([]v1.AdminAnalyticsBucket, len(userBucketOrder))
	for i, k := range userBucketOrder {
		userDist[i] = v1.AdminAnalyticsBucket{Bucket: k, Count: userBuckets[k]}
	}

	core.WriteResponse(c, nil, v1.AdminAnalyticsResponse{
		Summary: v1.AdminAnalyticsSummary{
			ActiveUsers:         int64(len(data.UserStats)),
			TotalRuns:           int64(len(data.RunStats)),
			DaysInRange:         data.DaysInRange,
			AvgTokensPerRun:     avgTokensPerRun,
			P50TokensPerRun:     percentileInt64(runTokensSorted, 50),
			P90TokensPerRun:     percentileInt64(runTokensSorted, 90),
			P95TokensPerRun:     percentileInt64(runTokensSorted, 95),
			P50CostCentsPerUser: percentileInt64(userCostSorted, 50),
			P90CostCentsPerUser: percentileInt64(userCostSorted, 90),
			P95CostCentsPerUser: percentileInt64(userCostSorted, 95),
		},
		RunDistribution:  runDist,
		UserDistribution: userDist,
		UserDetails:      userDetails,
		ModelBreakdown:   modelBreakdown,
		TopUsers:         topUsers,
	})
}
