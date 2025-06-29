package numind

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/controller/v1/book"
	"numind-server/internal/numind/controller/v1/card"
	"numind-server/internal/numind/controller/v1/image"
	"numind-server/internal/numind/controller/v1/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/auth"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
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

	authz, err := auth.NewAuthz(store.S.DB())
	if err != nil {
		return err
	}

	uc := user.New(store.S, authz)
	b := biz.NewBiz(store.S)
	ic := image.New(b)
	cc := card.New(b)
	bc := book.New(b)

	v1Group := g.Group("/v1")

	//g.POST("/login", uc.Login)
	v1Group.POST("/wechat/login", uc.WechatLogin)

	// 图片相关
	v1Group.POST("/images", ic.Create)
	v1Group.POST("/images/batch", ic.BatchCreate)
	v1Group.GET("/images", ic.List)
	v1Group.GET("/images/:id", ic.Get)
	v1Group.PUT("/images/:id", ic.Update)
	v1Group.DELETE("/images/:id", ic.Delete)

	// 卡片相关
	v1Group.POST("/cards", cc.Create)
	v1Group.GET("/cards", cc.List)
	v1Group.GET("/cards/:id", cc.Get)
	v1Group.PUT("/cards/:id", cc.Update)
	v1Group.DELETE("/cards/:id", cc.Delete)

	// 卡册相关
	v1Group.POST("/books", bc.Create)
	v1Group.GET("/books", bc.List)
	v1Group.GET("/books/:id", bc.Get)
	v1Group.PUT("/books/:id", bc.Update)
	v1Group.DELETE("/books/:id", bc.Delete)

	return nil
}

// installNumindAdminRouters 注册所有 Numind 业务路由
func installNumindAdminRouters(g *gin.Engine) error {

	return nil
}
