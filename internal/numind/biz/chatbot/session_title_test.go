package chatbot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newTitleTestBiz builds a chatbotBiz backed by an in-memory SQLite store with
// just the chatbot_session table — enough to exercise maybeGenerateTitle's
// UpdateTitle write without the full ChatStream LLM path.
func newTitleTestBiz(t *testing.T) (*chatbotBiz, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.ChatbotSession{}, &model.ChatbotConfig{}))
	return &chatbotBiz{ds: store.NewTestStore(db)}, db
}

// withGenTitleFn swaps the package-level title generator for the test. Not
// t.Parallel safe (package-level var).
func withGenTitleFn(t *testing.T, fn func(ctx context.Context, userMsg, assistantMsg string) (string, error)) {
	t.Helper()
	old := genTitleFn
	genTitleFn = fn
	t.Cleanup(func() { genTitleFn = old })
}

func TestMaybeGenerateTitle_DefaultName_GeneratesAndPersists(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)

	withGenTitleFn(t, func(_ context.Context, u, a string) (string, error) {
		assert.Contains(t, u, "退货", "user message forwarded to generator")
		assert.Contains(t, a, "流程", "assistant content forwarded to generator")
		return "退货流程咨询", nil
	})

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "怎么退货", "退货流程是这样的……")
	assert.Equal(t, "退货流程咨询", got, "first turn with default title → generate")

	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "退货流程咨询", row.Title, "generated title persisted to DB")
}

func TestMaybeGenerateTitle_RenamedOrTitled_Skips(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "我的重要对话", Status: "active"}
	require.NoError(t, db.Create(sess).Error)

	called := false
	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "x", nil
	})

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "q", "a")
	assert.Empty(t, got, "title != defaultName → skip")
	assert.False(t, called, "generator must NOT be called once renamed (US3)")

	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "我的重要对话", row.Title, "manual rename preserved")
}

func TestMaybeGenerateTitle_GenerateError_LeavesTitleUnchanged(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)

	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("llm down")
	})

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "q", "a")
	assert.Empty(t, got, "generate failure → no title")
	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "客服助手", row.Title, "title unchanged on generate failure (best-effort)")
}

func TestMaybeGenerateTitle_EmptyResult_NoUpdate(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)

	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		return "", nil // success but empty title
	})

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "q", "a")
	assert.Empty(t, got)
	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "客服助手", row.Title, "empty generated title → no update")
}

// Concurrent-rename race (P1): session.Title is a snapshot from ChatStream start;
// the user renames mid-stream so the DB row no longer equals defaultName by the
// time we write. The atomic compare-and-set must NOT clobber the manual rename.
func TestMaybeGenerateTitle_ConcurrentRename_NoClobber(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)
	// Simulate the user renaming the session DURING title generation: the DB row
	// changes while the in-memory snapshot (sess.Title) still holds the default.
	require.NoError(t, db.Model(&model.ChatbotSession{}).Where("id = ?", sess.ID).
		Update("title", "用户中途改的名").Error)

	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		return "自动生成标题", nil
	})

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "q", "a")
	assert.Empty(t, got, "compare-and-set must fail when title changed concurrently")

	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "用户中途改的名", row.Title, "manual rename during generation must NOT be clobbered (US3)")
}

// GenerateTitleForSession is the send-time (instant) path: ownership check + reuse
// of the default-name guard + CAS, generating from the prompt alone.
func TestGenerateTitleForSession(t *testing.T) {
	b, db := newTitleTestBiz(t)
	cfg := &model.ChatbotConfig{Name: "客服助手"}
	require.NoError(t, db.Create(cfg).Error)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: cfg.ID, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)

	// owner + still default name → generate from prompt + persist
	withGenTitleFn(t, func(_ context.Context, p, a string) (string, error) {
		assert.Contains(t, p, "怎么退货")
		assert.Empty(t, a, "instant path passes prompt only")
		return "退货咨询", nil
	})
	title, err := b.GenerateTitleForSession(context.Background(), 1, sess.ID, "怎么退货")
	require.NoError(t, err)
	assert.Equal(t, "退货咨询", title)
	var row model.ChatbotSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, "退货咨询", row.Title, "persisted")

	// already named (now != default) → "" skip
	title, err = b.GenerateTitleForSession(context.Background(), 1, sess.ID, "另一个问题")
	require.NoError(t, err)
	assert.Empty(t, title)

	// non-owner → error, no change
	_, err = b.GenerateTitleForSession(context.Background(), 999, sess.ID, "q")
	require.Error(t, err, "non-owner must be rejected")
}

func TestMaybeGenerateTitle_NilSession_ReturnsEmpty(t *testing.T) {
	b, _ := newTitleTestBiz(t)
	called := false
	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "x", nil
	})
	assert.Empty(t, b.maybeGenerateTitle(context.Background(), nil, "bot", "q", "a"))
	assert.False(t, called, "nil session → no generation")
}

func TestMaybeGenerateTitle_UpdateError_ReturnsEmpty(t *testing.T) {
	b, db := newTitleTestBiz(t)
	sess := &model.ChatbotSession{UserID: 1, ChatbotID: 2, Title: "客服助手", Status: "active"}
	require.NoError(t, db.Create(sess).Error)
	withGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		return "新标题", nil
	})
	// Close the DB so UpdateTitleIfCurrent errors.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	got := b.maybeGenerateTitle(context.Background(), sess, "客服助手", "q", "a")
	assert.Empty(t, got, "UpdateTitleIfCurrent error → best-effort returns empty")
}
