package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/controller/v1/ali"
	chatbotcontroller "numind-server/internal/numind/controller/v1/chatbot"
	"numind-server/internal/numind/controller/v1/config"
	creditcontroller "numind-server/internal/numind/controller/v1/credit"
	customercontroller "numind-server/internal/numind/controller/v1/customer"
	llmcontroller "numind-server/internal/numind/controller/v1/llm"
	monitorcontroller "numind-server/internal/numind/controller/v1/monitor"
	ordercontroller "numind-server/internal/numind/controller/v1/order"
	paymentcontroller "numind-server/internal/numind/controller/v1/payment"
	pdfcontroller "numind-server/internal/numind/controller/v1/pdf"
	"numind-server/internal/numind/controller/v1/salesrag"
	sopcontroller "numind-server/internal/numind/controller/v1/sop"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/controller/v1/user_billing"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"

	importMw "numind-server/internal/pkg/middleware"
)

// installNumindRouters 注册所有 Numind 工作台业务路由
func installNumindRouters(g *gin.Engine) error {
	// 注册 404 Handler.
	g.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	})

	// 注册 /healthz handler.
	g.GET("/healthz", func(c *gin.Context) {
		log.C(c).Infow("Healthz function called")
		core.WriteResponse(c, nil, map[string]string{"status": "ok"})
	})

	// 注册 pprof 路由
	pprof.Register(g)

	uc := user.New(store.S)
	b := biz.NewBiz(store.S)
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

	v1Group := g.Group("/v1")

	// 登录接口不需要鉴权
	v1Group.POST("/web/login", uc.WebLogin)

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
		// 权限检查（不需要功能权限中间件，供前端查询权限状态）
		authGroup.GET("/sales-rag/check-permission", salesRAGc.CheckSalesPermission)

		// 以下所有销售智能体路由需要功能权限检查
		salesGroup := authGroup.Group("/sales-rag")
		salesGroup.Use(importMw.FeaturePermission(model.FeatureKeySalesAgent))

		// 文档管理
		salesGroup.POST("/ingest", salesRAGc.Ingest)                  // 上传并解析文档
		salesGroup.GET("/documents", salesRAGc.ListDocuments)         // 获取文档列表
		salesGroup.GET("/documents/:id", salesRAGc.GetDocument)       // 获取文档详情
		salesGroup.GET("/documents/:id/chunks", salesRAGc.ListChunks) // 获取文档切片列表
		salesGroup.PUT("/documents/:id", salesRAGc.UpdateDocument)    // 更新文档
		salesGroup.DELETE("/documents/:id", salesRAGc.DeleteDocument) // 删除文档

		// 观点库
		salesGroup.GET("/opinion-tracks", salesRAGc.ListOpinionTracks) // 获取系统内置观点赛道列表

		// 会话管理
		salesGroup.POST("/sessions", salesRAGc.CreateSession)           // 创建销售会话
		salesGroup.GET("/sessions", salesRAGc.ListSessions)             // 获取会话列表
		salesGroup.GET("/sessions/:id", salesRAGc.GetSession)           // 获取会话详情
		salesGroup.PUT("/sessions/:id", salesRAGc.UpdateSession)        // 更新会话信息
		salesGroup.DELETE("/sessions/:id", salesRAGc.DeleteSession)     // 删除会话
		salesGroup.PUT("/sessions/:id/pin", salesRAGc.PinSession)       // 置顶会话
		salesGroup.DELETE("/sessions/:id/pin", salesRAGc.UnpinSession)  // 取消置顶会话
		salesGroup.PUT("/sessions/:id/rename", salesRAGc.RenameSession) // 重命名会话

		// 消息管理
		salesGroup.POST("/sessions/:id/chat", salesRAGc.ChatWithSession)                         // 基于会话的销售对话（SSE流式）
		salesGroup.GET("/sessions/:id/messages", salesRAGc.ListMessages)                         // 获取会话消息列表
		salesGroup.POST("/sessions/:id/messages/:message_id/feedback", salesRAGc.SubmitFeedback) // 提交消息反馈（点赞/点踩）
		salesGroup.GET("/sessions/:id/messages/:message_id/feedback", salesRAGc.GetFeedback)     // 获取消息反馈

		// 客户档案管理
		salesGroup.PUT("/sessions/:id/customer-profile", salesRAGc.UpdateCustomerProfile) // 更新客户档案
		salesGroup.GET("/sessions/:id/customer-profile", salesRAGc.GetCustomerProfile)    // 获取客户档案
		salesGroup.POST("/analyze-profile", salesRAGc.AnalyzeProfile)                     // 解析文档生成客户档案
		salesGroup.POST("/analyze-profile-text", salesRAGc.AnalyzeProfileText)            // 纯文本分析生成客户档案

		// 聊天风格分析
		salesGroup.POST("/analyze-chat-style", salesRAGc.AnalyzeChatStyle) // 分析聊天风格（语言指纹）
		salesGroup.GET("/analyze-chat-style", salesRAGc.GetLanguageStyle)  // 获取已分析的聊天风格
		salesGroup.PUT("/analyze-chat-style", salesRAGc.SaveLanguageStyle) // 保存/更新语言风格
		salesGroup.POST("/ocr", salesRAGc.OCR)                             // OCR 识别图片
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

		// 书签管理
		authGroup.POST("/sop/bookmarks", userSopc.SaveBookmark)         // 保存节点为书签
		authGroup.GET("/sop/bookmarks/:id", userSopc.GetBookmark)       // 获取书签详情
		authGroup.DELETE("/sop/bookmarks/:id", userSopc.DeleteBookmark) // 删除书签

		// 逐步执行SOP节点（新增）- 注意：这些路由必须在 /sop/runs/:id 之前注册，避免路由冲突
		authGroup.POST("/sop/runs", userSopc.CreateRun)                                       // 创建Run（不立即执行，支持自动应用书签）
		authGroup.GET("/sop/runs/:id/next-node", userSopc.GetNextNode)                        // 获取下一个待执行节点
		authGroup.POST("/sop/runs/:id/nodes/:node_id/execute", userSopc.ExecuteNodeStream)    // 流式执行指定节点（支持文件上传）
		authGroup.POST("/sop/runs/:id/nodes/:node_id/apply-bookmark", userSopc.ApplyBookmark) // 应用书签到节点
		authGroup.DELETE("/sop/runs/:id/draft", userSopc.DeleteDraftRun)                      // 删除草稿状态的run
		authGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)                        // 删除草稿状态的run（Beacon方式）
		authGroup.POST("/sop/files/check-quality", userSopc.CheckFileQuality)                 // 检测上传文件质量
		authGroup.POST("/sop/files/parse-text", userSopc.ParseFileText)                       // 上传文件解析文本（返回文本用于回填）
		authGroup.POST("/sop/files/parse-text/query", userSopc.ParseFileTextQuery)            // 轮询qwen-long解析结果
		authGroup.POST("/sop/images/read", userSopc.ReadImageWithQwenVL)                      // 读取图片（qwen-vl-max）
		authGroup.POST("/sop/text/edit", userSopc.EditTextStream)                             // 文本编辑流式对话（不保存到数据库）
		authGroup.POST("/sop/chat/stream", userSopc.ChatAfterRunStream)                       // Run完成后的对话流式接口
		authGroup.GET("/sop/runs/:id/chat-messages", userSopc.ListRunChatMessages)            // 获取Run聊天记录
		authGroup.GET("/sop/runs/:id/status", userSopc.GetRunStatus)                          // 获取Run执行状态

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

	// 积分查询
	{
		creditCtrl := creditcontroller.New(b.Credit())
		authGroup.GET("/credits/balance", creditCtrl.GetBalance)
	}

	// 订单管理（B 客户）
	{
		orderCtrl := ordercontroller.New(b.Payment(), store.S)
		authGroup.POST("/orders", orderCtrl.CreateOrder)
		authGroup.GET("/orders", orderCtrl.ListOrders)
		authGroup.GET("/orders/:id", orderCtrl.GetOrder)
	}

	// 客户管理相关
	{
		customerCtrl := customercontroller.NewCustomerController(b.Customers(), b.Users())
		authGroup.POST("/customers", customerCtrl.Create)                                           // 创建子客户（注册）
		authGroup.GET("/customers/check-username", customerCtrl.CheckUsername)                      // 检查用户名是否可用
		authGroup.GET("/customers/statistics", customerCtrl.GetStatistics)                          // 获取客户统计数据
		authGroup.GET("/customers/sub-users", customerCtrl.ListSubUsers)                            // 获取二级客户列表
		authGroup.GET("/customers/sub-users/:user_id", customerCtrl.GetSubUserDetail)               // 获取二级客户详情
		authGroup.GET("/customers/sub-users/:user_id/templates", customerCtrl.ListSubUserTemplates) // 获取二级客户已授权模板
		authGroup.POST("/customers/sub-users/:user_id/templates", customerCtrl.GrantTemplates)      // 为二级客户授权模板
		authGroup.POST("/customers/batch/grant-templates", customerCtrl.BatchGrantTemplates)        // 批量为多个二级客户授权模板
		authGroup.POST("/customers/batch/revoke-templates", customerCtrl.BatchRevokeTemplates)      // 批量为多个二级客户撤销模板权限
		authGroup.PUT("/customers/sub-users/:user_id/tier", customerCtrl.UpdateSubUserTier)         // 升级子用户会员等级
		authGroup.DELETE("/customers/sub-users/:user_id/templates", customerCtrl.RevokeTemplates)   // 撤销二级客户模板权限

		// 功能权限管理
		authGroup.GET("/customers/sub-users/:user_id/features", customerCtrl.ListSubUserFeatures)
		authGroup.POST("/customers/sub-users/:user_id/features", customerCtrl.GrantFeatures)
		authGroup.DELETE("/customers/sub-users/:user_id/features", customerCtrl.RevokeFeatures)
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
			chatbotGroup.POST("/sessions", chatbotCtrl.CreateSession)
			chatbotGroup.GET("/sessions", chatbotCtrl.ListSessions)
			chatbotGroup.DELETE("/sessions/:id", chatbotCtrl.DeleteSession)
			chatbotGroup.GET("/sessions/:id/messages", chatbotCtrl.ListMessages)
			chatbotGroup.POST("/sessions/:id/chat", chatbotCtrl.Chat)
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

	// 支付回调（无需鉴权）
	{
		paymentCtrl := paymentcontroller.New(b.Payment())
		v1Group.POST("/payment/wechat/notify", paymentCtrl.WechatNotify)
		v1Group.POST("/payment/alipay/notify", paymentCtrl.AlipayNotify)
	}

	return nil
}
