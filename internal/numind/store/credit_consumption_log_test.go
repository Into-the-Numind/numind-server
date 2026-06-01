package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newReservationTestDB 建内存 SQLite 并 hand-roll credit_reservation 表
// （CreditReservation.Status/FinalizeReason 是 MySQL ENUM，SQLite 不解析 → 用 TEXT）。
func newReservationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL DEFAULT '',
    reference_id TEXT NOT NULL DEFAULT '',
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL DEFAULT 0,
    coefficient_id INTEGER,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    estimation_source TEXT NOT NULL DEFAULT 'credit_coefficient',
    token_profile_id INTEGER,
    estimated_prompt_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_completion_tokens INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    context_budget_event_id INTEGER,
    user_type_multiplier REAL NOT NULL DEFAULT 1.0
);`).Error)
	return db
}

func ptrI64(v int64) *int64 { return &v }

func TestListReconciledReservationsByUser(t *testing.T) {
	ctx := context.Background()
	db := newReservationTestDB(t)
	s := &creditStore{db: db}

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	rows := []model.CreditReservation{
		{UserID: 1, Operation: "sop_run", Status: "reconciled", ActualCostCents: ptrI64(18), CreatedAt: base},
		{UserID: 1, Operation: "salesrag_chat", Status: "reconciled", ActualCostCents: ptrI64(6), CreatedAt: base.Add(time.Hour)},
		{UserID: 1, Operation: "sop_run", Status: "reserved", ActualCostCents: nil, CreatedAt: base.Add(2 * time.Hour)},                 // 未平账 → 排除
		{UserID: 1, Operation: "ocr", Status: "refunded", ActualCostCents: ptrI64(0), CreatedAt: base.Add(3 * time.Hour)},               // 全退 → 排除
		{UserID: 1, Operation: "ocr", Status: "expired", ActualCostCents: ptrI64(5), CreatedAt: base.Add(3*time.Hour + 30*time.Minute)}, // expired → 排除
		{UserID: 1, Operation: "file_parse", Status: "reconciled", ActualCostCents: ptrI64(0), CreatedAt: base.Add(4 * time.Hour)},      // 0 成本 → 排除
		{UserID: 2, Operation: "sop_run", Status: "reconciled", ActualCostCents: ptrI64(99), CreatedAt: base},                           // 别的用户 → 隔离
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}

	got, total, err := s.ListReconciledReservationsByUser(ctx, 1, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "只数 user=1 的 reconciled 且 actual_cost_cents>0")
	require.Len(t, got, 2)
	assert.Equal(t, "salesrag_chat", got[0].Operation) // created_at DESC
	assert.Equal(t, "sop_run", got[1].Operation)
	assert.Equal(t, int64(6), *got[0].ActualCostCents)

	page1, total2, err := s.ListReconciledReservationsByUser(ctx, 1, 0, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total2)
	require.Len(t, page1, 1)
	assert.Equal(t, "salesrag_chat", page1[0].Operation)

	// 第 2 页（offset=1, limit=1）应拿到第二条（sop_run），防 Offset/Limit 被误改。
	page2, total3, err := s.ListReconciledReservationsByUser(ctx, 1, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total3)
	require.Len(t, page2, 1)
	assert.Equal(t, "sop_run", page2[0].Operation)

	// 正向越权隔离：从 user=2 视角只看到自己的 1 条记录，绝不见 user=1 的数据。
	got2, total4, err := s.ListReconciledReservationsByUser(ctx, 2, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total4)
	require.Len(t, got2, 1)
	assert.Equal(t, "sop_run", got2[0].Operation)
	assert.Equal(t, int64(99), *got2[0].ActualCostCents)
}
