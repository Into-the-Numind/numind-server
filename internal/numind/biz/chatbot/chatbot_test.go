// Package chatbot_test — biz/chatbot 运行时守卫测试（child-run-permission Task 4 +
// chatbot-list-symmetric-with-sop hotfix + Task 10 fragment contract）。
//
// 覆盖场景：
//   - ListVisibleChatbots：子账号看到父账号全部 published（与 SOP 对称，不按白名单隐藏）
//   - CheckChatbotPermission：父账号 bypass / 子账号授权命中 / 子账号未授权 / draft chatbot
//   - CreateSession：未授权拒绝 / 已授权成功
//   - ChatStream：撤权后即时生效（P1-B 关键回归）
//   - BuildChatContextFragments：当前消息为 critical fragment（Task 10）
//   - ChatbotChatOperation：billing operation 常量值（Task 10）
package chatbot_test

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

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"
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
//   - user（hand-rolled，post-T4 legacy_tier 列已 DROP）
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
            parent_user_id  INTEGER
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

// newChatbotBiz 构造 biz 实例，retrieveSvc 传 nil —— ListVisibleChatbots
// / CreateSession / ChatStream 的权限守卫路径不会走到检索。
func newChatbotBiz(db *gorm.DB) chatbot.IChatbotBiz {
	return chatbot.NewChatbotBiz(store.NewTestStore(db), nil)
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
	err = b.ChatStream(context.Background(), child, session.ID, "hello", nil, "", false, handler)

	require.Error(t, err, "撤权后 ChatStream 必须返回错误")
	assert.True(t, errors.Is(err, errno.ErrChatbotRunDenied),
		"撤权后 ChatStream 应返回 ErrChatbotRunDenied，实际=%v。"+
			"如果实际错误来自 aiservice，说明权限守卫漏了（P1-B 回归）", err)
}

// ============================================================================
// Task 10: Context Fragment Contract
// ============================================================================

// TestChatbotStreamBuildsCurrentMessageAsCriticalFragment verifies that
// BuildChatContextFragments places the current user message as the last fragment
// and that it is classified as RoleRecent + SourceUser + Critical=true with
// CompressNone — matching spec §9.2 "current user message → recent + critical".
func TestChatbotStreamBuildsCurrentMessageAsCriticalFragment(t *testing.T) {
	history := []model.ChatbotMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	kbChunks := []domain.KnowledgeChunk{
		{ID: "chunk-1", Content: "Knowledge chunk one.", Score: 0.8},
	}
	currentMsg := "What is the pricing?"

	frags := chatbot.BuildChatContextFragments("You are a helpful assistant.", history, currentMsg, kbChunks, 10)

	require.NotEmpty(t, frags, "fragments must not be empty")

	// Last fragment must be the current user message.
	last := frags[len(frags)-1]
	assert.Equal(t, currentMsg, last.Content, "last fragment must be the current user message")
	assert.Equal(t, cb.RoleRecent, last.Role, "current message must be RoleRecent")
	assert.Equal(t, cb.SourceUser, last.Source, "current message must be SourceUser")
	assert.True(t, last.Critical, "current message must be Critical=true")
	assert.Equal(t, cb.CompressNone, last.Compressibility, "current message must be CompressNone")

	// System fragment must be immutable.
	first := frags[0]
	assert.Equal(t, cb.RoleImmutable, first.Role, "first fragment must be the system prompt (RoleImmutable)")

	// KB evidence fragment must be RoleEvidence + SourceKB.
	var evidenceFrags []cb.ContextFragment
	for _, f := range frags {
		if f.Role == cb.RoleEvidence {
			evidenceFrags = append(evidenceFrags, f)
		}
	}
	require.Len(t, evidenceFrags, 1, "one KB chunk → one evidence fragment")
	assert.Equal(t, cb.SourceKB, evidenceFrags[0].Source, "KB fragment must be SourceKB")
	assert.Equal(t, "chunk-1", evidenceFrags[0].SourceReference, "KB fragment SourceReference must be chunk ID")
}

// TestChatbotStreamUsesChatbotChatOperation verifies that the billing operation
// constant exported from the chatbot package is "chatbot_chat", matching the
// budgetOperationMap entry and the Langfuse trace tag (spec §6.1.1 + Task 10
// normalisation requirement).
func TestChatbotStreamUsesChatbotChatOperation(t *testing.T) {
	// The constant is used in ChatStream's billing.WithBilling call.
	// Verifying the exported constant ensures refactoring doesn't silently
	// change the operation string and break the budgetOperationMap lookup.
	assert.Equal(t, "chatbot_chat", chatbot.ChatbotChatOperation,
		"ChatbotChatOperation must be 'chatbot_chat' to match budgetOperationMap")
}

// ============================================================================
// T3 helpers（rename-pin feature）
// ============================================================================

// insertChatbotSession 插入一条 chatbot_session 行，供 rename/pin 测试使用。
func insertChatbotSession(t *testing.T, db *gorm.DB, userID, chatbotID uint, title string) uint {
	t.Helper()
	session := model.ChatbotSession{
		UserID:    userID,
		ChatbotID: chatbotID,
		Title:     title,
		Status:    model.ChatbotSessionStatusActive,
	}
	require.NoError(t, db.Create(&session).Error)
	return session.ID
}

// ============================================================================
// RenameSession (T3)
// ============================================================================

