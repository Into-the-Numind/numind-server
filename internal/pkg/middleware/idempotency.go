package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
)

const (
	headerIdempotencyKey = "Idempotency-Key"
	maxIdempotencyKeyLen = 64
)

// RequireIdempotencyKey enforces presence of the Idempotency-Key header
// on write methods (POST/PUT/PATCH) and validates its length.
func RequireIdempotencyKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(headerIdempotencyKey)
		isWrite := c.Request.Method == http.MethodPost ||
			c.Request.Method == http.MethodPut ||
			c.Request.Method == http.MethodPatch

		if key == "" {
			if isWrite {
				core.WriteResponse(c, errno.ErrBind.SetMessage("Idempotency-Key 必填"), nil)
				c.Abort()
				return
			}
			c.Next()
			return
		}
		if len(key) > maxIdempotencyKeyLen {
			core.WriteResponse(c, errno.ErrBind.SetMessage("Idempotency-Key 长度超限（最多 64）"), nil)
			c.Abort()
			return
		}
		c.Set("idempotency_key", key)
		c.Next()
	}
}
