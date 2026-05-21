package numind

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz"
	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/numind/biz/compliance"
	bizcb "numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/controller/v1/admin"
	"numind-server/internal/numind/controller/v1/admin_ai"
	"numind-server/internal/numind/controller/v1/admin_b2b"
	"numind-server/internal/numind/controller/v1/admin_billing"
	"numind-server/internal/numind/controller/v1/admin_contextbudget"
	"numind-server/internal/numind/controller/v1/admin_credit"
	"numind-server/internal/numind/controller/v1/admin_dashboard"
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
	adminMembershipSvc := membership.NewMembershipService(store.S.DB())
	adminCreditWithMembership := admin_credit.NewWithMembership(adminCreditCtrl, adminMembershipSvc)

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
		adminGroup.POST("/users/:id/reset-password", userCtrl.ResetPassword)
		// Task 12 §5.3: admin 查任意用户余额（含 booster 字段）
		// 用 :id 而非 :user_id 以避免 gin router 报错（同一前缀 /users/ 下不允许混用路径参数名）。
		adminGroup.GET("/users/:id/balance", adminCreditWithMembership.GetUserBalance)
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
		adminGroup.GET("/credits/user-types", adminCreditCtrl.ListUserTypeConfigs)
		adminGroup.PUT("/credits/user-types/:user_type", adminCreditCtrl.UpdateUserTypeConfig)
	}

	// Phase 2 T2.3: estimation-coefficient CRUD (spec §3.11 + §4.1.2)
	{
		adminGroup.GET("/estimation-coefficients", coefficientCtrl.ListCoefficients)
		adminGroup.GET("/estimation-coefficients/history", coefficientCtrl.ListCoefficientHistory)
		adminGroup.POST("/estimation-coefficients", coefficientCtrl.CreateCoefficient)
		adminGroup.PUT("/estimation-coefficients/:id", coefficientCtrl.UpdateCoefficient)
		adminGroup.DELETE("/estimation-coefficients/:id", coefficientCtrl.DeleteCoefficient)
	}

	// 订单管理
	{
		adminOrderCtrl := admin_order.New(store.S)
		adminGroup.GET("/orders", adminOrderCtrl.ListOrders)
		adminGroup.GET("/orders/:id", adminOrderCtrl.GetOrder)
	}

	// B2B 月度结算报表（Q1.4）: 父账户"帮开通"的 credit_package 按月聚合
	// Task 13 / T9: cutover-date wiring retained; chooseSource always returns new_only post-T9.
	{
		var b2bBillingBiz b2b_billing.IB2BBillingBiz
		cutoverStr := viper.GetString("billing.b2b_cutover_date")
		if cutoverStr != "" {
			if cutover, parseErr := time.Parse("2006-01-02", cutoverStr); parseErr == nil {
				b2bBillingBiz = b2b_billing.NewWithCutover(store.S, cutover.UTC())
				log.Infow("B2B billing cutover date set", "cutover", cutoverStr)
			} else {
				log.Warnw("B2B billing cutover date parse failed, defaulting to new_only (T9)", "raw", cutoverStr, "err", parseErr)
				b2bBillingBiz = b2b_billing.New(store.S)
			}
		} else {
			b2bBillingBiz = b2b_billing.New(store.S)
		}
		adminB2BCtrl := admin_b2b.New(b2bBillingBiz)
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
		aiGroup.POST("/services-with-route", aiSvcCtrl.CreateServiceWithRoute)
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

	// Compliance Rule Admin — M-C1a CRUD (5 endpoints)
	{
		complianceCache := compliance.NewTTLCache(compliance.DefaultCacheCap, compliance.DefaultCacheTTL)
		complianceAdminSvc := compliance.NewAdminService(store.S.Compliance(), complianceCache)
		complianceRuleCtl := admin.NewComplianceRuleController(complianceAdminSvc)
		crGroup := adminGroup.Group("/compliance-rules")
		crGroup.GET("", complianceRuleCtl.List)
		crGroup.POST("", complianceRuleCtl.Create)
		crGroup.GET("/:id", complianceRuleCtl.Get)
		crGroup.PATCH("/:id", complianceRuleCtl.Patch)
		crGroup.DELETE("/:id", complianceRuleCtl.Delete)
	}

	// Agent Run Admin — M-C3b force-cancel + M-C4a listing (2 endpoints)
	{
		agentAdminSvc := agentbiz.NewAgentAdminService(store.S.AgentRuns(), b.Agents())
		agentRunCtl := admin.NewAgentRunController(agentAdminSvc)
		arGroup := adminGroup.Group("/agent-runs")
		arGroup.GET("", agentRunCtl.List)
		arGroup.POST("/:id/cancel", agentRunCtl.Cancel)
	}

	// Context Budget Admin — token profiles, policies, preview, events (Task 11)
	{
		cbStore := store.NewContextBudgetStore(store.S.DB())
		cbBiz := bizcb.New(cbStore, bizcb.Options{})
		cbReg := registry.New(store.S.DB())
		cbAiSvcBiz := aiservice_admin.New(cbReg, store.S.DB())
		cbCtrl := admin_contextbudget.New(cbBiz, cbStore, cbAiSvcBiz, store.S.DB())
		adminGroup.GET("/context-budget/token-profiles", cbCtrl.ListTokenProfiles)
		adminGroup.POST("/context-budget/token-profiles", cbCtrl.CreateTokenProfile)
		adminGroup.PUT("/context-budget/token-profiles/:id", cbCtrl.UpdateTokenProfile)
		adminGroup.DELETE("/context-budget/token-profiles/:id", cbCtrl.DeleteTokenProfile)
		adminGroup.GET("/context-budget/token-profiles/history", cbCtrl.GetTokenProfileHistory)
		adminGroup.GET("/context-budget/policies", cbCtrl.ListPolicies)
		adminGroup.PUT("/context-budget/policies/:operation", cbCtrl.UpsertPolicy)
		adminGroup.POST("/context-budget/preview", cbCtrl.Preview)
		adminGroup.GET("/context-budget/events", cbCtrl.ListEvents)
	}

	return nil
}
