package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/controller/v1/ali"
	customercontroller "numind-server/internal/numind/controller/v1/customer"
	pdfcontroller "numind-server/internal/numind/controller/v1/pdf"
	"numind-server/internal/numind/controller/v1/salesrag"
	sopcontroller "numind-server/internal/numind/controller/v1/sop"
	"numind-server/internal/numind/controller/v1/user"
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
	salesRAGc := salesrag.NewSalesRAGController(b)

	// 初始化SOP控制器（用户端）
	userSopc := sopcontroller.NewSopController(b.Sop(), b.Ali(), b.Volc())

	// 初始化PDF控制器
	pdfc := pdfcontroller.NewPdfController()

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
		salesGroup.POST("/sessions/:id/chat", salesRAGc.ChatWithSession) // 基于会话的销售对话（SSE流式）
		salesGroup.GET("/sessions/:id/messages", salesRAGc.ListMessages) // 获取会话消息列表

		// 客户档案管理
		salesGroup.PUT("/sessions/:id/customer-profile", salesRAGc.UpdateCustomerProfile) // 更新客户档案
		salesGroup.GET("/sessions/:id/customer-profile", salesRAGc.GetCustomerProfile)    // 获取客户档案
		salesGroup.POST("/analyze-profile", salesRAGc.AnalyzeProfile)           // 解析文档生成客户档案
		salesGroup.POST("/analyze-profile-text", salesRAGc.AnalyzeProfileText) // 纯文本分析生成客户档案

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

	return nil
}
