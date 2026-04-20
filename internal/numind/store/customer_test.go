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
)

// newCustomerTestDB 创建 ListSubUsers 测试用的 SQLite DB（仅 user 表最小 schema）
func newCustomerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/customer_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            nickname        TEXT,
            username        TEXT,
            parent_user_id  INTEGER,
            billing_mode    TEXT NOT NULL DEFAULT 'credits',
            user_tier       TEXT DEFAULT 'free'
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func insertCustomerTestUser(t *testing.T, db *gorm.DB, parentID *uint, createdAt time.Time, nickname string) uint {
	t.Helper()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, nickname, parent_user_id) VALUES (?, ?, ?, ?)`,
		createdAt, createdAt, nickname, parentVal,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func TestListSubUsers_IncludesParentSelf_Ordered(t *testing.T) {
	db := newCustomerTestDB(t)
	cs := NewCustomerStore(db)

	now := time.Now()
	parent := insertCustomerTestUser(t, db, nil, now.Add(-30*24*time.Hour), "ParentSelf")
	older := insertCustomerTestUser(t, db, &parent, now.Add(-20*24*time.Hour), "ChildOlder")
	newer := insertCustomerTestUser(t, db, &parent, now.Add(-5*24*time.Hour), "ChildNewer")

	// Unrelated parent X and their child Y — must NOT appear in parent's list
	otherParent := insertCustomerTestUser(t, db, nil, now, "OtherParent")
	_ = insertCustomerTestUser(t, db, &otherParent, now, "OtherChild")

	users, total, err := cs.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "total = parent + 2 children")
	require.Len(t, users, 3)
	assert.Equal(t, parent, users[0].ID, "parent self must be first")
	assert.Equal(t, newer, users[1].ID, "newer child second (created_at DESC)")
	assert.Equal(t, older, users[2].ID, "older child third")

	for _, u := range users {
		assert.NotEqual(t, otherParent, u.ID, "otherParent must not leak")
	}
}
