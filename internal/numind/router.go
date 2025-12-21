package numind

import (
	"fmt"
	"numind-server/internal/numind/biz"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/controller/v1/account"
	"numind-server/internal/numind/controller/v1/article"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/card"
	"numind-server/internal/numind/controller/v1/category"
	"numind-server/internal/numind/controller/v1/chat"
	"numind-server/internal/numind/controller/v1/image"
	"numind-server/internal/numind/controller/v1/membership"
	"numind-server/internal/numind/controller/v1/order"
	"numind-server/internal/numind/controller/v1/pagination"
	ragcontroller "numind-server/internal/numind/controller/v1/rag"
	sopcontroller "numind-server/internal/numind/controller/v1/sop"
	"numind-server/internal/numind/controller/v1/template"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/controller/v1/feedback"
	importPayController "numind-server/internal/numind/controller/v1/pay"
	importMw "numind-server/internal/pkg/middleware"
)

// installNumindRouters 注册所有 Numind 小程序业务路由
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

	// 暂时禁用gzip压缩中间件，排查乱码问题
	// g.Use(importMw.GzipCompression())

	uc := user.New(store.S)
	b := biz.NewBiz(store.S)
	ic := image.New(b)
	bc := book.New(b)
	catc := category.New(b)
	tc := template.New(b)
	fc := feedback.New(b)
	chatc := chat.New(b.Chats())
	ac := article.NewArticleController(b.Article())

	// 使用 biz 层已初始化的 RAG 服务
	ragService := b.Rag()
	if ragService == nil {
		log.Fatalw("RAG服务未初始化")
	}
	ragc := ragcontroller.NewRagController(ragService, b.Chats())

	// 初始化SOP控制器（用户端）
	userSopc := sopcontroller.NewSopController(b.Sop(), b.Ali(), b.Volc())

	// 🔍 系统启动时检查并向量化历史笔记（异步执行，不阻塞启动）- 暂时注释，不使用向量化
	// go func() {
	// 	ctx := context.Background()
	// 	log.Infow("开始检查历史笔记向量化状态")
	// 	if err := vectorizeHistoricalBooks(ctx, b, ragService); err != nil {
	// 		log.Errorw("历史笔记向量化检查失败", "error", err)
	// 	} else {
	// 		log.Infow("历史笔记向量化检查完成")
	// 	}
	// }()

	v1Group := g.Group("/v1")

	// 登录接口不需要鉴权
	v1Group.POST("/wechat/login", uc.WechatLogin)
	v1Group.POST("/web/login", uc.WebLogin)

	// WebSocket连接不需要鉴权，因为它在内部处理认证
	v1Group.GET("/chat/ws", chatc.WebSocket)

	// 需要鉴权的接口
	authGroup := v1Group.Group("")
	authGroup.Use(importMw.AuthMiddleware())

	// 图片相关
	{
		authGroup.POST("/images", ic.Create)
		authGroup.POST("/images/batch", ic.BatchUpload)
		authGroup.GET("/images", ic.List)
		authGroup.GET("/images/:id", ic.Get)
		authGroup.PUT("/images/:id", ic.Update)
		authGroup.DELETE("/images/:id", ic.Delete)
	}

	// 笔记相关
	{
		authGroup.PUT("/books/:id/category", bc.SetCategory)                               // 设置卡册分类
		authGroup.POST("/books", bc.Create)                                                // 创建卡册
		authGroup.GET("/books", bc.List)                                                   // 获取卡册列表
		authGroup.GET("/books/:id", bc.Get)                                                // 获取卡册详情
		authGroup.PUT("/books/:id", bc.Update)                                             // 更新卡册
		authGroup.PUT("/books/:id/type", bc.UpdateBookType)                                // 更新笔记类型（用于 todo 打钩）
		authGroup.PUT("/books/:id/content", bc.UpdateContent)                              // 更新笔记内容
		authGroup.POST("/books/:id/generate-long-image", bc.GenerateLongImage)             // 生成长图
		authGroup.POST("/books/:id/generate-paginated-images", bc.GeneratePaginatedImages) // 生成分页图片
		authGroup.DELETE("/books/:id", bc.Delete)                                          // 删除卡册
		authGroup.DELETE("/books", bc.DeleteBatch)                                         // 批量删除卡册，query: bookID=1&bookID=2
	}

	// 卡片相关
	{
		cardController := card.New(b)
		authGroup.POST("/cards", cardController.Create)       // 创建卡片
		authGroup.GET("/cards", cardController.List)          // 获取卡片列表
		authGroup.GET("/cards/:id", cardController.Get)       // 获取卡片详情
		authGroup.PUT("/cards/:id", cardController.Update)    // 更新卡片
		authGroup.DELETE("/cards/:id", cardController.Delete) // 删除卡片
	}

	// 分类相关
	{
		authGroup.POST("/categories", catc.Create)       // 创建分类
		authGroup.GET("/categories", catc.List)          // 获取分类列表
		authGroup.GET("/categories/:id", catc.Get)       // 获取分类详情
		authGroup.PUT("/categories/:id", catc.Update)    // 更新分类
		authGroup.DELETE("/categories/:id", catc.Delete) // 删除分类
	}

	// 模板相关
	{
		authGroup.POST("/templates", tc.Create)       // 创建模板
		authGroup.GET("/templates", tc.List)          // 获取模板列表
		authGroup.GET("/templates/:id", tc.Get)       // 获取模板详情
		authGroup.PUT("/templates/:id", tc.Update)    // 更新模板
		authGroup.DELETE("/templates/:id", tc.Delete) // 删除模板
	}

	// 反馈相关
	{
		authGroup.POST("/feedbacks", fc.Create)       // 创建反馈
		authGroup.GET("/feedbacks", fc.List)          // 获取反馈列表
		authGroup.GET("/feedbacks/:id", fc.Get)       // 获取反馈详情
		authGroup.DELETE("/feedbacks/:id", fc.Delete) // 删除反馈
	}

	// 对话相关
	{
		authGroup.POST("/chat/sessions", chatc.CreateSession)                           // 创建对话会话
		authGroup.GET("/chat/sessions", chatc.ListSessions)                             // 获取会话列表
		authGroup.GET("/chat/sessions/:id", chatc.GetSession)                           // 获取会话详情
		authGroup.PUT("/chat/sessions/:id", chatc.UpdateSession)                        // 更新会话
		authGroup.DELETE("/chat/sessions/:id", chatc.DeleteSession)                     // 删除会话
		authGroup.GET("/chat/sessions/:id/messages", chatc.ListMessages)                // 获取会话消息
		authGroup.GET("/chat/sessions/:id/with-messages", chatc.GetSessionWithMessages) // 获取会话及消息
		// 笔记聊天相关
		authGroup.GET("/chat/book/:book_id/sessions", chatc.ListBookSessions)       // 列出笔记的所有会话
		authGroup.GET("/chat/session/:session_id/history", chatc.GetSessionHistory) // 获取会话的聊天记录
	}

	// 分页相关
	{
		paginationController := pagination.NewPaginationController(b.Pagination())
		authGroup.POST("/pagination/paginate", paginationController.Paginate)              // 执行分页
		authGroup.POST("/pagination/paginate-json", paginationController.PaginateFromJSON) // 从JSON字符串分页
		authGroup.GET("/pagination/config", paginationController.GetConfig)                // 获取配置
		authGroup.GET("/pagination/style-config", paginationController.GetStyleConfig)     // 获取样式配置
		authGroup.PUT("/pagination/config", paginationController.UpdateConfig)             // 更新配置
		authGroup.GET("/pagination/test", paginationController.TestPagination)             // 测试分页功能
	}

	// 文章相关
	{
		authGroup.POST("/articles/fetch", ac.FetchArticle)                // 获取文章内容
		authGroup.GET("/articles", ac.GetArticles)                        // 获取文章列表
		authGroup.GET("/articles/:id", ac.GetArticle)                     // 获取单个文章
		authGroup.PUT("/articles/:id/category", ac.UpdateArticleCategory) // 更新文章分类
		authGroup.DELETE("/articles/:id", ac.DeleteArticle)               // 删除文章
		authGroup.POST("/articles/:id/favorite", ac.AddFavorite)          // 添加收藏
		authGroup.DELETE("/articles/:id/favorite", ac.RemoveFavorite)     // 移除收藏
		authGroup.GET("/articles/favorites", ac.GetFavorites)             // 获取收藏列表
		authGroup.POST("/articles/paraphrase", ac.ParaphraseText)         // 文本释义
	}

	// 用户相关
	{
		authGroup.GET("/users/me", uc.GetCurrentUser)    // 获取当前用户信息
		authGroup.PUT("/users/me", uc.UpdateProfile)     // 更新当前用户个人信息
		authGroup.POST("/users/avatar", uc.UploadAvatar) // 上传用户头像
		authGroup.POST("/users/logout", uc.Logout)       // 用户登出
	}

	// 微信支付相关
	{
		// 微信支付下单接口（需鉴权）
		authGroup.POST("/pay/wechat/native", importPayController.WechatNativePay)
		authGroup.POST("/pay/wechat/miniprogram", importPayController.WechatMiniProgramPay)
		// 微信支付回调接口（无需鉴权）
		g.POST("/api/pay/wechat/notify", importPayController.WechatPayNotify)
	}

	// 订单相关
	{
		orderBiz := orderbiz.NewOrderBiz(store.S, b.Users(), b.AccountRecords())
		orderCtrl := order.New(orderBiz)
		authGroup.POST("/order/create", orderCtrl.Create)
		authGroup.GET("/order/list", orderCtrl.ListByUser)
		g.POST("/api/v1/order/wechat_notify", orderCtrl.WechatNotify)
	}

	// 账户记录相关
	{
		accountCtrl := account.NewAccountController(b)
		authGroup.GET("/account/records", accountCtrl.GetUserPaymentHistory) // 获取用户支付历史
		authGroup.GET("/account/total", accountCtrl.GetUserTotalAmount)      // 获取用户总消费金额
		authGroup.GET("/account/summary", accountCtrl.GetUserAccountSummary) // 获取用户账户摘要
	}

	// 会员相关
	{
		membershipCtrl := membership.NewMembershipController(b)
		authGroup.POST("/membership/payment", membershipCtrl.CreateMembershipPayment) // 创建会员购买支付
		authGroup.GET("/membership/info", membershipCtrl.GetMembershipInfo)           // 获取用户会员信息
		authGroup.GET("/membership/permission", membershipCtrl.CheckCreatePermission) // 检查用户创建卡册权限
		//authGroup.POST("/membership/consume", membershipCtrl.ConsumeUsage)            // 消费使用次数
		g.GET("/v1/membership/plans", membershipCtrl.GetMembershipPlans) // 获取会员套餐信息（无需鉴权）
	}

	// RAG相关
	{
		authGroup.POST("/rag/chat", ragc.ChatWithRAG) // 基于笔记进行RAG对话
	}

	// SOP相关（用户端接口）
	{
		// 用户执行SOP
		authGroup.GET("/sop/templates", userSopc.ListTemplates)                    // 获取可用模板列表
		authGroup.GET("/sop/templates/:id/nodes", userSopc.GetTemplateNodes)       // 获取模板的所有节点
		authGroup.POST("/sop/templates/:id/execute", userSopc.ExecuteTemplate)     // 执行模板（异步，一次性执行所有节点）
		authGroup.GET("/sop/templates/executed", userSopc.ListMyExecutedTemplates) // 获取当前用户已执行的模板列表（按模板分组）

		// 逐步执行SOP节点（新增）- 注意：这些路由必须在 /sop/runs/:id 之前注册，避免路由冲突
		authGroup.POST("/sop/runs", userSopc.CreateRun)                                    // 创建Run（不立即执行）
		authGroup.GET("/sop/runs/:id/next-node", userSopc.GetNextNode)                     // 获取下一个待执行节点
		authGroup.POST("/sop/runs/:id/nodes/:node_id/execute", userSopc.ExecuteNodeStream) // 流式执行指定节点（支持文件上传）
		authGroup.POST("/sop/files/check-quality", userSopc.CheckFileQuality)              // 检测上传文件质量
		authGroup.POST("/sop/text/edit", userSopc.EditTextStream)                          // 文本编辑流式对话（不保存到数据库）
		authGroup.GET("/sop/runs/:id/status", userSopc.GetRunStatus)                       // 获取Run执行状态

		authGroup.GET("/sop/runs/:id", userSopc.GetRun)              // 查看执行记录
		authGroup.GET("/sop/runs/:id/detail", userSopc.GetRunDetail) // 查看执行详情
		authGroup.GET("/sop/runs", userSopc.ListMyRuns)              // 获取我的执行记录列表
		authGroup.GET("/sop/notes/:id", userSopc.GetNote)            // 查看笔记详情
		authGroup.GET("/sop/notes", userSopc.ListMyNotes)            // 获取我的笔记列表
	}

	return nil
}

