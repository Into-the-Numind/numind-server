package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newMonitorTestDB creates an isolated in-memory SQLite DB for monitor store tests.
func newMonitorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.MonitorConfig{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestUpsertConfig_NotifyOnUpdateFalse_NewRecord verifies that when UpsertConfig
// creates a new MonitorConfig (first time for a user), setting notify_on_update=false
// is actually persisted as false. Regression for the GORM `default:true` gotcha:
// without the UpdateColumn fixup, FirstOrCreate falls back to the DB default of true
// for model.MonitorConfig.NotifyOnUpdate.
func TestUpsertConfig_NotifyOnUpdateFalse_NewRecord(t *testing.T) {
	db := newMonitorTestDB(t)
	s := &monitorStore{db: db}

	cfg := &model.MonitorConfig{
		UserID:         100,
		NotifyOnUpdate: false, // explicitly false for a new record
	}

	err := s.UpsertConfig(context.Background(), cfg)
	require.NoError(t, err)
	assert.NotZero(t, cfg.ID, "config should have been assigned an ID")

	assert.False(t, cfg.NotifyOnUpdate,
		"returned config should have notify_on_update=false")

	// Double-check the actual DB row.
	var row model.MonitorConfig
	require.NoError(t, db.Where("user_id = ?", cfg.UserID).First(&row).Error)
	assert.False(t, row.NotifyOnUpdate,
		"DB row should persist notify_on_update=false (not defaulted to true)")
}

// TestUpsertConfig_NotifyOnUpdateFalse_ExistingRecord verifies that updating an
// existing MonitorConfig to notify_on_update=false also persists correctly.
func TestUpsertConfig_NotifyOnUpdateFalse_ExistingRecord(t *testing.T) {
	db := newMonitorTestDB(t)
	s := &monitorStore{db: db}

	// Create initial config with notify_on_update=true.
	cfg := &model.MonitorConfig{
		UserID:         101,
		NotifyOnUpdate: true,
	}
	require.NoError(t, s.UpsertConfig(context.Background(), cfg))
	require.True(t, cfg.NotifyOnUpdate, "initial config should have notify_on_update=true")

	// Now update to false.
	cfg.NotifyOnUpdate = false
	err := s.UpsertConfig(context.Background(), cfg)
	require.NoError(t, err)

	var row model.MonitorConfig
	require.NoError(t, db.Where("user_id = ?", uint(101)).First(&row).Error)
	assert.False(t, row.NotifyOnUpdate,
		"DB row should persist notify_on_update=false after update")
}

// TestUpsertConfig_NotifyOnUpdateTrue verifies that creating with notify_on_update=true
// still works correctly.
func TestUpsertConfig_NotifyOnUpdateTrue(t *testing.T) {
	db := newMonitorTestDB(t)
	s := &monitorStore{db: db}

	cfg := &model.MonitorConfig{
		UserID:         102,
		NotifyOnUpdate: true,
	}

	require.NoError(t, s.UpsertConfig(context.Background(), cfg))

	var row model.MonitorConfig
	require.NoError(t, db.Where("user_id = ?", uint(102)).First(&row).Error)
	assert.True(t, row.NotifyOnUpdate, "DB row should have notify_on_update=true")
}
