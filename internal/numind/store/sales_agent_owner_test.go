package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

func setupSalesAgentOwnerStoreTest(t *testing.T) (ISalesAgentOwnerStore, *gorm.DB) {
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/sao_test.db?_busy_timeout=5000"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SalesAgentOwner{}))
	return NewSalesAgentOwnerStore(db), db
}

func TestSalesAgentOwner_Exists_True(t *testing.T) {
	s, db := setupSalesAgentOwnerStoreTest(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: 30}).Error)

	exists, err := s.Exists(ctx, 30)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestSalesAgentOwner_Exists_False(t *testing.T) {
	s, _ := setupSalesAgentOwnerStoreTest(t)
	ctx := context.Background()

	exists, err := s.Exists(ctx, 30)
	require.NoError(t, err)
	require.False(t, exists, "empty table 必须返回 (false, nil), 不能是 ErrRecordNotFound")
}

func TestSalesAgentOwner_Exists_DifferentParentID(t *testing.T) {
	s, db := setupSalesAgentOwnerStoreTest(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: 30}).Error)

	exists, err := s.Exists(ctx, 1) // admin 不在表中
	require.NoError(t, err)
	require.False(t, exists)
}

func TestSalesAgentOwner_Exists_DBClosed(t *testing.T) {
	s, db := setupSalesAgentOwnerStoreTest(t)
	sqlDB, _ := db.DB()
	sqlDB.Close() // 模拟 DB 错误

	exists, err := s.Exists(context.Background(), 30)
	require.Error(t, err)
	require.False(t, exists)
}
