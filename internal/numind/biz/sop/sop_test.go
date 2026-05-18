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

	creditbiz "numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// zeroBalanceCreditSvc is a minimal credit.ICreditService stub for tests that
// only need to exercise CreateRun's "free credits user with zero balance →
// typed Credits.Insufficient error" path. All other methods panic — they're
// not on the test's code path.
type zeroBalanceCreditSvc struct{ creditbiz.ICreditService }

func (zeroBalanceCreditSvc) GetBalance(_ context.Context, _ *model.User) (*creditbiz.BalanceBreakdown, error) {
	return &creditbiz.BalanceBreakdown{SubRemain: 0, BoosterRemain: 0}, nil
}

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

// newCreateRunTestDB sets up an in-memory SQLite with the hand-rolled user
// table (legacy_tier columns dropped post-T4) plus AutoMigrate for non-ENUM models.
func newCreateRunTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
			username        TEXT,
			password        TEXT,
			is_admin        INTEGER DEFAULT 0,
			status          INTEGER DEFAULT 0,
			last_login      DATETIME
		)`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.UserTemplatePermission{},
		&model.SopTemplate{},
	), "auto-migrate")

	return db
}

// TestCreateRun_FreeUserReturnsTypedError verifies that a free-tier credits
// user with zero balance hitting CreateRun gets a typed *errno.Errno
// (Credits.Insufficient, HTTP 402), not a bare error.
//
// Regression for the bug where the controller mapped a bare biz denial error
// to HTTP 500. The legacy_tier path is being deprecated; the only modern
// production denial path for a free user is the credits zero-balance check.
func TestCreateRun_FreeUserReturnsTypedError(t *testing.T) {
	db := newCreateRunTestDB(t)

	require.NoError(t, db.Exec(
		`INSERT INTO user (id, created_at, updated_at)
		 VALUES (1, ?, ?)`,
		time.Now(), time.Now(),
	).Error)

	ds := store.NewTestStore(db)
	b := sop.NewSopBiz(ds, nil, nil).WithCreditService(zeroBalanceCreditSvc{}, nil)

	_, err := b.CreateRun(context.Background(), uint(1), uint(1), "any text")
	require.Error(t, err, "free credits user with zero balance must be denied")

	var e *errno.Errno
	require.True(t, errors.As(err, &e), "biz should return *errno.Errno, got %T: %v", err, err)
	assert.Equal(t, "Credits.Insufficient", e.Code, "error code should be Credits.Insufficient")
	assert.Equal(t, 402, e.HTTP, "HTTP status should be 402 (Payment Required), not 500")
	assert.Contains(t, e.Message, "积分不足", "message should preserve Chinese reason for user display")
}

// TestCreateRun_TemplateUnauthorizedReturnsTypedError verifies that a sub-user
// who has permission records configured but not for the requested template
// gets *errno.Errno{Code:"Config.TemplateUnauthorized", HTTP:403} — not 500.
// Regression for the same 500-wrapping bug, template-permission branch.
func TestCreateRun_TemplateUnauthorizedReturnsTypedError(t *testing.T) {
	db := newCreateRunTestDB(t)

	// Parent (id=1): primary customer, passes HasTemplatePermission fast path,
	// exists here only so the sub-user's parent_user_id FK is satisfiable logically.
	require.NoError(t, db.Exec(
		`INSERT INTO user (id, created_at, updated_at)
		 VALUES (1, ?, ?)`,
		time.Now(), time.Now(),
	).Error)
	// Sub-user (id=2): credits-only billing → we reach the template check.
	require.NoError(t, db.Exec(
		`INSERT INTO user (id, created_at, updated_at, parent_user_id)
		 VALUES (2, ?, ?, 1)`,
		time.Now(), time.Now(),
	).Error)

	// Grant sub-user permission ONLY for template 99 — requesting template 1
	// must be denied.
	require.NoError(t, db.Create(&model.UserTemplatePermission{
		ParentUserID: 1,
		SubUserID:    2,
		TemplateID:   99,
	}).Error)

	ds := store.NewTestStore(db)
	b := sop.NewSopBiz(ds, nil, nil)

	_, err := b.CreateRun(context.Background(), uint(1), uint(2), "any text")
	require.Error(t, err, "sub-user without permission for template 1 must be denied")

	var e *errno.Errno
	require.True(t, errors.As(err, &e), "biz should return *errno.Errno, got %T: %v", err, err)
	assert.Equal(t, "Config.TemplateUnauthorized", e.Code)
	assert.Equal(t, 403, e.HTTP, "HTTP status should be 403 (Forbidden), not 500")
}