// TestRenameSession_TrimEmpty_ReturnsBindError 传入纯空白 title（trim 后为空）→ ErrBind。
func TestRenameSession_TrimEmpty_ReturnsBindError(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, parent, cbID, "original title")

	err := b.RenameSession(context.Background(), parent, sessionID, "   ")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind),
		"空白 title（trim 后为空）应返回 ErrBind，实际=%v", err)
}

// TestRenameSession_OverLimit_ReturnsBindError 传入 201 字节 ASCII title → ErrBind。
func TestRenameSession_OverLimit_ReturnsBindError(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, parent, cbID, "original title")

	// 201 个 ASCII 字符（len=201 字节）
	tooLong := make([]byte, 201)
	for i := range tooLong {
		tooLong[i] = 'a'
	}

	err := b.RenameSession(context.Background(), parent, sessionID, string(tooLong))
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind),
		"超过 200 字节的 title 应返回 ErrBind，实际=%v", err)
}

// TestRenameSession_NotOwner_ReturnsForbidden session.UserID != userID → ErrForbidden。
func TestRenameSession_NotOwner_ReturnsForbidden(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	owner := insertUserRow(t, db, nil)
	other := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, owner, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, owner, cbID, "owner's session")

	err := b.RenameSession(context.Background(), other, sessionID, "hacked title")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrForbidden),
		"非 session 所有者应返回 ErrForbidden，实际=%v", err)
}

// TestRenameSession_SoftDeleted_ReturnsSessionNotFound GetSession 返回 ErrRecordNotFound → ErrSessionNotFound。
func TestRenameSession_SoftDeleted_ReturnsSessionNotFound(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	// 不插入任何 session，直接传一个不存在的 sessionID
	err := b.RenameSession(context.Background(), parent, 99999, "new title")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrSessionNotFound),
		"session 不存在时应返回 ErrSessionNotFound，实际=%v", err)
}

// ============================================================================
// PinSession (T3)
// ============================================================================

// TestPinSession_PinFirstTime pinned=true → 返回 *time.Time 非 nil。
func TestPinSession_PinFirstTime(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, parent, cbID, "my session")

	before := time.Now().Add(-time.Second)
	pinnedAt, err := b.PinSession(context.Background(), parent, sessionID, true)
	require.NoError(t, err)
	require.NotNil(t, pinnedAt, "pin=true 应返回非 nil 的 pinnedAt")
	assert.True(t, pinnedAt.After(before),
		"pinnedAt 应该是当前时间之后，before=%v pinnedAt=%v", before, pinnedAt)
}

// TestPinSession_Unpin pinned=false → 返回 nil，SetPinnedAt 传入 nil（取消置顶）。
func TestPinSession_Unpin(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, parent, cbID, "my session")

	// 先置顶
	_, err := b.PinSession(context.Background(), parent, sessionID, true)
	require.NoError(t, err)

	// 再取消置顶
	pinnedAt, err := b.PinSession(context.Background(), parent, sessionID, false)
	require.NoError(t, err)
	assert.Nil(t, pinnedAt, "pin=false 应返回 nil 的 pinnedAt")

	// 验证 DB 中 pinned_at 确实为 NULL
	var session model.ChatbotSession
	require.NoError(t, db.First(&session, sessionID).Error)
	assert.Nil(t, session.PinnedAt, "取消置顶后 DB 中 pinned_at 应为 NULL")
}

// TestPinSession_RepinRefreshesPinnedAt 先 pin 记录 t1，再 pin 返回 t2 > t1（EC-14 重复置顶刷新）。
func TestPinSession_RepinRefreshesPinnedAt(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	parent := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, parent, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, parent, cbID, "my session")

	// 第一次置顶
	t1, err := b.PinSession(context.Background(), parent, sessionID, true)
	require.NoError(t, err)
	require.NotNil(t, t1)

	// 稍等一个纳秒以确保时间戳不同（time.Now() 精度足够）
	// 在实际中两次调用间距远超 1ns，但为保证测试稳定增加短暂间隔
	time.Sleep(time.Millisecond)

	// 第二次置顶（重复置顶，EC-14 刷新 pinnedAt）
	t2, err := b.PinSession(context.Background(), parent, sessionID, true)
	require.NoError(t, err)
	require.NotNil(t, t2)

	assert.True(t, t2.After(*t1),
		"重复置顶应刷新 pinnedAt（EC-14），t1=%v t2=%v", t1, t2)
}

// TestPinSession_NotOwner_ReturnsForbidden session.UserID != userID → ErrForbidden。
func TestPinSession_NotOwner_ReturnsForbidden(t *testing.T) {
	db := newChatbotTestDB(t)
	b := newChatbotBiz(db)

	owner := insertUserRow(t, db, nil)
	other := insertUserRow(t, db, nil)
	cbID := insertChatbotConfig(t, db, owner, "bot", model.ChatbotStatusPublished)
	sessionID := insertChatbotSession(t, db, owner, cbID, "owner's session")

	pinnedAt, err := b.PinSession(context.Background(), other, sessionID, true)
	require.Error(t, err)
	assert.Nil(t, pinnedAt)
	assert.True(t, errors.Is(err, errno.ErrForbidden),
		"非 session 所有者 pin 操作应返回 ErrForbidden，实际=%v", err)
}
