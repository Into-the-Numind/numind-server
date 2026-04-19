package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/controller/v1/admin_ai"
	"numind-server/internal/numind/controller/v1/admin_b2b"
	"numind-server/internal/numind/controller/v1/admin_billing"
	"numind-server/internal/numind/controller/v1/admin_credit"
	"numind-server/internal/numind/controller/v1/admin_dashboard"
	"numind-server/internal/numind/controller/v1/admin_migration"
	"numind-server/internal/numind/controller/v1/admin_login"
	"numind-server/internal/numind/controller/v1/admin_order"
	"numind-server/internal/numind/controller/v1/admin_sop"
	"numind-server/internal/numind/controller/v1/admin_user"
	monitorcontroller "numind-server/internal/numind/controller/v1/monitor"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice/registry"
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

	// Phase 2 T2.3: coefficient CRUD. EstimationBiz is constructed ad-hoc here
	// rather than threaded through biz.NewBiz to avoid expanding the IBiz
	// interface surface for a single admin concern.
	coefficientCtrl := admin_credit.NewCoefficientController(
		credit.NewEstimationBiz(store.S, b.Pricing()),
		store.S,
	)

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

	// Phase 2 T2.3: estimation-coefficient CRUD (spec §3.11 + §4.1.2)
	{
		adminGroup.GET("/estimation-coefficients", coefficientCtrl.ListCoefficients)
		adminGroup.GET("/estimation-coefficients/history", coefficientCtrl.ListCoefficientHistory)
		adminGroup.POST("/estimation-coefficients", coefficientCtrl.CreateCoefficient)
		adminGroup.PUT("/estimation-coefficients/:id", coefficientCtrl.UpdateCoefficient)
		adminGroup.DELETE("/estimation-coefficients/:id", coefficientCtrl.DeleteCoefficient)
	}

	// Phase 2 T2.3: billing-mode-init migration (spec §4.4.3, one-shot)
	{
		migrationCtrl := admin_migration.NewMigrationController(store.S)
		adminGroup.GET("/migrations/billing-mode-init/status", migrationCtrl.GetInitStatus)
		adminGroup.POST("/migrations/billing-mode-init", migrationCtrl.InitBillingMode)
	}

	// 订单管理
	{
		adminOrderCtrl := admin_order.New(store.S)
		adminGroup.GET("/orders", adminOrderCtrl.ListOrders)
		adminGroup.GET("/orders/:id", adminOrderCtrl.GetOrder)
	}

	// B2B 月度结算报表（Q1.4）: 父账户"帮开通"的 credit_package 按月聚合
	{
		adminB2BCtrl := admin_b2b.New(b2b_billing.New(store.S))
		adminGroup.GET("/b2b-billing-report", adminB2BCtrl.GetBillingReport)
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

	// 博主内容监控管理
	{
		monitorCtrl := monitorcontroller.NewMonitorController(b.Monitor(), store.S)
		adminMonitorGroup := adminGroup.Group("/monitor")
		adminMonitorGroup.GET("/overview", monitorCtrl.AdminOverview)
		adminMonitorGroup.GET("/bloggers", monitorCtrl.AdminListBloggers)
		adminMonitorGroup.GET("/notes", monitorCtrl.AdminListNotes)
		adminMonitorGroup.GET("/briefings", monitorCtrl.AdminListBriefings)
		adminMonitorGroup.GET("/users/:user_id/config", monitorCtrl.AdminGetUserConfig)
		adminMonitorGroup.PUT("/users/:user_id/config", monitorCtrl.AdminUpdateUserConfig)
	}

	// AI Service Manager — 服务 CRUD（Task 12）+ Task Profile（Task 13）+ Provider CRUD（Task 4）
	{
		reg := registry.New(store.S.DB())
		aiSvcBiz := aiservice_admin.New(reg, store.S.DB())
		aiSvcCtrl := admin_ai.NewAIServiceController(aiSvcBiz)
		aiTaskCtrl := admin_ai.NewTaskProfileController(aiSvcBiz)
		aiProviderCtrl := admin_ai.NewProviderController(aiSvcBiz)
		aiGroup := adminGroup.Group("/ai")
		aiGroup.GET("/services", aiSvcCtrl.ListServices)
		aiGroup.GET("/services/:id", aiSvcCtrl.GetService)
		aiGroup.POST("/services", aiSvcCtrl.CreateService)
		aiGroup.PUT("/services/:id", aiSvcCtrl.UpdateService)
		aiGroup.DELETE("/services/:id", aiSvcCtrl.DeprecateService)
		aiGroup.POST("/services/:id/restore", aiSvcCtrl.RestoreService)
		aiGroup.POST("/services/:id/validate-against/:task_id", aiTaskCtrl.ValidateAgainst)
		aiGroup.GET("/capability-schema", aiSvcCtrl.GetCapabilitySchema)
		aiGroup.GET("/tasks", aiTaskCtrl.ListTasks)
		aiGroup.GET("/tasks/:id", aiTaskCtrl.GetTask)
		aiGroup.PUT("/tasks/:id", aiTaskCtrl.UpdateTask)
		// Audit Logs (T1)
		auditCtrl := admin_ai.NewAuditLogController(aiSvcBiz)
		aiGroup.GET("/audit-logs", auditCtrl.ListLogs)
		// Route CRUD (T2)
		routeCtrl := admin_ai.NewRouteController(aiSvcBiz)
		aiGroup.POST("/services/:id/routes", routeCtrl.Create)
		aiGroup.PUT("/routes/:route_id", routeCtrl.Update)
		aiGroup.DELETE("/routes/:route_id", routeCtrl.Delete)
		aiGroup.POST("/routes/:route_id/toggle", routeCtrl.Toggle)
		// Provider CRUD (T4)
		aiGroup.GET("/providers", aiProviderCtrl.ListProviders)
		aiGroup.GET("/providers/:id", aiProviderCtrl.GetProvider)
		aiGroup.POST("/providers", aiProviderCtrl.CreateProvider)
		aiGroup.PUT("/providers/:id", aiProviderCtrl.UpdateProvider)
		aiGroup.DELETE("/providers/:id", aiProviderCtrl.DeleteProvider)
		aiGroup.POST("/providers/:id/test-connection", aiProviderCtrl.TestProviderConnection)
	}

	return nil
}
