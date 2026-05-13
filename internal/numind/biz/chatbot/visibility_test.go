package chatbot_test

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newChatbotVisTestDB raw SQL user 表 + AutoMigrate ChatbotConfig/Grant.
func newChatbotVisTestDB(t *testing.T) *gorm.DB {
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
			nickname        TEXT,
			parent_user_id  INTEGER,
			billing_mode    TEXT NOT NULL DEFAULT 'credits',
			user_tier       TEXT DEFAULT 'free'
		)`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.ChatbotConfig{},
		&model.ChatbotVisibilityGrant{},
	))
	return db
}

func insertCbUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	now := time.Now()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	require.NoError(t, db.Exec(
		`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`,
		now, now, parentVal,
	).Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertCbConfig(t *testing.T, db *gorm.DB, ownerID uint) uint {
	t.Helper()
	cb := &model.ChatbotConfig{
		UserID:       ownerID,
		Name:         "vis-test",
		SystemPrompt: "test",
		Status:       model.ChatbotStatusPublished,
	}
	require.NoError(t, db.Create(cb).Error)
	return cb.ID
}

// TestUpdateChatbotVisibility_Smoke 冒烟测试: 父账户配 → 回读一致.
func TestUpdateChatbotVisibility_Smoke(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, true, []uint{sub}))

	restricted, ids, err := chatbot.GetChatbotVisibility(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.True(t, restricted)
	assert.ElementsMatch(t, []uint{sub}, ids)
}

// TestUpdateChatbotVisibility_TurnOffPreservesGrants D3 保留语义.
func TestUpdateChatbotVisibility_TurnOffPreservesGrants(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub1 := insertCbUser(t, db, &parent)
	sub2 := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, true, []uint{sub1, sub2}))
	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, false, nil))
	restricted, ids, err := chatbot.GetChatbotVisibility(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.False(t, restricted)
	assert.ElementsMatch(t, []uint{sub1, sub2}, ids, "D3: grants preserved after turn off")
}

// TestUpdateChatbotVisibility_NonOwner ⚠️ owner 字段差异: chatbot.UserID 直接比较 (非指针).
func TestUpdateChatbotVisibility_NonOwner(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent1 := insertCbUser(t, db, nil)
	parent2 := insertCbUser(t, db, nil)
	cbID := insertCbConfig(t, db, parent1)

	err := chatbot.UpdateChatbotVisibility(ctx, s, parent2, cbID, true, nil)
	assert.ErrorIs(t, err, errno.ErrEntityNotOwnedByCaller)
}

// TestUpdateChatbotVisibility_SubUserCallerDenied 子账户调用 → 403.
func TestUpdateChatbotVisibility_SubUserCallerDenied(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	err := chatbot.UpdateChatbotVisibility(ctx, s, sub, cbID, true, nil)
	assert.ErrorIs(t, err, errno.ErrVisibilityPermissionDenied)
}

// TestIsChatbotVisibleToUser_ShortCircuit 短路 + parent bypass.
func TestIsChatbotVisibleToUser_ShortCircuit(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	visible, err := chatbot.IsChatbotVisibleToUser(ctx, s, sub, cbID)
	require.NoError(t, err)
	assert.True(t, visible, "default false → visible")

	visible, err = chatbot.IsChatbotVisibleToUser(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.True(t, visible, "parent bypass")
}

// TestUpdateChatbotVisibility_IdempotentReplay 验证幂等: 同一 PUT 连续 2 次, 第二次无副作用.
// 这是 spec §10.2 必需的幂等测试, 也是 P0-2 双路径删除模式的关键回归 (Unscoped 物理删让重复 PUT 不会唯一冲突).
func TestUpdateChatbotVisibility_IdempotentReplay(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, true, []uint{sub}))
	// 第二次相同请求, 应成功 (Unscoped 物理删覆盖旧记录, 无唯一冲突)
	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, true, []uint{sub}),
		"idempotent replay should succeed; ReplaceGrantsTx Unscoped delete avoids unique conflict")

	_, ids, err := chatbot.GetChatbotVisibility(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint{sub}, ids)
}

// TestUpdateChatbotVisibility_TurnOnEmpty 验证 spec §2.5 I-2 不变量 (对称 SOP 版):
// restricted=true 且 sub_user_ids=[] → 白名单严格全拒, 全部子用户都看不到.
func TestUpdateChatbotVisibility_TurnOnEmpty(t *testing.T) {
	db := newChatbotVisTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertCbUser(t, db, nil)
	sub := insertCbUser(t, db, &parent)
	cbID := insertCbConfig(t, db, parent)

	require.NoError(t, chatbot.UpdateChatbotVisibility(ctx, s, parent, cbID, true, []uint{}))

	visible, err := chatbot.IsChatbotVisibleToUser(ctx, s, sub, cbID)
	require.NoError(t, err)
	assert.False(t, visible, "I-2: visibility_restricted=true + grant=0 → strict deny-all for subs")

	// 父账户 bypass
	visible, err = chatbot.IsChatbotVisibleToUser(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.True(t, visible, "parent always visible")

	restricted, ids, err := chatbot.GetChatbotVisibility(ctx, s, parent, cbID)
	require.NoError(t, err)
	assert.True(t, restricted)
	assert.Empty(t, ids, "empty whitelist")
}
