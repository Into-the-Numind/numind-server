package user_billing

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
)

// UserBillingController 用户计费控制器
type UserBillingController struct {
	ds store.IStore
}

// New 创建用户计费控制器
func New(ds store.IStore) *UserBillingController {
	return &UserBillingController{ds: ds}
}

// GetSummary 获取当前用户的消费概览
func (ctrl *UserBillingController) GetSummary(c *gin.Context) {
	log.C(c).Infow("User get billing summary called")

	cu, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	u := cu.(*model.User)

	overview, err := ctrl.ds.Billing().GetUserUsageOverview(c, u.ID)
	if err != nil {
		log.C(c).Errorw("Failed to get user usage overview", "error", err, "user_id", u.ID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"today_cost_cents": overview.TodayCostCents,
		"month_cost_cents": overview.MonthCostCents,
		"total_cost_cents": overview.TotalCostCents,
		"today_call_count": overview.TodayCallCount,
		"month_call_count": overview.MonthCallCount,
		"total_call_count": overview.TotalCallCount,
		"by_service_type":  overview.ByServiceType,
		"by_operation":     overview.ByOperation,
	})
}

// ListRecords 获取当前用户的消费记录
func (ctrl *UserBillingController) ListRecords(c *gin.Context) {
	log.C(c).Infow("User list billing records called")

	cu, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	u := cu.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	filter := store.UsageRecordFilter{
		UserID:      u.ID,
		Offset:      offset,
		Limit:       limit,
		ServiceType: c.Query("service_type"),
		Operation:   c.Query("operation"),
	}

	if dateFrom := c.Query("date_from"); dateFrom != "" {
		if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
			filter.DateFrom = &t
		}
	}
	if dateTo := c.Query("date_to"); dateTo != "" {
		if t, err := time.Parse("2006-01-02", dateTo); err == nil {
			end := t.AddDate(0, 0, 1)
			filter.DateTo = &end
		}
	}

	records, total, err := ctrl.ds.Billing().ListUsageRecords(c, filter)
	if err != nil {
		log.C(c).Errorw("Failed to list user billing records", "error", err, "user_id", u.ID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	items := make([]gin.H, 0, len(records))
	for _, r := range records {
		items = append(items, gin.H{
			"id":               r.ID,
			"service_type":     r.ServiceType,
			"provider":         r.Provider,
			"model":            r.Model,
			"operation":        r.Operation,
			"prompt_tokens":    r.PromptTokens,
			"completion_tokens": r.CompletionTokens,
			"total_tokens":     r.TotalTokens,
			"cost_cents":       r.CostCents,
			"created_at":       r.CreatedAt,
		})
	}

	totalPages := int64(math.Ceil(float64(total) / float64(limit)))

	core.WriteResponse(c, nil, gin.H{
		"total":       total,
		"total_pages": totalPages,
		"records":     items,
	})
}
