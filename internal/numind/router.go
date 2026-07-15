package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/numind/biz/credit"
	customerbiz "numind-server/internal/numind/biz/customer"
	marketplacebiz "numind-server/internal/numind/biz/marketplace"
	"numind-server/internal/numind/biz/membership"
	skillbiz "numind-server/internal/numind/biz/skill"
	artifactbiz "numind-server/internal/numind/biz/skill/artifact"
	admincontroller "numind-server/internal/numind/controller/v1/admin"
	agentcontroller "numind-server/internal/numind/controller/v1/agent"
	"numind-server/internal/numind/controller/v1/ali"
	announcementcontroller "numind-server/internal/numind/controller/v1/announcement"
	chatbotcontroller "numind-server/internal/numind/controller/v1/chatbot"
	"numind-server/internal/numind/controller/v1/config"
	creditcontroller "numind-server/internal/numind/controller/v1/credit"
	customercontroller "numind-server/internal/numind/controller/v1/customer"
	documentcontroller "numind-server/internal/numind/controller/v1/document"
	feishucontroller "numind-server/internal/numind/controller/v1/feishu"
	llmcontroller "numind-server/internal/numind/controller/v1/llm"
	marketplacecontroller "numind-server/internal/numind/controller/v1/marketplace"
	meetingcontroller "numind-server/internal/numind/controller/v1/meeting"
	monitorcontroller "numind-server/internal/numind/controller/v1/monitor"
	ordercontroller "numind-server/internal/numind/controller/v1/order"
	parentbillingcontroller "numind-server/internal/numind/controller/v1/parent_billing"
	paymentcontroller "numind-server/internal/numind/controller/v1/payment"
	pdfcontroller "numind-server/internal/numind/controller/v1/pdf"
	"numind-server/internal/numind/controller/v1/salesrag"
	sopcontroller "numind-server/internal/numind/controller/v1/sop"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/controller/v1/user_billing"
	xhscontroller "numind-server/internal/numind/controller/v1/xhs"
	xhsscriptcontroller "numind-server/internal/numind/controller/v1/xhs_script"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"

	importMw "numind-server/internal/pkg/middleware"
)

