package chatbot_test

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newChatbotListFilterTestDB 创建 ListVisibleChatbotsWithPermission 4 象限矩阵测试用 SQLite DB.
func newChatbotListFilterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME,
			username        TEXT,
			parent_user_id  INTEGER,
			billing_mode    TEXT NOT NULL DEFAULT 'credits',
			user_tier       TEXT DEFAULT 'free'
		)`).Error)

	require.NoError(t, db.Exec(`
		CREATE TABLE user_chatbot_permission (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			sub_user_id INTEGER NOT NULL,
			chatbot_id  INTEGER NOT NULL,
			created_at  DATETIME,
			UNIQUE (sub_user_id, chatbot_id)
		)`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.ChatbotConfig{},
		&model.ChatbotVisibilityGrant{},
	))
	return db
}

func insertCbFilterUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	var pv interface{}
	if parentID != nil {
		pv = *parentID
	}
	require.NoError(t, db.Exec(
		`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`,
		time.Now(), time.Now(), pv,
	).Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertCbFilterConfig(t *testing.T, db *gorm.DB, ownerID uint, name string, visibilityRestricted bool) uint {
	t.Helper()
	cb := &model.ChatbotConfig{
		UserID:               ownerID,
		Name:                 name,
		SystemPrompt:         "test",
		Status:               model.ChatbotStatusPublished,
		VisibilityRestricted: visibilityRestricted,
	}
	require.NoError(t, db.Create(cb).Error)
	return cb.ID
}

// TestListVisibleChatbotsWithPermission_FourQuadrants 4 象限矩阵 (对称 Task 11 SOP 版).
func TestListVisibleChatbotsWithPermission_FourQuadrants(t *testing.T) {
	db := newChatbotListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbFilterUser(t, db, nil)
	sub := insertCbFilterUser(t, db, &parent)

	// 5 chatbot:
	//   cb100 (V/A): visibility=false, sub 在 run-perm → 列表 + HasPermission=true
	//   cb101 (V/D): visibility=false, sub 不在 run-perm → 列表 + HasPermission=false
	//   cb102 (H/A): visibility=true 不在 vis-set, 在 run-perm → 不在列表
	//   cb103 (H/D): visibility=true 不在 vis-set, 不在 run-perm → 不在列表
	//   cb104 (I/R): visibility=true 在 vis-set, 在 run-perm → 列表 + HasPermission=true
	cb100 := insertCbFilterConfig(t, db, parent, "VA-no-restrict-allowed", false)
	cb101 := insertCbFilterConfig(t, db, parent, "VD-no-restrict-denied", false)
	cb102 := insertCbFilterConfig(t, db, parent, "HA-restrict-allowed", true)
	cb103 := insertCbFilterConfig(t, db, parent, "HD-restrict-denied", true)
	cb104 := insertCbFilterConfig(t, db, parent, "IR-restrict-in-whitelist", true)

	// run-perm 白名单: cb100, cb102, cb104
	for _, cbID := range []uint{cb100, cb102, cb104} {
		require.NoError(t, db.Exec(
			`INSERT INTO user_chatbot_permission (sub_user_id, chatbot_id, created_at) VALUES (?, ?, ?)`,
			sub, cbID, time.Now(),
		).Error)
	}

	// visibility 白名单: 仅 cb104 给 sub
	require.NoError(t, db.Create(&model.ChatbotVisibilityGrant{
		ParentUserID: parent,
		SubUserID:    sub,
		ChatbotID:    cb104,
	}).Error)

	subUser := &model.User{ParentUserID: &parent}
	subUser.ID = sub

	b := chatbot.NewChatbotBiz(ds, nil, nil)
	items, err := b.ListVisibleChatbotsWithPermission(ctx, subUser)
	require.NoError(t, err)

	gotIDs := make(map[uint]bool)
	gotPerms := make(map[uint]bool)
	for _, it := range items {
		gotIDs[it.ID] = true
		gotPerms[it.ID] = it.HasPermission
	}

	assert.Len(t, items, 3, "should have 3 visible items (V/A + V/D + I/R)")
	assert.True(t, gotIDs[cb100], "V/A cb100 should be visible")
	assert.True(t, gotIDs[cb101], "V/D cb101 should be visible (visibility=false)")
	assert.False(t, gotIDs[cb102], "H/A cb102 should be HIDDEN (visibility filter)")
	assert.False(t, gotIDs[cb103], "H/D cb103 should be HIDDEN (visibility filter)")
	assert.True(t, gotIDs[cb104], "I/R cb104 should be visible (in vis whitelist)")

	assert.True(t, gotPerms[cb100], "V/A HasPermission=true")
	assert.False(t, gotPerms[cb101], "V/D HasPermission=false")
	assert.True(t, gotPerms[cb104], "I/R HasPermission=true")
}

// TestListVisibleChatbotsWithPermission_ParentBypass 父账户 bypass visibility 过滤.
func TestListVisibleChatbotsWithPermission_ParentBypass(t *testing.T) {
	db := newChatbotListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbFilterUser(t, db, nil)
	cbID := insertCbFilterConfig(t, db, parent, "restricted-no-grant", true)

	parentUser := &model.User{}
	parentUser.ID = parent

	b := chatbot.NewChatbotBiz(ds, nil, nil)
	items, err := b.ListVisibleChatbotsWithPermission(ctx, parentUser)
	require.NoError(t, err)

	assert.Len(t, items, 1, "parent bypasses visibility filter")
	assert.Equal(t, cbID, items[0].ID)
	assert.True(t, items[0].HasPermission, "parent always HasPermission=true")
}
