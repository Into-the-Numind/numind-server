package sop_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newSopTestDB creates an isolated in-memory SQLite DB for SOP biz tests.
func newSopTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestCreateTemplateByUser_TrailingChatFalse verifies that a template created
// with trailing_chat_enabled=false is actually persisted as false.
// Regression for the GORM `default:true` gotcha: without the UpdateColumn
// fixup, GORM v2 treats bool zero value (false) as "not set" and falls back
// to the DB default of true.
func TestCreateTemplateByUser_TrailingChatFalse(t *testing.T) {
	db := newSopTestDB(t)
	ds := store.NewTestStore(db)

	// nil executor and creditBiz are safe: CreateTemplateByUser does not use them.
	b := sop.NewSopBiz(ds, nil, nil)

	falseVal := false
	req := &sop.CreateTemplateByUserReq{
		Name:                "test-template",
		TrailingChatEnabled: &falseVal,
	}

	tmpl, err := b.CreateTemplateByUser(context.Background(), uint(1), req)
	require.NoError(t, err)
	require.NotNil(t, tmpl)

	assert.False(t, tmpl.TrailingChatEnabled,
		"returned template should have trailing_chat_enabled=false")

	// Double-check DB row.
	var row model.SopTemplate
	require.NoError(t, db.First(&row, tmpl.ID).Error)
	assert.False(t, row.TrailingChatEnabled,
		"DB row should persist trailing_chat_enabled=false (not defaulted to true)")
}

// TestCreateTemplateByUser_TrailingChatDefaultsToTrue verifies that omitting
// trailing_chat_enabled defaults to true (existing behaviour).
func TestCreateTemplateByUser_TrailingChatDefaultsToTrue(t *testing.T) {
	db := newSopTestDB(t)
	ds := store.NewTestStore(db)

	b := sop.NewSopBiz(ds, nil, nil)

	req := &sop.CreateTemplateByUserReq{
		Name:                "test-default",
		TrailingChatEnabled: nil, // omitted → should default to true
	}

	tmpl, err := b.CreateTemplateByUser(context.Background(), uint(2), req)
	require.NoError(t, err)
	assert.True(t, tmpl.TrailingChatEnabled, "omitted trailing_chat_enabled should default to true")
}
