package numind

import (
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/store"
)

func TestInstallAdminRoutersDoesNotInitializeUserSandboxRuntime(t *testing.T) {
	oldStore := store.S
	oldBiz := biz.B
	t.Cleanup(func() {
		store.S = oldStore
		biz.B = oldBiz
		viper.Reset()
	})
	viper.Reset()
	viper.Set("sandbox.backend", "broker")
	viper.Set("sandbox.broker_owner_id", "api-primary")

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin-router.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	store.S = store.NewTestDataStore(db)
	biz.B = nil

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, installAdminRouters(engine))
	require.Nil(t, biz.B, "admin router must not call biz.NewBiz or initialize the user Sandbox runtime")
	require.True(t, routeExists(engine.Routes(), "POST", "/v1/admin/agent-runs/:id/cancel"))
}

func routeExists(routes gin.RoutesInfo, method string, path string) bool {
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
