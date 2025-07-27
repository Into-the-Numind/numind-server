package numind

import (
	"numind-server/internal/numind/biz"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/card"
	"numind-server/internal/numind/controller/v1/category"
	"numind-server/internal/numind/controller/v1/image"
	"numind-server/internal/numind/controller/v1/order"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"

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

	// 用户相关
	authGroup.GET("/users", uc.List)            // 查询用户列表
	authGroup.GET("/users/:name", uc.Get)       // 查询用户详情
	authGroup.PUT("/users/:name", uc.Update)    // 更改用户
	authGroup.DELETE("/users/:name", uc.Delete) // 删除用户

	// 微信支付下单接口（需鉴权）
	authGroup.POST("/pay/wechat/native", importPayController.WechatNativePay)
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
