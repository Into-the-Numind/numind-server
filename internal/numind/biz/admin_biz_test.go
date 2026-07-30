package biz

import (
	"path/filepath"
	"testing"

	"numind-server/internal/numind/store"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewAdminBizBuildsOnlyAdminServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin-biz.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	B = nil
	b := NewAdminBiz(store.NewTestStore(db))

	require.NotNil(t, b.Sop())
	require.NotNil(t, b.Credit())
	require.NotNil(t, b.CreditService())
	require.NotNil(t, b.Pricing())
	require.NotNil(t, b.Monitor())
	require.NotNil(t, b.Announcement())

	require.Nil(t, B, "admin composition must not publish the full user-side global biz")
	require.Nil(t, b.Agents())
	require.Nil(t, b.AgentTools())
	require.Nil(t, b.Document())
	require.Nil(t, b.FeishuSvc())
	require.Nil(t, b.sandboxPool)
	require.Nil(t, b.agentRunner)
	require.Nil(t, b.agentToolRegistry)
	require.Nil(t, b.documentSvc)
	require.Nil(t, b.feishuSvc)
	require.Nil(t, b.memoryExtractor)
	require.Nil(t, b.memoryDigestCron)
	require.Nil(t, b.xhsEnricher)
	require.Nil(t, b.xhsService)
}
