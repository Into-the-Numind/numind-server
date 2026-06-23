package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// newXhsTestDB creates an isolated in-memory SQLite DB for xhs store tests.
func newXhsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(&model.XhsTopicNote{}), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newXhsNote(userID uint, noteID, hash string) *model.XhsTopicNote {
	return &model.XhsTopicNote{
		UserID:       userID,
		XhsNoteID:    noteID,
		ContentHash:  hash,
		NoteType:     model.XhsNoteTypeNormal,
		Title:        "测试标题",
		Content:      "测试正文",
		EnrichStatus: model.XhsEnrichPending,
	}
}

func TestUpsertNote_NewRecord_ReturnsChanged(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	n := newXhsNote(1, "note-a", "hash1")
	changed, err := s.UpsertByUserNote(ctx, n)
	require.NoError(t, err)
	assert.True(t, changed, "new record should report hashChanged=true")
	assert.NotZero(t, n.ID, "new record should be assigned an ID")
}

func TestUpsertNote_SameHash_ReturnsUnchanged(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	first := newXhsNote(1, "note-a", "hash1")
	_, err := s.UpsertByUserNote(ctx, first)
	require.NoError(t, err)

	// Simulate the note already enriched.
	require.NoError(t, s.UpdateEnrichStatus(ctx, first.ID, model.XhsEnrichDone))

	// Upsert again with the SAME hash but a different title (should NOT overwrite).
	again := newXhsNote(1, "note-a", "hash1")
	again.Title = "被改过的标题-不应写入"
	changed, err := s.UpsertByUserNote(ctx, again)
	require.NoError(t, err)
	assert.False(t, changed, "same hash should report hashChanged=false")
	assert.Equal(t, first.ID, again.ID, "should reuse existing ID")

	// Verify DB row untouched: enrich result preserved, title unchanged.
	row, err := s.GetNote(ctx, 1, first.ID)
	require.NoError(t, err)
	assert.Equal(t, model.XhsEnrichDone, row.EnrichStatus, "enrich status preserved")
	assert.Equal(t, "测试标题", row.Title, "title should not be overwritten on same hash")
}

func TestUpsertNote_DifferentHash_ReturnsChangedAndUpdates(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	first := newXhsNote(1, "note-a", "hash1")
	_, err := s.UpsertByUserNote(ctx, first)
	require.NoError(t, err)

	updated := newXhsNote(1, "note-a", "hash2")
	updated.Title = "新标题"
	changed, err := s.UpsertByUserNote(ctx, updated)
	require.NoError(t, err)
	assert.True(t, changed, "different hash should report hashChanged=true")
	assert.Equal(t, first.ID, updated.ID, "should reuse existing ID on update")

	row, err := s.GetNote(ctx, 1, first.ID)
	require.NoError(t, err)
	assert.Equal(t, "新标题", row.Title, "title should be updated")
	assert.Equal(t, "hash2", row.ContentHash, "content_hash should be updated")
}

func TestUpsertNote_UserIsolation_SameNoteID(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	// Same xhs_note_id but different users → two distinct rows.
	a := newXhsNote(1, "note-shared", "hashA")
	b := newXhsNote(2, "note-shared", "hashB")
	_, err := s.UpsertByUserNote(ctx, a)
	require.NoError(t, err)
	_, err = s.UpsertByUserNote(ctx, b)
	require.NoError(t, err)
	assert.NotEqual(t, a.ID, b.ID, "different users with same note_id should be distinct rows")
}

func TestListNotes_PaginationAndUserIsolation(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		n := newXhsNote(1, string(rune('a'+i))+"-note", "h")
		_, err := s.UpsertByUserNote(ctx, n)
		require.NoError(t, err)
	}
	// Another user's note must not leak.
	_, err := s.UpsertByUserNote(ctx, newXhsNote(2, "other-note", "h"))
	require.NoError(t, err)

	list, total, err := s.ListNotes(ctx, 1, XhsNoteFilter{}, 0, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total, "total should count only user 1's notes")
	assert.Len(t, list, 3, "limit=3 should return 3 rows")

	page2, total2, err := s.ListNotes(ctx, 1, XhsNoteFilter{}, 3, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total2)
	assert.Len(t, page2, 2, "second page should return remaining 2 rows")
}

