package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// migrateReservationSchema 为 SQLite 单元测试创建 reservation 相关两张表。
// GORM 的 AutoMigrate 会把 struct tag 里的 `type:enum(...)` 原样写进 CREATE TABLE
// SQL，SQLite 不支持 ENUM 类型所以会失败。这里用 raw SQL 显式建表（ENUM 退化为
// TEXT），对应字段的真实 ENUM DDL 由 migrations/20260419_10020{0,0}*.sql 在 MySQL
// 上创建（Task A.4 验证）。
//
// 注：此 fixture 只用于 model 层单元测试覆盖字段映射与基础唯一索引行为。
func migrateReservationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ddl := []string{
		`CREATE TABLE credit_reservation (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL,
			reference_type    TEXT    NOT NULL,
			reference_id      TEXT    NOT NULL,
			operation         TEXT    NOT NULL,
			reserved_credits  INTEGER NOT NULL,
			coefficient_id    INTEGER NOT NULL,
			status            TEXT    NOT NULL DEFAULT 'reserved',
			actual_cost_cents INTEGER,
			delta             INTEGER,
			finalize_reason   TEXT,
			idempotency_key   TEXT,
			reconciled_at     DATETIME,
			created_at        DATETIME,
			updated_at        DATETIME
		)`,
		`CREATE UNIQUE INDEX uk_idempotency_key ON credit_reservation (idempotency_key)`,
		`CREATE INDEX idx_user_status ON credit_reservation (user_id, status, created_at)`,
		`CREATE INDEX idx_status_created ON credit_reservation (status, created_at)`,
		`CREATE INDEX idx_coefficient ON credit_reservation (coefficient_id)`,
		`CREATE TABLE credit_reservation_item (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			reservation_id     INTEGER NOT NULL,
			package_id         INTEGER NOT NULL,
			credits            INTEGER NOT NULL,
			package_type       TEXT    NOT NULL,
			package_expires_at DATETIME NOT NULL,
			seq                INTEGER NOT NULL,
			created_at         DATETIME
		)`,
		`CREATE UNIQUE INDEX uk_reservation_seq ON credit_reservation_item (reservation_id, seq)`,
		`CREATE INDEX idx_reservation ON credit_reservation_item (reservation_id)`,
		`CREATE INDEX idx_package ON credit_reservation_item (package_id, created_at)`,
	}
	for _, s := range ddl {
		require.NoError(t, db.Exec(s).Error)
	}
}

func TestCreditReservation_ColumnsAndTables(t *testing.T) {
	db := newTestDB(t)
	migrateReservationSchema(t, db)

	assert.True(t, db.Migrator().HasTable(&CreditReservation{}))
	assert.True(t, db.Migrator().HasTable(&CreditReservationItem{}))

	for _, col := range []string{
		"id", "user_id", "reference_type", "reference_id", "operation",
		"reserved_credits", "coefficient_id", "status", "actual_cost_cents",
		"delta", "finalize_reason", "idempotency_key", "reconciled_at",
		"created_at", "updated_at",
	} {
		assert.True(t, db.Migrator().HasColumn(&CreditReservation{}, col),
			"credit_reservation should have column %s", col)
	}

	for _, col := range []string{
		"id", "reservation_id", "package_id", "credits",
		"package_type", "package_expires_at", "seq", "created_at",
	} {
		assert.True(t, db.Migrator().HasColumn(&CreditReservationItem{}, col),
			"credit_reservation_item should have column %s", col)
	}
}

func TestCreditReservation_TableNames(t *testing.T) {
	assert.Equal(t, "credit_reservation", CreditReservation{}.TableName())
	assert.Equal(t, "credit_reservation_item", CreditReservationItem{}.TableName())
}

