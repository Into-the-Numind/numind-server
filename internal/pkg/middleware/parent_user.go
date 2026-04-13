package middleware

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"github.com/gin-gonic/gin"
)

// ParentUserOnly 仅允许父用户（主账号）访问的中间件
func ParentUserOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			core.WriteResponse(c, errno.ErrTokenInvalid, nil)
			c.Abort()
			return
		}
		if user.ParentUserID != nil {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("仅限主账号操作"), nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
