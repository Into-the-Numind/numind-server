// Package chatbot_test — biz/chatbot 运行时守卫测试（child-run-permission Task 4 +
// chatbot-list-symmetric-with-sop hotfix）。
//
// 覆盖场景：
//   - ListVisibleChatbots：子账号看到父账号全部 published（与 SOP 对称，不按白名单隐藏）
//   - CheckChatbotPermission：父账号 bypass / 子账号授权命中 / 子账号未授权 / draft chatbot
//   - CreateSession：未授权拒绝 / 已授权成功
//   - ChatStream：撤权后即时生效（P1-B 关键回归）
package chatbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// makeUser 构造一个带 ID / ParentUserID 的 *model.User（model.User 嵌入 gorm.Model，
// 所以 ID 要通过 gorm.Model 字面量设置）。
func makeUser(id uint, parentID *uint) *model.User {
	return &model.User{
		Model:        gorm.Model{ID: id},
		ParentUserID: parentID,
	}
}

// newChatbotTestDB 建立运行时守卫测试用的 in-memory SQLite，包含：
//   - user（hand-rolled，MySQL enum billing_mode 无法 AutoMigrate）
//   - chatbot_config / chatbot_session / chatbot_message（ChatbotConfig 有
//     gorm.DeletedAt，可用 AutoMigrate）
//   - user_chatbot_permission（hand-rolled，无 gorm.Model 嵌入，字段定义在
//     model/user_chatbot_permission.go）
//
// 风格参考：biz/sop/sop_test.go 的 newCreateRunTestDB + store/customer_test.go
// 的 newPermissionTestDB。
func newChatbotTestDB(t *testing.T) *gorm.DB {
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
            nickname        TEXT,
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
		&model.ChatbotSession{},
		&model.ChatbotMessage{},
	))
	return db
}

