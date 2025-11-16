package numindadmin

import (
	"numind-server/internal/numind/biz"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/controller/v1/admin"
	adminaccount "numind-server/internal/numind/controller/v1/admin_account"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/config"
	"numind-server/internal/numind/controller/v1/feedback"
	"numind-server/internal/numind/controller/v1/image"
	"numind-server/internal/numind/controller/v1/order"
	"numind-server/internal/numind/controller/v1/template"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

	importMw "numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// installAdminRouters 注册所有后台管理路由
func installAdminRouters(g *gin.Engine) error {
	// 注册 404 Handler
	g.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	})

	// 注册 /healthz handler
	g.GET("/healthz", func(c *gin.Context) {
		log.C(c).Infow("Healthz function called")
		core.WriteResponse(c, nil, map[string]string{"status": "ok"})
	})

	uc := user.New(store.S)
	b := biz.NewBiz(store.S)
	ic := image.New(b)
	bc := book.New(b)
	tc := template.New(b)
	configc := config.New(b)
	adminc := admin.NewAdminController(b.Admin(), b.Payments(), b.Books(), b.Images())
	adminAccountCtrl := adminaccount.NewAdminAccountController(b.AdminAccounts())
	feedbackCtrl := feedback.NewAdminFeedbackController(b)

	// 登录接口不需要鉴权
	v1Group := g.Group("/v1/admin")
	v1Group.POST("/login", adminAccountCtrl.Login)

	// 需要管理员鉴权的接口
	// 使用专门的后台管理中间件，验证 admin token 并从 admin 表查询
	authGroup := v1Group.Group("")
	authGroup.Use(importMw.AdminSystemAuthMiddleware())

	// 用户管理
	{
		authGroup.GET("/users", adminc.GetUserList) // 后台管理系统专用，返回所有用户字段
		authGroup.GET("/users/:name", uc.Get)
		authGroup.PUT("/users/:name", uc.Update)
		authGroup.DELETE("/users/:name", uc.Delete)
	}

	// 笔记（书籍）管理
	{
		authGroup.GET("/books", bc.ListAll)         // 后台管理系统专用，返回所有书籍字段
		authGroup.GET("/books/:id", adminc.GetBook) // 后台管理系统专用，返回笔记详情（包含图片信息）
		authGroup.PUT("/books/:id", bc.Update)
		authGroup.DELETE("/books/:id", bc.Delete)
		authGroup.DELETE("/books", bc.DeleteBatch)
	}

	// 图片管理
	{
		authGroup.GET("/images", ic.List)
		authGroup.GET("/images/:id", ic.Get)
		authGroup.PUT("/images/:id", ic.Update)
		authGroup.DELETE("/images/:id", ic.Delete)
	}

	// 订单管理
	{
		orderBiz := orderbiz.NewOrderBiz(store.S, b.Users(), b.AccountRecords())
		orderCtrl := order.New(orderBiz)
		authGroup.GET("/orders", orderCtrl.ListByUser) // 可以扩展为管理员查看所有订单
	}

	// 支付管理
	{
		authGroup.GET("/payments", adminc.GetPaymentList)
		authGroup.GET("/payments/:out_trade_no", adminc.GetPayment)
	}

	// 模板管理
	{
		authGroup.GET("/templates", tc.List)
		authGroup.GET("/templates/:id", tc.Get)
		authGroup.POST("/templates", tc.Create)
		authGroup.PUT("/templates/:id", tc.Update)
		authGroup.DELETE("/templates/:id", tc.Delete)
	}

	// 系统配置管理
	{
		authGroup.POST("/system-configs", configc.Create)            // 创建系统配置
		authGroup.GET("/system-configs", configc.ListWithPagination) // 分页获取系统配置列表（返回所有字段）
		authGroup.GET("/system-configs/:key", configc.Get)           // 获取单个系统配置
		authGroup.PUT("/system-configs/:key", configc.Update)        // 更新系统配置
		authGroup.DELETE("/system-configs/:key", configc.Delete)     // 删除系统配置
		authGroup.POST("/system-configs/init", configc.InitDefault)  // 初始化默认配置
	}

	// 管理员统计信息
	{
		authGroup.GET("/stats", adminc.GetStats)
	}

	// 反馈管理
	{
		authGroup.POST("/feedbacks", feedbackCtrl.Create)       // 创建反馈
		authGroup.GET("/feedbacks", feedbackCtrl.List)          // 获取反馈列表（返回所有字段）
		authGroup.GET("/feedbacks/:id", feedbackCtrl.Get)       // 获取单个反馈
		authGroup.PUT("/feedbacks/:id", feedbackCtrl.Update)    // 更新反馈
		authGroup.DELETE("/feedbacks/:id", feedbackCtrl.Delete) // 删除反馈
	}

	return nil
}