func TestCreditReservation_CreateWithItemsAndSeqUniqueness(t *testing.T) {
	db := newTestDB(t)
	migrateReservationSchema(t, db)

	coeffID := uint64(1)
	future := time.Now().Add(30 * 24 * time.Hour)
	rsv := &CreditReservation{
		UserID:          42,
		ReferenceType:   "sop_run",
		ReferenceID:     "run-abc-123",
		Operation:       "sop_run",
		ReservedCredits: 150,
		CoefficientID:   &coeffID,
		Status:          "reserved",
		Items: []CreditReservationItem{
			{PackageID: 10, Credits: 50, PackageType: "trial", PackageExpiresAt: future, Seq: 1},
			{PackageID: 20, Credits: 60, PackageType: "subscription", PackageExpiresAt: future, Seq: 2},
			{PackageID: 30, Credits: 40, PackageType: "booster", PackageExpiresAt: future, Seq: 3},
		},
	}
	require.NoError(t, db.Create(rsv).Error)
	assert.NotZero(t, rsv.ID)

	// 验证 items 通过外键 ReservationID 正确写入
	var items []CreditReservationItem
	require.NoError(t, db.Where("reservation_id = ?", rsv.ID).Order("seq ASC").Find(&items).Error)
	require.Len(t, items, 3)
	assert.Equal(t, 1, items[0].Seq)
	assert.Equal(t, 2, items[1].Seq)
	assert.Equal(t, 3, items[2].Seq)
	assert.Equal(t, uint64(10), items[0].PackageID)
	assert.Equal(t, uint64(20), items[1].PackageID)
	assert.Equal(t, uint64(30), items[2].PackageID)

	// 验证 Preload 关联加载
	var loaded CreditReservation
	require.NoError(t, db.Preload("Items").First(&loaded, rsv.ID).Error)
	assert.Len(t, loaded.Items, 3)

	// 同一 reservation 插入重复 seq 必须失败（唯一索引 uk_reservation_seq）
	dup := &CreditReservationItem{
		ReservationID:    rsv.ID,
		PackageID:        99,
		Credits:          1,
		PackageType:      "booster",
		PackageExpiresAt: future,
		Seq:              1, // 重复 seq
	}
	assert.Error(t, db.Create(dup).Error, "duplicate (reservation_id, seq) should violate unique index")

	// 不同 reservation 的相同 seq 应当允许
	rsv2 := &CreditReservation{
		UserID:          42,
		ReferenceType:   "sop_run",
		ReferenceID:     "run-xyz-456",
		Operation:       "sop_run",
		ReservedCredits: 10,
		CoefficientID:   &coeffID,
		Status:          "reserved",
	}
	require.NoError(t, db.Create(rsv2).Error)
	okItem := &CreditReservationItem{
		ReservationID:    rsv2.ID,
		PackageID:        11,
		Credits:          10,
		PackageType:      "trial",
		PackageExpiresAt: future,
		Seq:              1,
	}
	assert.NoError(t, db.Create(okItem).Error, "same seq across different reservations should be allowed")
}

func TestCreditReservation_IdempotencyKeyUniqueness(t *testing.T) {
	db := newTestDB(t)
	migrateReservationSchema(t, db)

	coeffID := uint64(1)
	key := "idem-key-1"
	r1 := &CreditReservation{
		UserID:          1,
		ReferenceType:   "sop_run",
		ReferenceID:     "r1",
		Operation:       "sop_run",
		ReservedCredits: 10,
		CoefficientID:   &coeffID,
		Status:          "reserved",
		IdempotencyKey:  &key,
	}
	require.NoError(t, db.Create(r1).Error)

	r2 := &CreditReservation{
		UserID:          1,
		ReferenceType:   "sop_run",
		ReferenceID:     "r2",
		Operation:       "sop_run",
		ReservedCredits: 20,
		CoefficientID:   &coeffID,
		Status:          "reserved",
		IdempotencyKey:  &key, // same key
	}
	assert.Error(t, db.Create(r2).Error, "duplicate idempotency_key should fail")

	// NULL idempotency_key 允许多行
	n1 := &CreditReservation{
		UserID: 2, ReferenceType: "sop_run", ReferenceID: "n1",
		Operation: "sop_run", ReservedCredits: 5, CoefficientID: &coeffID, Status: "reserved",
	}
	n2 := &CreditReservation{
		UserID: 2, ReferenceType: "sop_run", ReferenceID: "n2",
		Operation: "sop_run", ReservedCredits: 5, CoefficientID: &coeffID, Status: "reserved",
	}
	assert.NoError(t, db.Create(n1).Error)
	assert.NoError(t, db.Create(n2).Error, "NULL idempotency_key should allow multiple rows")
}

func TestCreditReservation_OptionalPointerFields(t *testing.T) {
	db := newTestDB(t)
	migrateReservationSchema(t, db)

	// 创建时不填 actual_cost_cents / delta / finalize_reason / reconciled_at / idempotency_key
	// 应允许 NULL
	coeffID := uint64(2)
	rsv := &CreditReservation{
		UserID:          9,
		ReferenceType:   "sop_chat",
		ReferenceID:     "chat-1",
		Operation:       "sop_chat",
		ReservedCredits: 5,
		CoefficientID:   &coeffID,
		Status:          "reserved",
	}
	require.NoError(t, db.Create(rsv).Error)

	var got CreditReservation
	require.NoError(t, db.First(&got, rsv.ID).Error)
	assert.Nil(t, got.ActualCostCents)
	assert.Nil(t, got.Delta)
	assert.Nil(t, got.FinalizeReason)
	assert.Nil(t, got.ReconciledAt)
	assert.Nil(t, got.IdempotencyKey)

	// reconcile 路径：填充 actual/delta/reason/reconciled_at
	actual := int64(450)
	delta := int64(-50)
	reason := "normal"
	now := time.Now()
	got.ActualCostCents = &actual
	got.Delta = &delta
	got.FinalizeReason = &reason
	got.ReconciledAt = &now
	got.Status = "reconciled"
	require.NoError(t, db.Save(&got).Error)

	var after CreditReservation
	require.NoError(t, db.First(&after, rsv.ID).Error)
	require.NotNil(t, after.ActualCostCents)
	assert.Equal(t, int64(450), *after.ActualCostCents)
	require.NotNil(t, after.Delta)
	assert.Equal(t, int64(-50), *after.Delta)
	require.NotNil(t, after.FinalizeReason)
	assert.Equal(t, "normal", *after.FinalizeReason)
	assert.Equal(t, "reconciled", after.Status)
}
