package biz

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSandboxWiringBrokerBackendBuildsUserAPIBrokerPool(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("salesrag.vector_store.type", "memory")
	viper.Set("features.feishu_integration.enabled", false)
	viper.Set("agent.memory.digest.enabled", false)
	viper.Set("sandbox.backend", "broker")
	viper.Set("sandbox.broker_socket", filepath.Join(t.TempDir(), "sandboxd.sock"))
	viper.Set("sandbox.broker_owner_id", "api-primary")
	viper.Set("sandbox.pool_min", 0)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "biz-sandbox-wiring.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	b := NewBiz(store.NewTestStore(db))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b.CloseComplianceAudit(ctx)
		b.CloseMemoryExtractor(ctx)
		b.CloseDigestCron(ctx)
		b.CloseXhsEnricher(ctx)
		_ = b.CloseExternalResumeLifecycle(ctx)
		if b.sandboxPool != nil {
			_ = b.sandboxPool.Close()
		}
	})

	require.NotNil(t, b.sandboxPool)
	require.True(t, b.sandboxPool.IsEnabled())
	_, ok := b.sandboxPool.DockerClient().(sandbox.BrokerLeaseLifecycle)
	require.True(t, ok, "broker backend must not wire the direct Docker CLI client")
}