func TestListNotes_Filters(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	v := newXhsNote(1, "video-1", "h1")
	v.NoteType = model.XhsNoteTypeVideo
	v.EnrichStatus = model.XhsEnrichDone
	v.Title = "口播视频选题"
	_, err := s.UpsertByUserNote(ctx, v)
	require.NoError(t, err)

	n := newXhsNote(1, "normal-1", "h2")
	n.Title = "图文笔记"
	_, err = s.UpsertByUserNote(ctx, n)
	require.NoError(t, err)

	// note_type filter
	list, total, err := s.ListNotes(ctx, 1, XhsNoteFilter{NoteType: model.XhsNoteTypeVideo}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, "video-1", list[0].XhsNoteID)

	// enrich_status filter
	_, total, err = s.ListNotes(ctx, 1, XhsNoteFilter{EnrichStatus: model.XhsEnrichDone}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	// keyword filter (title)
	_, total, err = s.ListNotes(ctx, 1, XhsNoteFilter{Keyword: "口播"}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
}

func TestGetNote_CrossUserNotFound(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	n := newXhsNote(1, "note-a", "h")
	_, err := s.UpsertByUserNote(ctx, n)
	require.NoError(t, err)

	// Same id but wrong user → not found.
	_, err = s.GetNote(ctx, 2, n.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrXhsNoteNotFound), "cross-user get should be ErrXhsNoteNotFound")

	// Owner can read.
	got, err := s.GetNote(ctx, 1, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "note-a", got.XhsNoteID)
}

func TestDeleteNote_CrossUserNoop(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	n := newXhsNote(1, "note-a", "h")
	_, err := s.UpsertByUserNote(ctx, n)
	require.NoError(t, err)

	// Wrong user delete must not remove the row.
	require.NoError(t, s.DeleteNote(ctx, 2, n.ID))
	got, err := s.GetNote(ctx, 1, n.ID)
	require.NoError(t, err)
	assert.NotNil(t, got, "row should still exist after cross-user delete")

	// Owner delete removes it.
	require.NoError(t, s.DeleteNote(ctx, 1, n.ID))
	_, err = s.GetNote(ctx, 1, n.ID)
	assert.True(t, errors.Is(err, errno.ErrXhsNoteNotFound), "row should be gone after owner delete")
}

func TestUpdateEnrichResult(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	n := newXhsNote(1, "note-a", "h")
	_, err := s.UpsertByUserNote(ctx, n)
	require.NoError(t, err)

	transcript := "口播转写内容"
	n.AITopicAngle = "角度"
	n.AIOneLine = "一句话总结"
	n.VideoTranscript = &transcript
	n.EnrichStatus = model.XhsEnrichDone
	require.NoError(t, s.UpdateEnrichResult(ctx, n))

	row, err := s.GetNote(ctx, 1, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "角度", row.AITopicAngle)
	assert.Equal(t, "一句话总结", row.AIOneLine)
	require.NotNil(t, row.VideoTranscript)
	assert.Equal(t, transcript, *row.VideoTranscript)
	assert.Equal(t, model.XhsEnrichDone, row.EnrichStatus)
}

func TestGetByIDs_UserIsolation(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	a := newXhsNote(1, "a", "h")
	b := newXhsNote(1, "b", "h")
	other := newXhsNote(2, "c", "h")
	_, err := s.UpsertByUserNote(ctx, a)
	require.NoError(t, err)
	_, err = s.UpsertByUserNote(ctx, b)
	require.NoError(t, err)
	_, err = s.UpsertByUserNote(ctx, other)
	require.NoError(t, err)

	got, err := s.GetByIDs(ctx, 1, []uint64{a.ID, b.ID, other.ID})
	require.NoError(t, err)
	assert.Len(t, got, 2, "should only return user 1's notes even if other user's id is requested")

	empty, err := s.GetByIDs(ctx, 1, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestListPendingEnrich(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	// Seed 4 pending notes (across two users — queue scan is cross-user) + 2 non-pending.
	for i := 0; i < 3; i++ {
		n := newXhsNote(1, string(rune('a'+i))+"-pending", "h")
		_, err := s.UpsertByUserNote(ctx, n)
		require.NoError(t, err)
	}
	otherUserPending := newXhsNote(2, "u2-pending", "h")
	_, err := s.UpsertByUserNote(ctx, otherUserPending)
	require.NoError(t, err)

	done := newXhsNote(1, "done-note", "h")
	done.EnrichStatus = model.XhsEnrichDone
	_, err = s.UpsertByUserNote(ctx, done)
	require.NoError(t, err)

	failed := newXhsNote(1, "failed-note", "h")
	failed.EnrichStatus = model.XhsEnrichFailed
	_, err = s.UpsertByUserNote(ctx, failed)
	require.NoError(t, err)

	// limit cap: ask for 2, should get exactly 2 (out of 4 pending).
	capped, err := s.ListPendingEnrich(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, capped, 2, "limit=2 should cap the result")
	for _, n := range capped {
		assert.Equal(t, model.XhsEnrichPending, n.EnrichStatus, "only pending rows returned")
	}

	// status filter: large limit returns ALL 4 pending and NO non-pending.
	all, err := s.ListPendingEnrich(ctx, 100)
	require.NoError(t, err)
	assert.Len(t, all, 4, "should return all 4 pending rows, excluding done/failed")
	for _, n := range all {
		assert.Equal(t, model.XhsEnrichPending, n.EnrichStatus, "non-pending rows must be excluded")
	}
}

// TestUpsertNote_DifferentHash_ClearsEnrichment verifies the documented contract:
// when content_hash changes, the full-field Save wipes stale AI enrichment fields
// (caller passes a freshly-scraped struct with enrich_status=pending + zero AI fields).
func TestUpsertNote_DifferentHash_ClearsEnrichment(t *testing.T) {
	s := &xhsStore{db: newXhsTestDB(t)}
	ctx := context.Background()

	// 1. Upsert hash1.
	first := newXhsNote(1, "note-a", "hash1")
	_, err := s.UpsertByUserNote(ctx, first)
	require.NoError(t, err)

	// 2. Enrich it: set AI fields + enrich_status=done.
	first.AITopicAngle = "原始选题角度"
	first.AIOneLine = "原始一句话"
	first.EnrichStatus = model.XhsEnrichDone
	require.NoError(t, s.UpdateEnrichResult(ctx, first))

	// 3. Re-upsert hash2 with a freshly-scraped struct (AI fields zero, status=pending).
	updated := newXhsNote(1, "note-a", "hash2")
	updated.Title = "新内容"
	// newXhsNote already sets EnrichStatus=pending and leaves AI fields zero.
	changed, err := s.UpsertByUserNote(ctx, updated)
	require.NoError(t, err)
	assert.True(t, changed, "different hash should report hashChanged=true")

	// 4. Assert stale enrichment was wiped and status reset to pending.
	row, err := s.GetNote(ctx, 1, first.ID)
	require.NoError(t, err)
	assert.Equal(t, "", row.AITopicAngle, "stale ai_topic_angle should be cleared")
	assert.Equal(t, "", row.AIOneLine, "stale ai_one_line should be cleared")
	assert.Equal(t, model.XhsEnrichPending, row.EnrichStatus, "enrich_status should reset to pending")
	assert.Equal(t, "hash2", row.ContentHash, "content_hash should be updated")
}