// getUserIDFromToken 从JWT token中获取用户ID
func getUserIDFromToken(c *gin.Context) (uint, error) {
	header := c.Request.Header.Get("Authorization")
	if len(header) == 0 {
		return 0, fmt.Errorf("missing authorization header")
	}

	var tokenString string
	fmt.Sscanf(header, "Bearer %s", &tokenString)

	// 使用viper获取JWT密钥
	jwtSecret := viper.GetString("jwt.secret")
	if jwtSecret == "" {
		return 0, fmt.Errorf("jwt secret not configured")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if userID, exists := claims["user_id"]; exists {
			return uint(userID.(float64)), nil
		}
	}

	return 0, fmt.Errorf("invalid token or missing user_id")
}

// vectorizeHistoricalBooks 检查并向量化历史笔记 - 暂时注释，不使用向量化
// 在系统启动时异步执行，检查所有已创建的笔记，如果还没有向量化，则进行向量化
// func vectorizeHistoricalBooks(ctx context.Context, b biz.IBiz, ragService *ragbiz.RagService) error {
// 	log.Infow("开始检查历史笔记向量化状态")

// 	// 分批获取所有笔记（只获取状态为 success 的笔记）
// 	batchSize := 100
// 	offset := 0
// 	totalProcessed := 0
// 	totalVectorized := 0
// 	totalSkipped := 0

