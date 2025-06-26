package middleware

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/known"
	"numind-server/internal/pkg/log"
)

// Auther 用来定义授权接口实现.
// sub: 操作主题，obj：操作对象, act：操作
type Auther interface {
	Authorize(sub, obj, act string) (bool, error)
}

// Authz 是 Gin 中间件，用来进行请求授权.
func Authz(a Auther) gin.HandlerFunc {
	return func(c *gin.Context) {
		sub := c.GetString(known.XUsernameKey)
		obj := c.Request.URL.Path
		act := c.Request.Method

		log.Debugw("Build authorize context", "sub", sub, "obj", obj, "act", act)
		if allowed, _ := a.Authorize(sub, obj, act); !allowed {
			// 未通过授权
			core.WriteResponse(c, errno.ErrUnauthorized, nil)
			// 在中间件中调用 Abort() 方法，会直接终止请求
			c.Abort()
			return
		}
	}
}
