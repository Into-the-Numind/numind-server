package sop_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
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

// TestCreateRun_FreeUserReturnsTypedError verifies that a free-tier user hitting
// CreateRun gets a typed *errno.Errno (SOP.RunDenied, HTTP 403), not a plain
// errors.New. Regression for the bug where controller mapped biz denial to 500.
//
// user table is hand-rolled via raw SQL because model.User has a GORM
// `type:enum('legacy_tier','credits')` tag on billing_mode that SQLite
// AutoMigrate cannot parse. Same pattern as biz/credit/grant_membership_test.go.
func TestCreateRun_FreeUserReturnsTypedError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME,
			phone           TEXT,
			nickname        TEXT,
			avatar_url      TEXT,
			parent_user_id  INTEGER,
			total_sop_runs  INTEGER DEFAULT 0,
			monthly_sop_runs INTEGER DEFAULT 0,
			monthly_reset_at DATETIME,
			user_tier       TEXT DEFAULT 'free',
			tier_expires    DATETIME,
			billing_mode    TEXT NOT NULL DEFAULT 'credits',
			username        TEXT,
			password        TEXT,
			is_admin        INTEGER DEFAULT 0,
			status          INTEGER DEFAULT 0,
			last_login      DATETIME
		)`).Error)

	require.NoError(t, db.Exec(
		`INSERT INTO user (created_at, updated_at, user_tier, billing_mode, monthly_sop_runs)
		 VALUES (?, ?, 'free', 'credits', 0)`,
		time.Now(), time.Now(),
	).Error)

	ds := store.NewTestStore(db)
	b := sop.NewSopBiz(ds, nil, nil)

	_, err = b.CreateRun(context.Background(), uint(1), uint(1), "any text")
	require.Error(t, err, "free-tier user must be denied")

	var e *errno.Errno
	require.True(t, errors.As(err, &e), "biz should return *errno.Errno, got %T: %v", err, err)
	assert.Equal(t, "SOP.RunDenied", e.Code, "error code should be SOP.RunDenied")
	assert.Equal(t, 403, e.HTTP, "HTTP status should be 403 (Forbidden), not 500")
	assert.Contains(t, e.Message, "免费用户", "message should preserve Chinese reason for user display")
}