// installNumindRouters 注册所有 Numind 工作台业务路由
func installNumindRouters(g *gin.Engine, b biz.IBiz) error {
	// 注册 404 Handler.
	g.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	})

	// 注册 /healthz handler.
	g.GET("/healthz", func(c *gin.Context) {
		log.C(c).Infow("Healthz function called")
		core.WriteResponse(c, nil, map[string]string{"status": "ok"})
	})

	// 注册 /healthz/ai handler (免鉴权).
	g.GET("/healthz/ai", aiservice.HealthzHandler)

	// 注册 pprof 路由
	pprof.Register(g)

	uc := user.New(store.S)
	alic := ali.New(b.Ali())
	salesRAGc := salesrag.NewSalesRAGController(b, b.Credit())

	// 初始化SOP控制器（用户端）
	userSopc := sopcontroller.NewSopController(b.Sop(), b.Ali(), b.Volc(), b.Credit(), b.LLMRouter())

	// 初始化PDF控制器
	pdfc := pdfcontroller.NewPdfController()

	// 初始化自助配置控制器（B端）
	kbCtrl := config.NewKnowledgeBaseController(b.KnowledgeBase())
	configChatbotCtrl := config.NewChatbotConfigController(b.Chatbot())
	configSopCtrl := config.NewSopConfigController(b.Sop())

	// 初始化C端智能体对话控制器
	chatbotCtrl := chatbotcontroller.NewChatbotController(b.Chatbot(), b.LLMRouter())

	// 初始化 LLM 模型与偏好控制器
	llmCtrl := llmcontroller.NewLLMController(b.LLMRouter())
	xhsScriptCtrl := xhsscriptcontroller.NewController(b.XhsScript(), b.Payment())

	v1Group := g.Group("/v1")

	// 登录接口不需要鉴权
	v1Group.POST("/web/login", uc.WebLogin)

	// 小红书视频口播稿仿写 MVP：产品私有 session，支持匿名试用 cookie + 插件 ext-token。
	{
		xhsScriptGroup := v1Group.Group("/xhs-script")
		{
			xhsScriptGroup.POST("/trial", xhsScriptCtrl.Trial)
			xhsScriptGroup.POST("/register", xhsScriptCtrl.Register)
			xhsScriptGroup.POST("/login", xhsScriptCtrl.Login)
			xhsScriptGroup.POST("/logout", xhsScriptCtrl.Logout)
			xhsScriptGroup.GET("/me", xhsScriptCtrl.Me)
			xhsScriptGroup.PUT("/profile", xhsScriptCtrl.SaveProfile)
			xhsScriptGroup.POST("/profile/extract-text", xhsScriptCtrl.ExtractProfileText)
			xhsScriptGroup.GET("/ext-token", xhsScriptCtrl.ExtToken)
			xhsScriptGroup.POST("/ext-token", xhsScriptCtrl.ExtToken)
			xhsScriptGroup.POST("/notes", xhsScriptCtrl.Ingest)
			xhsScriptGroup.GET("/notes", xhsScriptCtrl.ListNotes)
			xhsScriptGroup.GET("/notes/:id", xhsScriptCtrl.GetNote)
			xhsScriptGroup.POST("/notes/:id/transcribe", xhsScriptCtrl.Transcribe)
			xhsScriptGroup.POST("/notes/:id/generate", xhsScriptCtrl.Generate)
			xhsScriptGroup.GET("/quota", xhsScriptCtrl.GetQuota)
			xhsScriptGroup.POST("/orders", xhsScriptCtrl.CreateOrder)
			xhsScriptGroup.GET("/orders/:id/status", xhsScriptCtrl.GetOrderStatus)
			xhsScriptGroup.GET("/orders/:id", xhsScriptCtrl.GetOrderStatus)
			xhsScriptGroup.POST("/analytics/events", xhsScriptCtrl.TrackEvents)
		}
	}

	{
		xhsScriptAdminGroup := v1Group.Group("/admin/xhs-script")
		xhsScriptAdminGroup.Use(importMw.AdminAuthMiddleware())
		xhsScriptAdminGroup.GET("/analytics", xhsScriptCtrl.AdminAnalytics)
	}
	{
		xhsScriptProductAdminGroup := v1Group.Group("/xhs-script/admin")
		xhsScriptProductAdminGroup.Use(importMw.AdminAuthMiddleware())
		xhsScriptProductAdminGroup.GET("/metrics", xhsScriptCtrl.AdminAnalytics)
	}

	// 需要鉴权的接口
	authGroup := v1Group.Group("")
	authGroup.Use(importMw.AuthMiddleware())

	// 用户相关
	{
		authGroup.GET("/users/me", uc.GetCurrentUser)    // 获取当前用户信息
		authGroup.PUT("/users/me", uc.UpdateProfile)     // 更新当前用户个人信息
		authGroup.POST("/users/avatar", uc.UploadAvatar) // 上传用户头像
		authGroup.POST("/users/logout", uc.Logout)       // 用户登出
	}

	// 销售智能体 RAG 相关
	{
		// 权限检查（供前端 UI gating；子账号未授权时返回 has_permission:false，不被 gate 拦截）
		authGroup.GET("/sales-rag/check-permission", salesRAGc.CheckSalesPermission)

		// 知识库（文档管理）：所有登录用户可用，数据按 user_id 在 biz/store 层隔离。
		// 拆分自原 salesGroup（feature: salesrag-kb-public）—— 销售智能体权限不再
		// 控制"能否管文档"，仅控制"能否进行销售对话"。
		salesDocGroup := authGroup.Group("/sales-rag")
		salesDocGroup.POST("/ingest", salesRAGc.Ingest)                  // 上传并解析文档
		salesDocGroup.GET("/documents", salesRAGc.ListDocuments)         // 获取文档列表
		salesDocGroup.GET("/documents/:id", salesRAGc.GetDocument)       // 获取文档详情
		salesDocGroup.GET("/documents/:id/chunks", salesRAGc.ListChunks) // 获取文档切片列表
		salesDocGroup.PUT("/documents/:id", salesRAGc.UpdateDocument)    // 更新文档
		salesDocGroup.DELETE("/documents/:id", salesRAGc.DeleteDocument) // 删除文档

		// 销售对话相关（子账号须在 user_feature_permission 有 sales_agent 记录；父账号须在 sales_agent_owner 表）
		salesChatGroup := authGroup.Group("/sales-rag")
		salesChatGroup.Use(importMw.FeaturePermission(model.FeatureKeySalesAgent))

		// 观点库
		salesChatGroup.GET("/opinion-tracks", salesRAGc.ListOpinionTracks) // 获取系统内置观点赛道列表

		// 会话管理
		salesChatGroup.POST("/sessions", salesRAGc.CreateSession)           // 创建销售会话
		salesChatGroup.GET("/sessions", salesRAGc.ListSessions)             // 获取会话列表
		salesChatGroup.GET("/sessions/:id", salesRAGc.GetSession)           // 获取会话详情
		salesChatGroup.PUT("/sessions/:id", salesRAGc.UpdateSession)        // 更新会话信息
		salesChatGroup.DELETE("/sessions/:id", salesRAGc.DeleteSession)     // 删除会话
		salesChatGroup.PUT("/sessions/:id/pin", salesRAGc.PinSession)       // 置顶会话
		salesChatGroup.DELETE("/sessions/:id/pin", salesRAGc.UnpinSession)  // 取消置顶会话
		salesChatGroup.PUT("/sessions/:id/rename", salesRAGc.RenameSession) // 重命名会话

		// 消息管理
		salesChatGroup.POST("/sessions/:id/chat", salesRAGc.ChatWithSession)                         // 基于会话的销售对话（SSE流式）
		salesChatGroup.GET("/sessions/:id/messages", salesRAGc.ListMessages)                         // 获取会话消息列表
		salesChatGroup.POST("/sessions/:id/messages/:message_id/feedback", salesRAGc.SubmitFeedback) // 提交消息反馈（点赞/点踩）
		salesChatGroup.GET("/sessions/:id/messages/:message_id/feedback", salesRAGc.GetFeedback)     // 获取消息反馈

		// 客户档案管理
		salesChatGroup.PUT("/sessions/:id/customer-profile", salesRAGc.UpdateCustomerProfile) // 更新客户档案
		salesChatGroup.GET("/sessions/:id/customer-profile", salesRAGc.GetCustomerProfile)    // 获取客户档案
		salesChatGroup.POST("/analyze-profile", salesRAGc.AnalyzeProfile)                     // 解析文档生成客户档案
		salesChatGroup.POST("/analyze-profile-text", salesRAGc.AnalyzeProfileText)            // 纯文本分析生成客户档案

		// 聊天风格分析
		salesChatGroup.POST("/analyze-chat-style", salesRAGc.AnalyzeChatStyle) // 分析聊天风格（语言指纹）
		salesChatGroup.GET("/analyze-chat-style", salesRAGc.GetLanguageStyle)  // 获取已分析的聊天风格
		salesChatGroup.PUT("/analyze-chat-style", salesRAGc.SaveLanguageStyle) // 保存/更新语言风格
		salesChatGroup.POST("/ocr", salesRAGc.OCR)                             // OCR 识别图片
	}

	// 阿里云百炼相关
	{
		authGroup.POST("/ali/bailian/lease", alic.GetFileUploadLease) // 获取上传租约
		authGroup.POST("/ali/bailian/confirm", alic.AddFile)          // 确认上传并导入
		authGroup.POST("/ali/vision/analyze", alic.VisionAnalyze)     // 视觉理解 (Base64)
	}

	// 文档转文字相关（支持 PDF、Word、TXT、MD、RTF 等格式）
	{
		authGroup.POST("/pdf/convert-to-text", pdfc.ConvertToText) // 文档转文字（支持 .pdf, .txt, .md, .docx, .doc, .rtf）
		authGroup.POST("/files/extract-text", pdfc.ExtractText)    // 轻量文档转文本（无 run_id/node_id，不存储，支持 .pdf, .txt, .md, .docx, .doc）
	}

	// SOP相关（用户端接口）
	{
		// 用户执行SOP
		authGroup.GET("/sop/templates", userSopc.ListTemplates)                                // 获取可用模板列表
		authGroup.GET("/sop/templates/:id/nodes", userSopc.GetTemplateNodes)                   // 获取模板的所有节点
		authGroup.GET("/sop/templates/:id/check-permission", userSopc.CheckTemplatePermission) // 检查用户是否有模板权限
		authGroup.GET("/sop/templates/:id/bookmarks", userSopc.ListBookmarksByTemplate)        // 获取模板的所有书签
		authGroup.GET("/sop/templates/executed", userSopc.ListMyExecutedTemplates)             // 获取当前用户已执行的模板列表（按模板分组）
		authGroup.GET("/sop/templates/:id/runs", userSopc.ListTemplateRuns)                    // 获取指定模板下的所有历史运行记录（包含完整信息）
		// SOP 可见范围权限（sop-chatbot-visibility-scope spec §3.2/§3.3）
		authGroup.GET("/sop/templates/:id/visibility", userSopc.GetVisibility)    // 读 SOP 可见范围配置（父账户）
		authGroup.PUT("/sop/templates/:id/visibility", userSopc.UpdateVisibility) // 改 SOP 可见范围配置（父账户）

		// 书签管理
		authGroup.POST("/sop/bookmarks", userSopc.SaveBookmark)         // 保存节点为书签
		authGroup.GET("/sop/bookmarks/:id", userSopc.GetBookmark)       // 获取书签详情
		authGroup.DELETE("/sop/bookmarks/:id", userSopc.DeleteBookmark) // 删除书签

		// 逐步执行SOP节点（新增）- 注意：这些路由必须在 /sop/runs/:id 之前注册，避免路由冲突
		authGroup.POST("/sop/runs", userSopc.CreateRun)                                       // 创建Run（不立即执行，支持自动应用书签）
		authGroup.GET("/sop/runs/:id/next-node", userSopc.GetNextNode)                        // 获取下一个待执行节点
		authGroup.POST("/sop/runs/:id/nodes/:node_id/execute", userSopc.ExecuteNodeStream)    // 流式执行指定节点（支持文件上传）
		authGroup.POST("/sop/runs/:id/nodes/:node_id/apply-bookmark", userSopc.ApplyBookmark) // 应用书签到节点
		authGroup.DELETE("/sop/runs/:id/draft", userSopc.DeleteDraftRun)                      // 删除草稿状态的run（标准 fetch 走 Authorization header）
		// POST 路由不能放在 authGroup 内，因为 navigator.sendBeacon 无法设置 Authorization header。
		// AuthMiddleware 会在 token header 缺失时立即 c.Abort() + 401，导致 controller 里的
		// query token fallback 是 dead code。下方独立组使用 OptionalAuthMiddleware 让请求穿透到
		// controller，由 controller (bookmark.go:347-374) 自己处理 ?token=xxx query 兜底。
		// 详见 task 1 reviewer 发现 P0 + spec §5.1

		authGroup.POST("/sop/files/check-quality", userSopc.CheckFileQuality)      // 检测上传文件质量
		authGroup.POST("/sop/files/parse-text", userSopc.ParseFileText)            // 上传文件解析文本（返回文本用于回填）
		authGroup.POST("/sop/files/parse-text/query", userSopc.ParseFileTextQuery) // 轮询qwen-long解析结果
		authGroup.POST("/sop/images/read", userSopc.ReadImageWithQwenVL)           // 读取图片（qwen-vl-max）
		authGroup.POST("/sop/text/edit", userSopc.EditTextStream)                  // 文本编辑流式对话（不保存到数据库）
		authGroup.POST("/sop/chat/stream", userSopc.ChatAfterRunStream)            // Run完成后的对话流式接口
		authGroup.GET("/sop/runs/:id/chat-messages", userSopc.ListRunChatMessages) // 获取Run聊天记录
		authGroup.GET("/sop/runs/:id/status", userSopc.GetRunStatus)               // 获取Run执行状态

		authGroup.GET("/sop/runs/:id", userSopc.GetRun)                    // 查看执行记录
		authGroup.DELETE("/sop/runs/:id", userSopc.DeleteRun)              // 物理删除执行记录
		authGroup.POST("/sop/runs/batch/delete", userSopc.BatchDeleteRuns) // 批量删除执行记录
		authGroup.GET("/sop/runs/:id/detail", userSopc.GetRunDetail)       // 查看执行详情
		authGroup.GET("/sop/runs", userSopc.ListMyRuns)                    // 获取我的执行记录列表
		authGroup.GET("/sop/notes/:id", userSopc.GetNote)                  // 查看笔记详情
		authGroup.GET("/sop/notes", userSopc.ListMyNotes)                  // 获取我的笔记列表
	}

	// 用户消费查询
	{
		billingCtrl := user_billing.New(store.S)
		authGroup.GET("/billing/summary", billingCtrl.GetSummary)
		authGroup.GET("/billing/records", billingCtrl.ListRecords)
	}

	// 新 membership 系统（subscription/trial_grant/credit_cycle 表）
	membershipSvc := membership.NewMembershipService(store.S.DB())
	uc.WithMembershipSvc(membershipSvc)

	// 积分查询 + B2B2C 会员赋予（Task 10 / §5.1 + §5.7）
	creditCtrl := creditcontroller.New(
		b.Credit(),
		b.CreditService(),
		credit.NewPromptEstimator(store.S),
		store.S,
	).WithMembershipSvc(membershipSvc)
	{
		authGroup.GET("/credits/balance", creditCtrl.GetBalance)
		authGroup.POST("/credits/estimate", creditCtrl.Estimate)                 // Phase 2 T2.3：运行前估算（spec §3.11 + §4.3）
		authGroup.GET("/credits/consumption-log", creditCtrl.ListConsumptionLog) // 积分消耗流水（平账后真实记录）
		// T9: GET /v1/credits/packages deleted — credit_package dead route removed
	}

	// 订单管理（B 客户）
	{
		orderCtrl := ordercontroller.New(b.Payment(), store.S)
		authGroup.POST("/orders", importMw.RequireIdempotencyKey(), orderCtrl.CreateOrder)
		authGroup.GET("/orders", orderCtrl.ListOrders)
		authGroup.GET("/orders/:id", orderCtrl.GetOrder)
		authGroup.GET("/orders/:id/status", orderCtrl.GetOrderStatus) // Task 12 §5.8: booster polling
	}

	// 客户管理相关
	{
		customerBiz := customerbiz.New(store.S, customerbiz.WithMembershipSvc(membershipSvc))
		customerCtrl := customercontroller.NewCustomerController(customerBiz, b.Users())
		authGroup.POST("/customers", customerCtrl.Create)                                           // 创建子客户（注册）
		authGroup.GET("/customers/check-username", customerCtrl.CheckUsername)                      // 检查用户名是否可用
		authGroup.GET("/customers/statistics", customerCtrl.GetStatistics)                          // 获取客户统计数据
		authGroup.GET("/customers/sub-users", customerCtrl.ListSubUsers)                            // 获取二级客户列表
		authGroup.GET("/customers/sub-users/:user_id", customerCtrl.GetSubUserDetail)               // 获取二级客户详情
		authGroup.GET("/customers/sub-users/:user_id/templates", customerCtrl.ListSubUserTemplates) // 获取二级客户已授权模板
		authGroup.POST("/customers/sub-users/:user_id/templates", customerCtrl.GrantTemplates)      // 为二级客户授权模板
		authGroup.POST("/customers/batch/grant-templates", customerCtrl.BatchGrantTemplates)        // 批量为多个二级客户授权模板
		authGroup.POST("/customers/batch/revoke-templates", customerCtrl.BatchRevokeTemplates)      // 批量为多个二级客户撤销模板权限
		authGroup.DELETE("/customers/sub-users/:user_id/templates", customerCtrl.RevokeTemplates)   // 撤销二级客户模板权限

		// Chatbot 权限管理（与模板权限对称）
		authGroup.GET("/customers/sub-users/:user_id/chatbots", customerCtrl.ListSubUserChatbots) // 获取二级客户已授权 chatbot
		authGroup.POST("/customers/sub-users/:user_id/chatbots", customerCtrl.GrantChatbots)      // 为二级客户授权 chatbot
		authGroup.DELETE("/customers/sub-users/:user_id/chatbots", customerCtrl.RevokeChatbots)   // 撤销二级客户 chatbot 权限
		authGroup.POST("/customers/batch/grant-chatbots", customerCtrl.BatchGrantChatbots)        // 批量为多个二级客户授权 chatbot
		authGroup.POST("/customers/batch/revoke-chatbots", customerCtrl.BatchRevokeChatbots)      // 批量为多个二级客户撤销 chatbot 权限

		// 功能权限管理
		authGroup.GET("/customers/sub-users/:user_id/features", customerCtrl.ListSubUserFeatures)
		authGroup.POST("/customers/sub-users/:user_id/features", customerCtrl.GrantFeatures)
		authGroup.DELETE("/customers/sub-users/:user_id/features", customerCtrl.RevokeFeatures)
	}

	// 通知中心（公告/问卷）用户端（notification-center T4a）。
	// 整组挂 feature flag：flag off 时所有路由返回 ErrFeatureDisabled（spec §2）。
	// AuthMiddleware 由 authGroup 继承。静态路由 /unread-count 注册在 /:id 之前——
	// gin v1（httprouter）同层级静态优先于 :param，故二者不冲突。
	{
		annCtrl := announcementcontroller.NewUserController(b.Announcement())
		annGroup := authGroup.Group("/announcements")
		annGroup.Use(importMw.FeatureFlag("features.notification_center.enabled"))
		{
			annGroup.GET("", annCtrl.ListAnnouncements)               // 公告列表（含 unread_count）
			annGroup.GET("/unread-count", annCtrl.UnreadCount)        // 未读数（铃铛轮询）
			annGroup.GET("/:id", annCtrl.GetDetail)                   // 公告详情（含问卷题目）
			annGroup.POST("/:id/read", annCtrl.MarkRead)              // 标记已读（幂等）
			annGroup.POST("/:id/survey/submit", annCtrl.SubmitSurvey) // 提交问卷答卷
		}
	}

	// 文档系统 v1（document-system）：agent 生成产物的打开/编辑/保存/导出。
	// 整组套 FeatureFlag —— flag off（prod 默认）时所有路由返回 ErrFeatureDisabled(404)。
	{
		docCtrl := documentcontroller.NewController(b.Document())
		docGroup := authGroup.Group("/documents")
		docGroup.Use(importMw.FeatureFlag("features.document_system.enabled"))
		{
			docGroup.POST("/open", docCtrl.Open)        // 打开/懒建档
			docGroup.GET("/:id", docCtrl.Get)           // 取文档（重开）
			docGroup.PUT("/:id", docCtrl.Save)          // 自动保存
			docGroup.GET("/:id/export", docCtrl.Export) // 导出下载 ?format=md|pdf|docx
		}
	}

	// 会议副驾（meeting-copilot）：全新独立模式、内部试用先行、代码高度自包含可整体删除。
	// 整组套 FeatureFlag —— flag off（prod 默认）时所有路由返回 ErrFeatureDisabled(404)。
	// AuthMiddleware 由 authGroup 继承。注意路由注册顺序：静态 /presets 在 /:id 之前注册——
	// gin v1（httprouter）同层级静态优先于 :param，二者不冲突（与通知中心 /unread-count 同理）。
	{
		meetingCtrl := meetingcontroller.NewController(b.Meeting())
		meetingGroup := authGroup.Group("/meetings")
		meetingGroup.Use(importMw.FeatureFlag("features.meeting_copilot.enabled"))
		{
			// 预设（静态路径，先于 /:id 注册）。
			meetingGroup.GET("/presets", meetingCtrl.ListPresets)         // 当前用户预设 + 内置
			meetingGroup.POST("/presets", meetingCtrl.SavePreset)         // 存预设
			meetingGroup.DELETE("/presets/:id", meetingCtrl.DeletePreset) // 删预设（仅本人、非 builtin）

			// 会话生命周期。
			meetingGroup.POST("", meetingCtrl.CreateSession) // 创建会话
			meetingGroup.GET("", meetingCtrl.ListSessions)   // 分页列表
			meetingGroup.GET("/:id", meetingCtrl.GetSession) // 详情（含 segments + feedbacks）

			// 分段转写 + 反馈（SSE）+ 结束。
			meetingGroup.POST("/:id/segments", meetingCtrl.IngestSegment)    // 分段近实时转写（multipart，旧路径保留）
			meetingGroup.POST("/:id/recording", meetingCtrl.UploadRecording) // 整场录音上传（实时流式路径，SPEC §3）
			meetingGroup.POST("/:id/feedback", meetingCtrl.GenerateFeedback) // 反馈（SSE）
			meetingGroup.POST("/:id/end", meetingCtrl.EndSession)            // 结束会话（可选 body {generate_summary}，缺省 true）
		}

		// 实时流式 ASR ws 端点（SPEC §2）：浏览器 ws 无法带 Authorization 头，故**不**挂
		// AuthMiddleware（鉴权在 handler 内用 ?token= 完成），仅套 FeatureFlag（off→404）。
		// 与 meetingGroup 平级注册到 v1Group，避免继承 authGroup 的 AuthMiddleware。
		wsGroup := v1Group.Group("/meetings")
		wsGroup.Use(importMw.FeatureFlag("features.meeting_copilot.enabled"))
		{
			wsGroup.GET("/:id/asr-stream", meetingCtrl.AsrStream) // 实时流式转写（websocket，GET 升级）
		}
	}

	// 小红书选题采集（xhs-collector）：浏览器插件批量上送笔记 payload 落入用户私有累积选题库。
	// AuthMiddleware 由 authGroup 继承；user_id 从鉴权上下文取，保证多租户归属不可伪造。
	{
		xhsCtrl := xhscontroller.NewController(b.Xhs(), b.Users())
		xhsGroup := authGroup.Group("/xhs")
		{
			xhsGroup.GET("/ext-token", xhsCtrl.ExtToken)   // 换发 scope=xhs 受限 token 给浏览器插件（一键授权，不扣分）
			xhsGroup.POST("/notes", xhsCtrl.Ingest)        // 批量摄入插件采集的笔记（去重 upsert，置 pending）
			xhsGroup.GET("/notes", xhsCtrl.List)           // 分页查询当前用户选题库（note_type/keyword/enrich_status/sort 过滤）
			xhsGroup.POST("/notes/export", xhsCtrl.Export) // 导出选中笔记为 CSV（≤200 条，COS + 1h 签名链接，不扣分）
			xhsGroup.GET("/notes/:id", xhsCtrl.Get)        // 单条选题笔记详情（user 隔离）
			xhsGroup.DELETE("/notes/:id", xhsCtrl.Delete)  // 删除单条选题笔记（user 隔离）
		}
	}
	// 飞书个人工作空间：所有接口从 authGroup 取 user_id；浏览器不能提交
	// argv/scopes/app/user。live authorization URL 只由 connect/refresh 返回，
	// status 永远是纯读。flag/完整 composition 任一失败则不注册整个组。
	if feishuSvc := b.FeishuSvc(); feishuSvc != nil {
		feishuCtrl := feishucontroller.NewController(feishuSvc)

		feishuAuthGroup := authGroup.Group("/feishu")
		feishuAuthGroup.Use(importMw.FeatureFlag("features.feishu_integration.enabled"))
		{
			feishuAuthGroup.GET("/status", feishuCtrl.Status)
			feishuAuthGroup.POST("/connect", feishuCtrl.Connect)
			feishuAuthGroup.POST("/operations/:id/resume", feishuCtrl.ResumeOperation)
			feishuAuthGroup.POST("/actions/:session_id/refresh", feishuCtrl.RefreshAction)
			feishuAuthGroup.DELETE("/connection", feishuCtrl.Unbind)
		}
	}

	// B2B2C 会员赋予（Q1 / Task 10）：父账户为子账户开通会员，不走支付流程
	// creditCtrl.GrantMembership 使用新 membership.MembershipService 路径（§5.1 + §5.7）
	{
		// 子账户列表别名（前端 /v1/users/children，Q2 新增），复用 CustomerController.ListSubUsers
		// Why: b.Customers() 不注入 membershipSvc，会让 ListSubUsers 走旧 credit_package 路径
		// 与 line 238 主路径产生数据分歧。这里复用同样的构造方式，保持读路径一致。
		childListBiz := customerbiz.New(store.S, customerbiz.WithMembershipSvc(membershipSvc))
		childListCtrl := customercontroller.NewCustomerController(childListBiz, b.Users())
		authGroup.GET("/users/children", childListCtrl.ListSubUsers)
		// Task 12 §5.4: 父账户查子账户余额（不含 booster，隐私保护）
		authGroup.GET("/users/children/:child_id/balance", creditCtrl.GetChildBalance)
		authGroup.POST("/users/children/:child_id/grant-membership",
			importMw.RequireIdempotencyKey(),
			creditCtrl.GrantMembership)

		// 父账户自助费用对账（parent-billing-report）。
		// 父账户校验在 biz 层（GetBillingReportForParent → ErrNotParentAccount → 403），
		// 非中间件，故此处无 ParentUserOnly()。越权隔离由 biz SQL granter 过滤保证。
		parentBillingCtrl := parentbillingcontroller.New(b2b_billing.New(store.S))
		authGroup.GET("/users/me/billing-report", parentBillingCtrl.GetMyBillingReport)
	}

	// 自助配置中心（B端，需要主账号 + 功能权限）
	{
		configGroup := authGroup.Group("/config")
		configGroup.Use(importMw.ParentUserOnly(), importMw.FeaturePermission(model.FeatureKeySelfServiceConfig))
		{
			// 知识库管理
			configGroup.POST("/knowledge-bases", kbCtrl.Create)
			configGroup.GET("/knowledge-bases", kbCtrl.List)
			configGroup.GET("/knowledge-bases/:id", kbCtrl.Get)
			configGroup.PUT("/knowledge-bases/:id", kbCtrl.Update)
			configGroup.DELETE("/knowledge-bases/:id", kbCtrl.Delete)
			configGroup.POST("/knowledge-bases/:id/documents", kbCtrl.UploadDocuments)
			configGroup.DELETE("/knowledge-bases/:id/documents/:docId", kbCtrl.RemoveDocument)

			// 智能体配置
			configGroup.POST("/chatbots", configChatbotCtrl.Create)
			configGroup.GET("/chatbots", configChatbotCtrl.List)
			configGroup.GET("/chatbots/:id", configChatbotCtrl.Get)
			configGroup.PUT("/chatbots/:id", configChatbotCtrl.Update)
			configGroup.DELETE("/chatbots/:id", configChatbotCtrl.Delete)
			configGroup.PUT("/chatbots/:id/status", configChatbotCtrl.UpdateStatus)

			// SOP 模板配置
			configGroup.POST("/sop-templates", configSopCtrl.Create)
			configGroup.GET("/sop-templates", configSopCtrl.List)
			configGroup.GET("/sop-templates/:id", configSopCtrl.Get)
			configGroup.PUT("/sop-templates/:id", configSopCtrl.Update)
			configGroup.DELETE("/sop-templates/:id", configSopCtrl.Delete)
			configGroup.PUT("/sop-templates/:id/status", configSopCtrl.UpdateStatus)
			configGroup.POST("/sop-templates/:id/nodes", configSopCtrl.CreateNode)
			configGroup.PUT("/sop-templates/:id/nodes/batch-sort", configSopCtrl.BatchSortNodes) // 必须在 :nodeId 之前注册
			configGroup.PUT("/sop-templates/:id/nodes/:nodeId", configSopCtrl.UpdateNode)
			configGroup.DELETE("/sop-templates/:id/nodes/:nodeId", configSopCtrl.DeleteNode)
		}
	}

	// C端智能体对话
	{
		chatbotGroup := authGroup.Group("/chatbot")
		{
			chatbotGroup.GET("/list", chatbotCtrl.List)
			chatbotGroup.GET("/:id/check-permission", chatbotCtrl.CheckPermission) // 前端点击前权限预检（mirror SOP /check-permission）
			// Chatbot 可见范围权限（sop-chatbot-visibility-scope spec §3.4）
			chatbotGroup.GET("/:id/visibility", chatbotCtrl.GetVisibility)    // 读 chatbot 可见范围配置（父账户）
			chatbotGroup.PUT("/:id/visibility", chatbotCtrl.UpdateVisibility) // 改 chatbot 可见范围配置（父账户）
			chatbotGroup.POST("/sessions", chatbotCtrl.CreateSession)
			chatbotGroup.GET("/sessions", chatbotCtrl.ListSessions)
			chatbotGroup.DELETE("/sessions/:id", chatbotCtrl.DeleteSession)
			chatbotGroup.GET("/sessions/:id/messages", chatbotCtrl.ListMessages)
			chatbotGroup.POST("/sessions/:id/chat", chatbotCtrl.Chat)
			chatbotGroup.POST("/sessions/:id/title", chatbotCtrl.GenerateTitle) // instant-title-ux: 发送时即时生成标题
			chatbotGroup.PUT("/sessions/:id/rename", chatbotCtrl.RenameSession) // 重命名会话
			chatbotGroup.PUT("/sessions/:id/pin", chatbotCtrl.PinSession)       // 置顶/取消置顶会话
		}
	}

	// LLM 模型列表与用户偏好
	{
		llmGroup := authGroup.Group("/llm")
		{
			llmGroup.GET("/models", llmCtrl.ListModels)
			llmGroup.GET("/preference", llmCtrl.GetPreference)
			llmGroup.PUT("/preference", llmCtrl.SavePreference)
		}
	}

	// 博主内容监控
	{
		monitorCtrl := monitorcontroller.NewMonitorController(b.Monitor(), store.S)

		// 权限检查（不需要功能权限中间件，供前端查询权限状态）
		authGroup.GET("/monitor/check-permission", monitorCtrl.CheckPermission)

		// 以下所有监控路由需要功能权限检查
		monitorGroup := authGroup.Group("/monitor")
		monitorGroup.Use(importMw.FeaturePermission(model.FeatureKeyContentMonitor))

		monitorGroup.POST("/bloggers", monitorCtrl.AddBlogger)
		monitorGroup.GET("/bloggers", monitorCtrl.ListBloggers)
		monitorGroup.GET("/bloggers/:id", monitorCtrl.GetBlogger)
		monitorGroup.PUT("/bloggers/:id", monitorCtrl.UpdateBlogger)
		monitorGroup.DELETE("/bloggers/:id", monitorCtrl.DeleteBlogger)
		monitorGroup.POST("/bloggers/:id/check", monitorCtrl.CheckBlogger)
		monitorGroup.POST("/check-batch", monitorCtrl.CheckBatch)
		monitorGroup.GET("/notes", monitorCtrl.ListNotes)
		monitorGroup.GET("/notes/:id", monitorCtrl.GetNote)
		monitorGroup.POST("/notes/:id/analyze", monitorCtrl.AnalyzeNote)
		monitorGroup.GET("/briefings", monitorCtrl.ListBriefings)
		monitorGroup.GET("/briefings/:id", monitorCtrl.GetBriefing)
		monitorGroup.POST("/briefings/generate", monitorCtrl.GenerateBriefing)
		monitorGroup.GET("/config", monitorCtrl.GetConfig)
		monitorGroup.PUT("/config", monitorCtrl.UpdateConfig)
		monitorGroup.GET("/stats", monitorCtrl.GetStats)

		// XHS account binding (QR code login)
		monitorGroup.POST("/xhs/qr/create", monitorCtrl.CreateXhsQR)
		monitorGroup.GET("/xhs/qr/status/:qr_id", monitorCtrl.CheckXhsQRStatus)
		monitorGroup.POST("/xhs/qr/complete/:qr_id", monitorCtrl.CompleteXhsQR)
		monitorGroup.GET("/xhs/bind-status", monitorCtrl.GetXhsBindStatus)
		monitorGroup.POST("/xhs/unbind", monitorCtrl.UnbindXhs)
	}

	// Agent 技能系统（#5/14 agent-mode-skill-system）
	// 全部走 AuthMiddleware（authGroup），父账户专属（子账户在 biz 层返回 403）。
	{
		skillSvc := skillbiz.NewService(store.S)
		skillCtrl := agentcontroller.NewSkillController(skillSvc)
		agentGroup := authGroup.Group("/agent")
		{
			skills := agentGroup.Group("/skills")
			{
				skills.POST("", skillCtrl.Create)
				skills.GET("", skillCtrl.List)
				skills.GET("/:id", skillCtrl.Get)
				skills.PATCH("/:id", skillCtrl.Patch)
				skills.DELETE("/:id", skillCtrl.Delete)
				skills.GET("/:id/history", skillCtrl.ListHistory)
				skills.POST("/:id/restore/:version", skillCtrl.Restore)
			}
			agentGroup.GET("/skill-templates", skillCtrl.ListTemplates)
		}
	}

	// Skill artifact 系统 v2（agent-mode-v2-skill-as-artifact, spec §4）
	//   - /v1/skills/*          独立 Skill 资产 CRUD + 历史/回滚
	//   - /v1/agents/:id/skills/*  Agent ↔ Skill 装载关系
	// 全部 user_token middleware；父账户专属，子账户在 controller 顶部 403。
	// 与上方 /v1/agent/skills/*（v1 内嵌式）并存，v2 #2 接管 runtime 后才下线 v1。
	{
		skillArtifactSvc := artifactbiz.NewService(store.S.DB())
		skillBindingSvc := artifactbiz.NewBindingService(store.S.DB())
		artifactCtrl := agentcontroller.NewSkillArtifactController(skillArtifactSvc, skillBindingSvc)

		skillsV2 := authGroup.Group("/skills")
		{
			skillsV2.POST("", artifactCtrl.CreateSkill)
			skillsV2.POST("/import-template", artifactCtrl.ImportTemplate)
			skillsV2.GET("", artifactCtrl.ListSkills)
			skillsV2.GET("/:id", artifactCtrl.GetSkill)
			skillsV2.PUT("/:id", artifactCtrl.UpdateSkill)
			skillsV2.DELETE("/:id", artifactCtrl.DeleteSkill)
			skillsV2.GET("/:id/history", artifactCtrl.ListSkillHistory)
			skillsV2.POST("/:id/restore/:version", artifactCtrl.RestoreSkill)
			skillsV2.GET("/:id/agents", artifactCtrl.ListSkillBoundAgents)
		}

		agentsV2 := authGroup.Group("/agents/:id/skills")
		{
			agentsV2.GET("", artifactCtrl.ListAgentSkills)
			agentsV2.POST("", artifactCtrl.AttachSkill)
			agentsV2.DELETE("/:skill_id", artifactCtrl.DetachSkill)
			agentsV2.PUT("/reorder", artifactCtrl.ReorderSkills)
		}
	}

	// Skill marketplace 系统 v2 (agent-mode-v2-skill-marketplace, spec §6.1)
	//   - /v1/marketplace/* — 跨租户 Skill 发布/浏览/订阅
	// 全部 user_token middleware；父账户专属（biz 层 verifyParent 拦子账户 → 403）。
	// 依赖 #1 artifact biz（订阅时 clone skill 到订阅方租户）。
	{
		mpSvc := marketplacebiz.NewService(
			store.S.Marketplaces(),
			artifactbiz.NewService(store.S.DB()),
			store.S.Users(),
			store.S.DB(),
		)
		mpCtrl := marketplacecontroller.NewController(mpSvc)

		mp := authGroup.Group("/marketplace")
		{
			mp.POST("/sanitize-preview", mpCtrl.SanitizePreview)
			mp.POST("/publish", mpCtrl.Publish)
			mp.GET("/list", mpCtrl.List)
			// /my-subscriptions MUST be registered BEFORE /:id so Gin doesn't
			// capture it as the id path parameter (spec §6.1 Gin path order).
			mp.GET("/my-subscriptions", mpCtrl.ListMySubscriptions)
			mp.GET("/:id", mpCtrl.Get)
			mp.POST("/:id/unpublish", mpCtrl.Unpublish)
			mp.POST("/:id/subscribe", mpCtrl.Subscribe)
			mp.DELETE("/:id/unsubscribe", mpCtrl.Unsubscribe)
		}
	}

	// #14 follow-up ALPHA: student-facing agent endpoints (7 GET + 1 POST).
	// Bridges web-v3 to backend; routes sit under authGroup (user_token middleware).
	agentcontroller.RegisterStudentQueryRoutes(authGroup, skillbiz.NewService(store.S), b.StudentQuery())

	// #14 BETA student-facing run lifecycle endpoints
	// V1.5 task 1.2 (P1 #1 fix): controller calls biz.AttachmentFallback().GetStatusForUser
	// instead of store directly — no longer pass the store here.
	agentcontroller.RegisterStudentRunRoutes(authGroup, b)

	// Task 3.5 (agent-mode-v15-memory-layer-a): FULLTEXT 中文消息搜索
	// GET /v1/agent-runs/search — user_token middleware; 跨 user 严格隔离在 store 层 WHERE。
	agentcontroller.RegisterAgentSearchRoutes(authGroup, b.SearchService())

	// 支付回调（无需鉴权）
	{
		paymentCtrl := paymentcontroller.New(b.Payment())
		v1Group.POST("/payment/wechat/notify", paymentCtrl.WechatNotify)
		v1Group.POST("/payment/alipay/notify", paymentCtrl.AlipayNotify)

		// Existing payment configs and reverse proxies may publish callbacks under
		// /api/v1. Keep both forms live so async payment fulfillment is not lost.
		apiV1Group := g.Group("/api/v1")
		apiV1Group.POST("/payment/wechat/notify", paymentCtrl.WechatNotify)
		apiV1Group.POST("/payment/alipay/notify", paymentCtrl.AlipayNotify)
	}

	// Beacon 路由组（OptionalAuthMiddleware，让 controller 自己处理 query token）
	// navigator.sendBeacon 无法设置 Authorization header，必须通过 query 参数传 token。
	// AuthMiddleware 在 token header 缺失时会立即 401 abort，所以这些路由必须独立出来。
	// controller (bookmark.go DeleteDraftRun) 会从 ?token=xxx query 提取 token 并校验。
	{
		beaconGroup := v1Group.Group("")
		beaconGroup.Use(importMw.OptionalAuthMiddleware())
		beaconGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun) // Beacon 方式删除草稿
	}

	// RAG Eval Harness — admin-gated 检索调试端点（rag-eval-harness）。
	// 刻意注册在【用户服务】而非 admin 服务：检索栈需要 AI gateway + 挂载的 sqlite-vec 卷
	// （/opt/numind/dev/sales_vector.db），二者都只在用户服务进程/容器里存在；admin 服务既无
	// gateway（历史纯管理 API）也未挂该卷。这里复用的正是生产 chatbot 同一个 retrieve.Service
	// （b.RagRetrieve() == chatbotRetrieve），评估即真实链路。用 AdminAuthMiddleware 守卫
	// （admin token + IsAdmin），仍是 admin-only 工具，不对普通用户开放。路径保持
	// /v1/admin/rag-eval/retrieve 不变（仅端口从 9099 改为 9091）。
	// feature flag features.rag_eval.enabled：dev 开、prod 默认 OFF（不把 debug 端点暴露上 prod）。
	{
		ragEvalGroup := v1Group.Group("/admin/rag-eval")
		ragEvalGroup.Use(importMw.FeatureFlag("features.rag_eval.enabled"), importMw.AdminAuthMiddleware())
		evalCtl := admincontroller.NewRAGEvalController(b.RagRetrieve())
		ragEvalGroup.POST("/retrieve", evalCtl.Retrieve)
	}

	// Chunker admin endpoints — 结构感知切块器调试工具（RAG upgrade item 1）。
	// 刻意注册在【用户服务】而非 admin 服务：切块/向量化管道（AI gateway + sqlite-vec 卷）
	// 只在用户服务进程里存在。复用 features.rag_eval.enabled 旗标（dev 开、prod 默认 OFF）
	// + AdminAuthMiddleware 守卫，不对普通用户开放。
	{
		chunkerGroup := v1Group.Group("/admin/chunker")
		chunkerGroup.Use(importMw.FeatureFlag("features.rag_eval.enabled"), importMw.AdminAuthMiddleware())
		chunkerCtl := admincontroller.NewChunkerController(b.SalesRAG())
		chunkerGroup.POST("/preview", chunkerCtl.Preview)
		chunkerGroup.POST("/reindex", chunkerCtl.Reindex)
	}

	return nil
}
