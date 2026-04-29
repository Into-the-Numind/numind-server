package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
)

// MaintenanceMode reads MAINTENANCE_MODE env var. When "true", all non-GET/HEAD/OPTIONS
// requests return 503 with Retry-After: 600, EXCEPT payment callbacks (whitelisted)
// to avoid losing wechat/alipay async notifications during the maintenance window.
func MaintenanceMode() gin.HandlerFunc {
	enabled := os.Getenv("MAINTENANCE_MODE") == "true"
	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		// 支付回调豁免（保证回调不丢失）
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/v1/payment/") &&
			(strings.HasSuffix(path, "/notify") || strings.HasSuffix(path, "/callback")) {
			c.Next()
			return
		}

		// 放行 GET/HEAD/OPTIONS
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}

		// 写请求 503
		c.Header("Retry-After", "600")
		core.WriteResponse(c, errno.ErrSystemMaintenance.SetMessage("系统维护中，请稍后再试"), nil)
		c.Abort()
	}
}
