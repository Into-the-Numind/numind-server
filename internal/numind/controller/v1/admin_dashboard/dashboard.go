package admin_dashboard

import (
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

// AdminDashboardController 管理员仪表盘控制器
type AdminDashboardController struct {
	ds store.IStore
}

// New 创建管理员仪表盘控制器
func New(ds store.IStore) *AdminDashboardController {
	return &AdminDashboardController{ds: ds}
}

// GetStats 获取仪表盘统计数据
func (ctrl *AdminDashboardController) GetStats(c *gin.Context) {
	log.C(c).Infow("Admin get dashboard stats called")

	db := ctrl.ds.DB()

	// 用户总数及等级分布（合并为一条 SQL）
	type tierCount struct {
		Tier  string `gorm:"column:tier"`
		Count int64  `gorm:"column:cnt"`
	}
	var tierCounts []tierCount
	if err := db.Model(&model.User{}).
		Select("COALESCE(NULLIF(user_tier, ''), 'free') as tier, COUNT(*) as cnt").
		Group("tier").
		Scan(&tierCounts).Error; err != nil {
		log.C(c).Errorw("Failed to query user tier breakdown", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	var totalUsers int64
	var tierBreakdown v1.TierBreakdown
	for _, tc := range tierCounts {
		totalUsers += tc.Count
		switch tc.Tier {
		case "free":
			tierBreakdown.Free = tc.Count
		case "standard":
			tierBreakdown.Standard = tc.Count
		case "premium":
			tierBreakdown.Premium = tc.Count
		}
	}

	// SOP运行总数
	var totalRuns int64
	if err := db.Model(&model.SopRun{}).Count(&totalRuns).Error; err != nil {
		log.C(c).Errorw("Failed to count total runs", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// 今日运行数
	var runsToday int64
	today := time.Now().Truncate(24 * time.Hour)
	if err := db.Model(&model.SopRun{}).Where("created_at >= ?", today).Count(&runsToday).Error; err != nil {
		log.C(c).Errorw("Failed to count today's runs", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// Token使用总量
	// TODO: 考虑添加时间范围过滤参数
	var totalTokens int64
	if err := db.Model(&model.SopNodeRun{}).Select("COALESCE(SUM(total_tokens), 0)").Scan(&totalTokens).Error; err != nil {
		log.C(c).Errorw("Failed to sum total tokens", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, v1.DashboardStatsResponse{
		TotalUsers:    totalUsers,
		TierBreakdown: tierBreakdown,
		TotalRuns:     totalRuns,
		RunsToday:     runsToday,
		TotalTokens:   totalTokens,
	})
}

// GetRecentRuns 获取最近的SOP运行记录
func (ctrl *AdminDashboardController) GetRecentRuns(c *gin.Context) {
	log.C(c).Infow("Admin get recent runs called")

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	db := ctrl.ds.DB()

	var runs []model.SopRun
	if err := db.Preload("Template").Preload("User").
		Order("created_at DESC").
		Limit(limit).
		Find(&runs).Error; err != nil {
		log.C(c).Errorw("Failed to query recent runs", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// 收集 run IDs 用于查询 token 统计
	runIDs := make([]uint, len(runs))
	for i, run := range runs {
		runIDs[i] = run.ID
	}

	// 批量查询每个 run 的 total_tokens
	type tokenSum struct {
		RunID       uint  `gorm:"column:run_id"`
		TotalTokens int64 `gorm:"column:total_tokens"`
	}
	var tokenSums []tokenSum
	if len(runIDs) > 0 {
		db.Model(&model.SopNodeRun{}).
			Select("run_id, COALESCE(SUM(total_tokens), 0) as total_tokens").
			Where("run_id IN ?", runIDs).
			Group("run_id").
			Scan(&tokenSums)
	}
	tokenMap := make(map[uint]int64)
	for _, ts := range tokenSums {
		tokenMap[ts.RunID] = ts.TotalTokens
	}

	items := make([]v1.RecentRunItem, 0, len(runs))
	for _, run := range runs {
		item := v1.RecentRunItem{
			ID:          run.ID,
			TemplateID:  run.TemplateID,
			UserID:      run.UserID,
			Status:      run.Status,
			TotalTokens: tokenMap[run.ID],
			StartedAt:   run.StartedAt,
			FinishedAt:  run.FinishedAt,
			CreatedAt:   run.CreatedAt,
		}
		if run.Template != nil {
			item.TemplateName = run.Template.Name
		}
		if run.User != nil {
			item.UserNickname = run.User.Nickname
		}
		items = append(items, item)
	}

	core.WriteResponse(c, nil, gin.H{
		"runs": items,
	})
}