// insertUserRow 插入一条 user 行，返回 ID。parentID=nil 表示父账号。
func insertUserRow(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(`INSERT INTO user (parent_user_id) VALUES (?)`, parentVal)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// insertChatbotConfig 插入一个智能体配置行，状态默认为 published 以便 C 端访问测试。
func insertChatbotConfig(t *testing.T, db *gorm.DB, ownerID uint, name, status string) uint {
	t.Helper()
	c := model.ChatbotConfig{
		UserID:       ownerID,
		Name:         name,
		SystemPrompt: "test prompt",
		Status:       status,
	}
	require.NoError(t, db.Create(&c).Error)
	return c.ID
}

// grantChatbotPerm 插入白名单行（子账号 sub 授权 chatbot）。
func grantChatbotPerm(t *testing.T, db *gorm.DB, subID, chatbotID uint) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO user_chatbot_permission (sub_user_id, chatbot_id, created_at)
         VALUES (?, ?, CURRENT_TIMESTAMP)`,
		subID, chatbotID,
	).Error)
}

// revokeChatbotPerm 物理删除白名单行（模拟父账号撤权）。
func revokeChatbotPerm(t *testing.T, db *gorm.DB, subID, chatbotID uint) {
	t.Helper()
	require.NoError(t, db.Exec(
		`DELETE FROM user_chatbot_permission WHERE sub_user_id = ? AND chatbot_id = ?`,
		subID, chatbotID,
	).Error)
}

// newChatbotBiz 构造 biz 实例，vectorStore/embedder 传 nil —— ListVisibleChatbots
// / CreateSession / ChatStream 的权限守卫路径不会走到它们。
func newChatbotBiz(db *gorm.DB) chatbot.IChatbotBiz {
	return chatbot.NewChatbotBiz(store.NewTestStore(db), nil, nil)
}

// ============================================================================
// ListVisibleChatbots
// ============================================================================

// TestListVisibleChatbots_ChildSeesAllPublished 子账号看到父账号全部 published chatbot。
// 与 SOP /v1/sop/templates 对称：列表不按白名单隐藏，点击时走 CheckChatbotPermission。
// 未授权的 chatbot 仍然可见，但点击后前端会弹"无权限"，运行时 CreateSession/ChatStream 拒绝。
func TestListVisibleChatbots_ChildSeesAllPublished(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)

	// 父账号发布 3 个智能体
	cb1 := insertChatbotConfig(t, db, parent, "bot1", model.ChatbotStatusPublished)
	cb2 := insertChatbotConfig(t, db, parent, "bot2", model.ChatbotStatusPublished)
	cb3 := insertChatbotConfig(t, db, parent, "bot3", model.ChatbotStatusPublished)

	// 子账号仅被授权 cb1 和 cb3；cb2 未授权
	grantChatbotPerm(t, db, child, cb1)
	grantChatbotPerm(t, db, child, cb3)

	childUser := makeUser(child, &parent)

	got, err := b.ListVisibleChatbots(context.Background(), childUser)
	require.NoError(t, err)

	ids := make([]uint, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	assert.ElementsMatch(t, []uint{cb1, cb2, cb3}, ids,
		"子账号应看到父账号全部 published chatbot（含未授权的 cb2），权限校验在点击时进行")
}

// TestListVisibleChatbots_ParentAll 父账号（ParentUserID == nil）不走白名单过滤，
// 能看到自己所有已发布的 chatbot。
func TestListVisibleChatbots_ParentAll(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cb1 := insertChatbotConfig(t, db, parent, "bot1", model.ChatbotStatusPublished)
	cb2 := insertChatbotConfig(t, db, parent, "bot2", model.ChatbotStatusPublished)
	// 一个 draft 的不应返回（既有语义不变）
	_ = insertChatbotConfig(t, db, parent, "draft-bot", model.ChatbotStatusDraft)

	// 父账号 user：ParentUserID == nil
	parentUser := makeUser(parent, nil)

	got, err := b.ListVisibleChatbots(context.Background(), parentUser)
	require.NoError(t, err)

	ids := make([]uint, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	assert.ElementsMatch(t, []uint{cb1, cb2}, ids, "父账号应返回所有已发布的 chatbot（不受白名单限制）")
}

// ============================================================================
// CheckChatbotPermission
// ============================================================================

// TestCheckChatbotPermission_ParentBypass 父账号对任意 published chatbot 都有权限（bypass 白名单）。
func TestCheckChatbotPermission_ParentBypass(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)

	ok, err := b.CheckChatbotPermission(context.Background(), parent, cbID)
	require.NoError(t, err)
	assert.True(t, ok, "父账号对 published chatbot 应始终有权限")
}

// TestCheckChatbotPermission_ChildGranted 子账号命中白名单 → true。
func TestCheckChatbotPermission_ChildGranted(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	grantChatbotPerm(t, db, child, cbID)

	ok, err := b.CheckChatbotPermission(context.Background(), child, cbID)
	require.NoError(t, err)
	assert.True(t, ok, "子账号命中白名单应返回有权限")
}

// TestCheckChatbotPermission_ChildDenied 子账号无白名单记录 → false。
func TestCheckChatbotPermission_ChildDenied(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	// 不授权

	ok, err := b.CheckChatbotPermission(context.Background(), child, cbID)
	require.NoError(t, err)
	assert.False(t, ok, "子账号无白名单记录应返回无权限（default-deny）")
}

// TestCheckChatbotPermission_DraftDenied 任何用户对 draft chatbot 都无运行权限。
func TestCheckChatbotPermission_DraftDenied(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "draft-bot", model.ChatbotStatusDraft)

	ok, err := b.CheckChatbotPermission(context.Background(), parent, cbID)
	require.NoError(t, err)
	assert.False(t, ok, "draft chatbot 即使对父账号也应无运行权限")
}

// TestCheckChatbotPermission_NotFound chatbot 不存在返回 false + nil error。
func TestCheckChatbotPermission_NotFound(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)

	ok, err := b.CheckChatbotPermission(context.Background(), parent, 9999)
	require.NoError(t, err, "不存在的 chatbot 不应返回错误")
	assert.False(t, ok)
}

// ============================================================================
// CreateSession
// ============================================================================

// TestCreateSession_ChatbotRunDenied 子账号对未授权的 chatbot 调 CreateSession
// → 返回 ErrChatbotRunDenied，且不创建 session 行。
func TestCreateSession_ChatbotRunDenied(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	// 不授权 → 子账号无白名单记录

	session, err := b.CreateSession(context.Background(), child, cbID)
	require.Error(t, err)
	assert.Nil(t, session)
	assert.True(t, errors.Is(err, errno.ErrChatbotRunDenied),
		"未授权 chatbot 创建会话必须返回 ErrChatbotRunDenied，实际=%v", err)

	// 确认无 session 行写入 DB
	var count int64
	require.NoError(t, db.Model(&model.ChatbotSession{}).Count(&count).Error)
	assert.EqualValues(t, 0, count, "权限检查应在任何 DB 写操作前阻断")
}

// TestCreateSession_ChatbotAllowed 子账号对已授权的 chatbot 调 CreateSession → 成功。
func TestCreateSession_ChatbotAllowed(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	grantChatbotPerm(t, db, child, cbID)

	session, err := b.CreateSession(context.Background(), child, cbID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, child, session.UserID)
	assert.Equal(t, cbID, session.ChatbotID)
	assert.Equal(t, "active", session.Status)
}

// ============================================================================
// ChatStream — P1-B 关键：撤销即时生效
// ============================================================================

// TestChatStream_AfterRevoke_Denied 【S3 Gate review P1-B】
//
// 场景：
//  1. 父账号发布 chatbot，授权给子账号
//  2. 子账号成功创建 session（此时持有 session 所有权）
//  3. 父账号撤权（删除白名单行）
//  4. 子账号用原 session 调 ChatStream → 应返回 ErrChatbotRunDenied
//
// 这验证了 PRD AS-5「撤销即时生效」：session 所有权 ≠ 当前运行权限，ChatStream
// 必须每次都重新检查白名单（解耦读权限 vs 运行权限）。
//
// handler 传 no-op：测试只关心权限检查是否在 LLM 调用前阻断，不需要真的跑 LLM。
// 如果权限检查路径有 bug（比如漏了这一步），测试会进入 aiservice.ChatStream，
// 在没有 AI Gateway 配置的单元测试环境下失败（不是 ErrChatbotRunDenied）。
func TestChatStream_AfterRevoke_Denied(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	// 1. 建立父子 + chatbot + 授权
	parent := insertUserRow(t, db, nil)
	child := insertUserRow(t, db, &parent)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	grantChatbotPerm(t, db, child, cbID)

	// 2. 子账号创建 session（在授权期内）
	session, err := b.CreateSession(context.Background(), child, cbID)
	require.NoError(t, err, "授权期内 CreateSession 应成功")
	require.NotNil(t, session)

	// 3. 父账号撤权（物理删除白名单行）
	revokeChatbotPerm(t, db, child, cbID)

	// 4. 子账号继续对同一个 session 调 ChatStream → 必须被权限检查阻断
	handler := func(event string, data interface{}) error { return nil }
	err = b.ChatStream(context.Background(), child, session.ID, "hello", "", false, handler)

	require.Error(t, err, "撤权后 ChatStream 必须返回错误")
	assert.True(t, errors.Is(err, errno.ErrChatbotRunDenied),
		"撤权后 ChatStream 应返回 ErrChatbotRunDenied，实际=%v。"+
			"如果实际错误来自 aiservice，说明权限守卫漏了（P1-B 回归）", err)
}
