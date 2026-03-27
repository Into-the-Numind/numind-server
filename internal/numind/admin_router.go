package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/controller/v1/admin_billing"
	"numind-server/internal/numind/controller/v1/admin_credit"
	"numind-server/internal/numind/controller/v1/admin_dashboard"
	"numind-server/internal/numind/controller/v1/admin_login"
	"numind-server/internal/numind/controller/v1/admin_order"
	"numind-server/internal/numind/controller/v1/admin_sop"
	"numind-server/internal/numind/controller/v1/admin_user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

	"github.com/gin-gonic/gin"

	importMw "numind-server/internal/pkg/middleware"
)

// installAdminRouters 注册所有管理后台业务路由
func installAdminRouters(g *gin.Engine) error {
	// 注册 404 Handler
	g.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	})

	// 注册 /healthz handler
	g.GET("/healthz", func(c *gin.Context) {
		log.C(c).Infow("Admin healthz function called")
		core.WriteResponse(c, nil, map[string]string{"status": "ok"})
	})

	// 初始化控制器
	loginCtrl := admin_login.New(store.S)
	userCtrl := admin_user.New(store.S)
	dashboardCtrl := admin_dashboard.New(store.S)

	billingCtrl := admin_billing.New(store.S)

	b := biz.NewBiz(store.S)
	sopCtrl := admin_sop.NewSopController(b.Sop())
	adminCreditCtrl := admin_credit.New(b.Credit(), store.S)

	v1Group := g.Group("/v1")

	// 管理员登录（不需要鉴权）
	v1Group.POST("/admin/login", loginCtrl.Login)

	// 需要管理员鉴权的接口
	adminGroup := v1Group.Group("/admin")
	adminGroup.Use(importMw.AdminAuthMiddleware())

	// 登出（客户端清除 token，预留后续 token 黑名单实现）
	adminGroup.POST("/logout", loginCtrl.Logout)

	// 仪表盘
	{
		adminGroup.GET("/dashboard/stats", dashboardCtrl.GetStats)
		adminGroup.GET("/dashboard/recent-runs", dashboardCtrl.GetRecentRuns)
	}

	// 用户管理
	{
		adminGroup.GET("/users", userCtrl.ListUsers)
		adminGroup.GET("/users/:id", userCtrl.GetUser)
		adminGroup.PUT("/users/:id", userCtrl.UpdateUser)
		adminGroup.PUT("/users/:id/status", userCtrl.UpdateUserStatus)
		adminGroup.PUT("/users/:id/tier", userCtrl.UpdateUserTier)
		adminGroup.POST("/users/:id/reset-password", userCtrl.ResetPassword)
	}

	// SOP管理（复用已有的admin_sop控制器）
	{
		// 模板管理
		adminGroup.POST("/sop/templates", sopCtrl.CreateTemplate)
		adminGroup.GET("/sop/templates", sopCtrl.ListTemplates)
		adminGroup.GET("/sop/templates/:id", sopCtrl.GetTemplate)
		adminGroup.PUT("/sop/templates/:id", sopCtrl.UpdateTemplate)
		adminGroup.DELETE("/sop/templates/:id", sopCtrl.DeleteTemplate)

		// 节点管理
		adminGroup.POST("/sop/nodes", sopCtrl.CreateNode)
		adminGroup.GET("/sop/templates/:id/nodes", sopCtrl.ListNodesByTemplate)
		adminGroup.GET("/sop/nodes/:id", sopCtrl.GetNode)
		adminGroup.PUT("/sop/nodes/:id", sopCtrl.UpdateNode)
		adminGroup.DELETE("/sop/nodes/:id", sopCtrl.DeleteNode)

		// 运行监控
		adminGroup.GET("/sop/runs", sopCtrl.ListRuns)
		adminGroup.GET("/sop/runs/:id", sopCtrl.GetRun)
		adminGroup.GET("/sop/runs/:id/detail", sopCtrl.GetRunDetail)

		// 笔记查看
		adminGroup.GET("/sop/notes/:id", sopCtrl.GetNote)
		adminGroup.GET("/sop/users/:user_id/notes", sopCtrl.ListNotesByUser)
	}

	// 积分管理
	{
		adminGroup.GET("/credits/users", adminCreditCtrl.ListUsers)
		adminGroup.GET("/credits/users/:id", adminCreditCtrl.GetUserDetail)
		adminGroup.POST("/credits/users/:id/recharge", adminCreditCtrl.Recharge)
	}

	// 订单管理
	{
		adminOrderCtrl := admin_order.New(store.S)
		adminGroup.GET("/orders", adminOrderCtrl.ListOrders)
		adminGroup.GET("/orders/:id", adminOrderCtrl.GetOrder)
	}

	// 计费管理
	{
		adminGroup.GET("/billing/overview", billingCtrl.GetOverview)
		adminGroup.GET("/billing/records", billingCtrl.ListUsageRecords)
		adminGroup.GET("/billing/users", billingCtrl.GetUserConsumption)
		adminGroup.GET("/billing/pricing-rules", billingCtrl.ListPricingRules)
		adminGroup.POST("/billing/pricing-rules", billingCtrl.CreatePricingRule)
		adminGroup.PUT("/billing/pricing-rules/:id", billingCtrl.UpdatePricingRule)
		adminGroup.DELETE("/billing/pricing-rules/:id", billingCtrl.DeletePricingRule)
		adminGroup.GET("/billing/pricing-rules/:id/tiers", billingCtrl.GetTiers)
		adminGroup.PUT("/billing/pricing-rules/:id/tiers", billingCtrl.ReplaceTiers)
		adminGroup.POST("/billing/recalculate", billingCtrl.Recalculate)
		adminGroup.GET("/billing/analytics", billingCtrl.GetAnalytics)
		adminGroup.GET("/billing/tier-changes", billingCtrl.ListTierChangeLogs)
		adminGroup.GET("/billing/tier-changes/stats", billingCtrl.GetTierChangeStats)
	}

	return nil
}
