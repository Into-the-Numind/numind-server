package numind

import (
	"fmt"
	"numind-server/internal/numind/biz"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/controller/v1/account"
	"numind-server/internal/numind/controller/v1/admin"
	"numind-server/internal/numind/controller/v1/article"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/card"
	"numind-server/internal/numind/controller/v1/category"
	"numind-server/internal/numind/controller/v1/chat"
	"numind-server/internal/numind/controller/v1/image"
	"numind-server/internal/numind/controller/v1/membership"
	"numind-server/internal/numind/controller/v1/order"
	"numind-server/internal/numind/controller/v1/pagination"
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

	"numind-server/internal/numind/controller/v1/config"
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
	adminc := admin.NewAdminController(b.Admin())

	v1Group := g.Group("/v1")

	// 登录接口不需要鉴权
	v1Group.POST("/wechat/login", uc.WechatLogin)
	v1Group.POST("/admin/login", uc.Login)

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

	// 卡册相关
	{
		authGroup.PUT("/books/:id/category", bc.SetCategory) // 设置卡册分类
		authGroup.POST("/books", bc.Create)                  // 创建卡册
		authGroup.GET("/books", bc.List)                     // 获取卡册列表
		authGroup.GET("/books/:id", bc.Get)                  // 获取卡册详情
		authGroup.PUT("/books/:id", bc.Update)               // 更新卡册
		authGroup.DELETE("/books/:id", bc.Delete)            // 删除卡册
		authGroup.DELETE("/books", bc.DeleteBatch)           // 批量删除卡册，query: bookID=1&bookID=2
		authGroup.GET("/books/:id/html", bc.ViewBookHTML)    // 查看卡册HTML
		authGroup.GET("/books/:id/image", bc.ViewBookImage)  // 查看卡册图片
	}

	// 卡片相关
	{
		cardController := card.New(b)
		authGroup.POST("/cards", cardController.Create)                              // 创建卡片
		authGroup.GET("/cards", cardController.List)                                 // 获取卡片列表
		authGroup.GET("/cards/:id", cardController.Get)                              // 获取卡片详情
		authGroup.PUT("/cards/:id", cardController.Update)                           // 更新卡片
		authGroup.DELETE("/cards/:id", cardController.Delete)                        // 删除卡片
		authGroup.POST("/cards/:id/render", cardController.RenderCard)               // 渲染卡片
		authGroup.POST("/cards/book/:bookId/render", cardController.RenderBookCards) // 渲染书籍卡片
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

	// 管理员相关
	adminGroup := v1Group.Group("/admin", importMw.AuthMiddleware())
	{
		adminGroup.GET("/articles", adminc.GetArticles)                     // 获取文章列表（管理员）
		adminGroup.GET("/articles/:id", adminc.GetArticle)                  // 获取单个文章（管理员）
		adminGroup.POST("/articles", adminc.CreateArticle)                  // 创建文章（管理员）
		adminGroup.PUT("/articles/:id", adminc.UpdateArticle)               // 更新文章（管理员）
		adminGroup.DELETE("/articles/:id", adminc.DeleteArticle)            // 删除文章（管理员）
		adminGroup.POST("/articles/bulk-delete", adminc.BulkDeleteArticles) // 批量删除文章
		adminGroup.GET("/categories", adminc.GetCategories)                 // 获取分类列表
		adminGroup.POST("/categories", adminc.CreateCategory)               // 创建分类
		adminGroup.PUT("/categories/:id", adminc.UpdateCategory)            // 更新分类
		adminGroup.DELETE("/categories/:id", adminc.DeleteCategory)         // 删除分类
		adminGroup.GET("/stats", adminc.GetStats)                           // 获取统计信息

		// 用户管理相关API（管理员权限）
		adminGroup.GET("/users", uc.List)            // 查询用户列表
		adminGroup.GET("/users/:name", uc.Get)       // 查询用户详情
		adminGroup.PUT("/users/:name", uc.Update)    // 更改用户
		adminGroup.DELETE("/users/:name", uc.Delete) // 删除用户
	}

	// 配置相关（管理员权限）
	{
		configc := config.New(b)
		adminGroup.POST("/configs", configc.Create)           // 创建配置
		adminGroup.GET("/configs", configc.List)              // 获取所有配置
		adminGroup.GET("/configs/:key", configc.Get)          // 获取单个配置
		adminGroup.PUT("/configs/:key", configc.Update)       // 更新配置
		adminGroup.DELETE("/configs/:key", configc.Delete)    // 删除配置
		adminGroup.POST("/configs/init", configc.InitDefault) // 初始化默认配置
	}

	// 用户相关
	{
		authGroup.GET("/users/me", uc.GetCurrentUser)    // 获取当前用户信息
		authGroup.PUT("/users/me", uc.UpdateProfile)     // 更新当前用户个人信息
		authGroup.POST("/users/avatar", uc.UploadAvatar) // 上传用户头像
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
		authGroup.POST("/membership/consume", membershipCtrl.ConsumeUsage)            // 消费使用次数
		g.GET("/membership/plans", membershipCtrl.GetMembershipPlans)                 // 获取会员套餐信息（无需鉴权）
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
