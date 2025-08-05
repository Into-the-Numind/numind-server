package numind

import (
	"numind-server/internal/numind/biz"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/card"
	"numind-server/internal/numind/controller/v1/category"
	"numind-server/internal/numind/controller/v1/chat"
	"numind-server/internal/numind/controller/v1/image"
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

	"numind-server/internal/numind/controller/v1/feedback"
	importPayController "numind-server/internal/numind/controller/v1/pay"
	importMw "numind-server/internal/pkg/middleware"
	importServices "numind-server/internal/services"
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

	// 初始化 AuthService
	db := store.S.DB()
	authService := importServices.NewAuthService(db)

	uc := user.New(store.S)
	b := biz.NewBiz(store.S)
	ic := image.New(b)
	cc := card.New(b)
	bc := book.New(b)
	catc := category.New(b)
	tc := template.New(b)
	fc := feedback.New(b)
	chatc := chat.New(store.S)

	v1Group := g.Group("/v1")

	// 登录接口不需要鉴权
	v1Group.POST("/wechat/login", uc.WechatLogin)
	v1Group.POST("/admin/login", uc.Login)

	// 需要鉴权的接口
	authGroup := v1Group.Group("")
	authGroup.Use(importMw.AuthMiddleware(authService))

	// 图片相关
	authGroup.POST("/images", ic.Create)
	authGroup.POST("/images/batch", ic.BatchUpload)
	authGroup.GET("/images", ic.List)
	authGroup.GET("/images/:id", ic.Get)
	authGroup.PUT("/images/:id", ic.Update)
	authGroup.DELETE("/images/:id", ic.Delete)

	// 卡片相关
	authGroup.POST("/cards", cc.Create)
	authGroup.GET("/cards", cc.List)
	authGroup.GET("/cards/:id", cc.Get)
	authGroup.PUT("/cards/:id", cc.Update)
	authGroup.DELETE("/cards/:id", cc.Delete)

	// 卡册相关
	authGroup.POST("/books", bc.Create)       // 创建卡册
	authGroup.GET("/books", bc.List)          // 获取卡册列表
	authGroup.GET("/books/:id", bc.Get)       // 获取卡册详情
	authGroup.PUT("/books/:id", bc.Update)    // 更新卡册
	authGroup.DELETE("/books/:id", bc.Delete) // 删除卡册

	// 分类相关
	authGroup.POST("/categories", catc.Create)       // 创建分类
	authGroup.GET("/categories", catc.List)          // 获取分类列表
	authGroup.GET("/categories/:id", catc.Get)       // 获取分类详情
	authGroup.PUT("/categories/:id", catc.Update)    // 更新分类
	authGroup.DELETE("/categories/:id", catc.Delete) // 删除分类

	// 模板相关
	authGroup.POST("/templates", tc.Create)       // 创建模板
	authGroup.GET("/templates", tc.List)          // 获取模板列表
	authGroup.GET("/templates/:id", tc.Get)       // 获取模板详情
	authGroup.PUT("/templates/:id", tc.Update)    // 更新模板
	authGroup.DELETE("/templates/:id", tc.Delete) // 删除模板

	// 反馈相关
	authGroup.POST("/feedbacks", fc.Create)       // 创建反馈
	authGroup.GET("/feedbacks", fc.List)          // 获取反馈列表
	authGroup.GET("/feedbacks/:id", fc.Get)       // 获取反馈详情
	authGroup.DELETE("/feedbacks/:id", fc.Delete) // 删除反馈

	// 对话相关
	authGroup.GET("/chat/ws", chatc.WebSocket)                                      // WebSocket连接
	authGroup.POST("/chat/sessions", chatc.CreateSession)                           // 创建对话会话
	authGroup.GET("/chat/sessions", chatc.ListSessions)                             // 获取会话列表
	authGroup.GET("/chat/sessions/:id", chatc.GetSession)                           // 获取会话详情
	authGroup.PUT("/chat/sessions/:id", chatc.UpdateSession)                        // 更新会话
	authGroup.DELETE("/chat/sessions/:id", chatc.DeleteSession)                     // 删除会话
	authGroup.GET("/chat/sessions/:id/messages", chatc.ListMessages)                // 获取会话消息
	authGroup.GET("/chat/sessions/:id/with-messages", chatc.GetSessionWithMessages) // 获取会话及消息

	// 分页相关
	paginationController := pagination.NewPaginationController(b.Pagination())
	authGroup.POST("/pagination/paginate", paginationController.Paginate)              // 执行分页
	authGroup.POST("/pagination/paginate-json", paginationController.PaginateFromJSON) // 从JSON字符串分页
	authGroup.GET("/pagination/config", paginationController.GetConfig)                // 获取配置
	authGroup.GET("/pagination/style-config", paginationController.GetStyleConfig)     // 获取样式配置
	authGroup.PUT("/pagination/config", paginationController.UpdateConfig)             // 更新配置
	authGroup.GET("/pagination/test", paginationController.TestPagination)             // 测试分页功能

	// 用户相关
	//authGroup.GET("/users", uc.List)              // 查询用户列表
	authGroup.GET("/users/me", uc.GetCurrentUser)    // 获取当前用户信息
	authGroup.PUT("/users/me", uc.UpdateProfile)     // 更新当前用户个人信息
	authGroup.POST("/users/avatar", uc.UploadAvatar) // 上传用户头像
	//authGroup.GET("/users/:name", uc.Get)         // 查询用户详情
	//authGroup.PUT("/users/:name", uc.Update)      // 更改用户
	//authGroup.DELETE("/users/:name", uc.Delete)   // 删除用户

	// 微信支付下单接口（需鉴权）
	authGroup.POST("/pay/wechat/native", importPayController.WechatNativePay)
	authGroup.POST("/pay/wechat/miniprogram", importPayController.WechatMiniProgramPay)
	// 微信支付回调接口（无需鉴权）
	g.POST("/api/pay/wechat/notify", importPayController.WechatPayNotify)

	// 订单相关
	orderBiz := orderbiz.NewOrderBiz(store.S)
	orderCtrl := order.New(orderBiz)
	authGroup.POST("/order/create", orderCtrl.Create)
	authGroup.GET("/order/list", orderCtrl.ListByUser)
	g.POST("/api/v1/order/wechat_notify", orderCtrl.WechatNotify)

	return nil
}

// installNumindAdminRouters 注册所有 Numind 业务路由
func installNumindAdminRouters(g *gin.Engine) error {

	return nil
}
