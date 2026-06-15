package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
)

// FeatureFlag returns a gin middleware that gates a route group behind a viper
// config bool key. When viper.GetBool(key) is false (unset = false, the default
// OFF posture for notification-center on prod), it responds 404 ErrFeatureDisabled
// and aborts the chain; otherwise it calls c.Next().
//
// Usage: router.Group("/v1/announcements").Use(middleware.FeatureFlag("features.notification_center.enabled"))
func FeatureFlag(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !viper.GetBool(key) {
			core.WriteResponse(c, errno.ErrFeatureDisabled, nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