// 	for {
// 		// 获取一批笔记
// 		count, books, err := b.Books().ListAll(ctx, offset, batchSize)
// 		if err != nil {
// 			return fmt.Errorf("获取笔记列表失败: %w", err)
// 		}

// 		if len(books) == 0 {
// 			break // 没有更多笔记了
// 		}

// 		log.Infow("处理笔记批次", "offset", offset, "count", len(books), "total", count)

// 		// 遍历这批笔记，检查并向量化
// 		for _, book := range books {
// 			// 只处理状态为 success 的笔记
// 			if book.Status != "success" {
// 				totalSkipped++
// 				continue
// 			}

// 			// 检查向量是否已存在
// 			exists, err := ragService.CheckBookVectorExists(ctx, book.ID)
// 			if err != nil {
// 				log.Errorw("检查笔记向量失败", "error", err, "book_id", book.ID)
// 				continue
// 			}

// 			if exists {
// 				totalSkipped++
// 				continue // 向量已存在，跳过
// 			}

// 			// 提取笔记内容（优先使用 ProcessedText，如果为空则使用 OriginalText）
// 			bookContent := book.ProcessedText
// 			if bookContent == "" {
// 				bookContent = book.OriginalText
// 			}

// 			// 如果内容为空，跳过
// 			if bookContent == "" {
// 				totalSkipped++
// 				continue
// 			}

// 			// 向量化笔记
// 			if err := ragService.AddBookVector(ctx, book.UserID, book.ID, bookContent); err != nil {
// 				log.Errorw("向量化笔记失败", "error", err, "book_id", book.ID, "user_id", book.UserID)
// 				// 继续处理下一个笔记，不中断整个流程
// 				continue
// 			}

// 			totalVectorized++
// 			log.Infow("✅ 历史笔记向量化成功", "book_id", book.ID, "user_id", book.UserID)
// 		}

// 		totalProcessed += len(books)
// 		offset += batchSize

// 		// 如果已经处理完所有笔记，退出循环
// 		if int64(offset) >= count {
// 			break
// 		}
// 	}

// 	log.Infow("历史笔记向量化检查完成",
// 		"total_processed", totalProcessed,
// 		"total_vectorized", totalVectorized,
// 		"total_skipped", totalSkipped)

// 	return nil
// }
