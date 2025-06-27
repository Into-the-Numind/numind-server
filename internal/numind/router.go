package numind

import (
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

	v1Group := g.Group("/v1")

	//g.POST("/login", uc.Login)
	v1Group.POST("/wechat/login", uc.WechatLogin)

	return nil
}

// installNumindAdminRouters 注册所有 Numind 业务路由
func installNumindAdminRouters(g *gin.Engine) error {

	return nil
}
