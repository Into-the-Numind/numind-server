package store

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newChatbotSessionTestDB 创建用于 ChatbotSession store 测试的 in-memory SQLite DB。
// 使用显式 DDL 而非 AutoMigrate：避免 MySQL enum/特殊类型 SQLite 不兼容问题。
func newChatbotSessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/chatbot_session_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE chatbot_session (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at    DATETIME,
			updated_at    DATETIME,
			deleted_at    DATETIME,
			user_id       INTEGER NOT NULL,
			chatbot_id    INTEGER NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'active',
			message_count INTEGER NOT NULL DEFAULT 0,
			pinned_at     DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertChatbotSession 向测试 DB 插入一条 chatbot_session 行，返回 ID。
func insertChatbotSession(t *testing.T, db *gorm.DB, userID, chatbotID uint, title string, updatedAt time.Time, pinnedAt *time.Time) uint {
	t.Helper()
	session := model.ChatbotSession{
		UserID:    userID,
		ChatbotID: chatbotID,
		Title:     title,
		Status:    model.ChatbotSessionStatusActive,
		PinnedAt:  pinnedAt,
	}
	// Insert with explicit updated_at so ordering tests are deterministic.
	result := db.Exec(
		`INSERT INTO chatbot_session (created_at, updated_at, user_id, chatbot_id, title, status, message_count, pinned_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		updatedAt, updatedAt, userID, chatbotID, title, session.Status, pinnedAt,
	)
	require.NoError(t, result.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// ============================================================================
// UpdateTitle
// ============================================================================

func TestUpdateTitle_Success(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	id := insertChatbotSession(t, db, 1, 10, "old title", now, nil)

	err := s.UpdateTitle(ctx, id, "new title")
	require.NoError(t, err)

	got, err := s.GetSession(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "new title", got.Title)
}

func TestUpdateTitle_NotFound_ReturnsErrRecordNotFound(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	err := s.UpdateTitle(ctx, 99999, "some title")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestUpdateTitle_DoesNotRefreshUpdatedAt validates D2 decision: UpdateTitle must use
// UpdateColumn to bypass GORM's automatic updated_at refresh.
func TestUpdateTitle_DoesNotRefreshUpdatedAt(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	// Use a timestamp well in the past so any auto-refresh would be detectable.
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertChatbotSession(t, db, 1, 10, "original", past, nil)

	before, err := s.GetSession(ctx, id)
	require.NoError(t, err)

	// Small sleep to ensure if updated_at were refreshed it would be a different value.
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, s.UpdateTitle(ctx, id, "renamed"))

	after, err := s.GetSession(ctx, id)
	require.NoError(t, err)

	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt),
		"UpdateTitle must NOT refresh updated_at (D2 decision); before=%v after=%v",
		before.UpdatedAt, after.UpdatedAt)
}

// ============================================================================
// SetPinnedAt
// ============================================================================

func TestSetPinnedAt_SetThenClear(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	id := insertChatbotSession(t, db, 1, 10, "session", now, nil)

	// Pin it.
	pinTime := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	require.NoError(t, s.SetPinnedAt(ctx, id, &pinTime))

	got, err := s.GetSession(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got.PinnedAt, "pinned_at should be set after pin")
	assert.True(t, pinTime.Equal(got.PinnedAt.UTC()),
		"pinned_at should match the value we set; got=%v", got.PinnedAt)

	// Unpin it (nil => SQL NULL).
	require.NoError(t, s.SetPinnedAt(ctx, id, nil))

	got, err = s.GetSession(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, got.PinnedAt, "pinned_at should be NULL after unpin")
}

// TestSetPinnedAt_DoesNotRefreshUpdatedAt validates D2 decision.
func TestSetPinnedAt_DoesNotRefreshUpdatedAt(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := insertChatbotSession(t, db, 1, 10, "session", past, nil)

	before, err := s.GetSession(ctx, id)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	pinTime := time.Now()
	require.NoError(t, s.SetPinnedAt(ctx, id, &pinTime))

	after, err := s.GetSession(ctx, id)
	require.NoError(t, err)

	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt),
		"SetPinnedAt must NOT refresh updated_at (D2 decision); before=%v after=%v",
		before.UpdatedAt, after.UpdatedAt)
}

func TestSetPinnedAt_NotFound(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	pinTime := time.Now()
	err := s.SetPinnedAt(ctx, 99999, &pinTime)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// ============================================================================
// ListSessionsByChatbot
// ============================================================================

// TestListSessionsByChatbot_PinnedFirstThenUnpinned verifies the 3-row ordering:
//
//	A: pinned_at='2026-05-13 10:00', updated_at='2026-05-13 08:00'
//	B: pinned_at='2026-05-13 09:00', updated_at='2026-05-13 12:00'
//	C: pinned_at=NULL,               updated_at='2026-05-13 13:00'
//
// Expected order: A (pinned_at newest), B (pinned_at older), C (unpinned).
func TestListSessionsByChatbot_PinnedFirstThenUnpinned(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	pinA := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	pinB := time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)
	updA := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	updB := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	updC := time.Date(2026, 5, 13, 13, 0, 0, 0, time.UTC)

	idA := insertChatbotSession(t, db, 1, 100, "A", updA, &pinA)
	idB := insertChatbotSession(t, db, 1, 100, "B", updB, &pinB)
	idC := insertChatbotSession(t, db, 1, 100, "C", updC, nil)

	sessions, total, err := s.ListSessionsByChatbot(ctx, 1, 100, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, sessions, 3)
	assert.Equal(t, idA, sessions[0].ID, "A (newest pinned_at) must be first")
	assert.Equal(t, idB, sessions[1].ID, "B (older pinned_at) must be second")
	assert.Equal(t, idC, sessions[2].ID, "C (unpinned) must be last")
}

// TestListSessionsByChatbot_OnlyPinned_OrderByPinnedAtDesc verifies that when all
// sessions are pinned, they are ordered by pinned_at DESC.
func TestListSessionsByChatbot_OnlyPinned_OrderByPinnedAtDesc(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	pin1 := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	pin2 := time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)
	pin3 := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)

	id1 := insertChatbotSession(t, db, 2, 200, "latest pin", now, &pin1)
	id2 := insertChatbotSession(t, db, 2, 200, "middle pin", now, &pin2)
	id3 := insertChatbotSession(t, db, 2, 200, "oldest pin", now, &pin3)

	sessions, total, err := s.ListSessionsByChatbot(ctx, 2, 200, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, sessions, 3)
	assert.Equal(t, id1, sessions[0].ID, "latest pinned_at must be first")
	assert.Equal(t, id2, sessions[1].ID)
	assert.Equal(t, id3, sessions[2].ID, "oldest pinned_at must be last")
}

// TestListSessionsByChatbot_FilteredByChatbotID verifies that sessions belonging to
// a different chatbot_id are not returned.
func TestListSessionsByChatbot_FilteredByChatbotID(t *testing.T) {
	db := newChatbotSessionTestDB(t)
	s := NewChatbotSessionStore(db)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	userID := uint(3)

	// Two sessions for chatbot 300.
	id1 := insertChatbotSession(t, db, userID, 300, "for bot 300 #1", now, nil)
	id2 := insertChatbotSession(t, db, userID, 300, "for bot 300 #2", now, nil)
	// One session for chatbot 301 (different chatbot, same user) — must NOT appear.
	_ = insertChatbotSession(t, db, userID, 301, "for bot 301", now, nil)

	sessions, total, err := s.ListSessionsByChatbot(ctx, userID, 300, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, sessions, 2)

	ids := []uint{sessions[0].ID, sessions[1].ID}
	assert.Contains(t, ids, id1)
	assert.Contains(t, ids, id2)
}
