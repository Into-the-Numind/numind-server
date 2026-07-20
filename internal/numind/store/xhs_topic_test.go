package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

func newXhsSnapshotNote(userID uint, i int, collectedAt time.Time) model.XhsTopicNote {
	transcript := fmt.Sprintf("视频文字稿-%03d", i)
	return model.XhsTopicNote{
		UserID:          userID,
		XhsNoteID:       fmt.Sprintf("xhs-%03d", i),
		ContentHash:     fmt.Sprintf("hash-%d-%03d", userID, i),
		NoteType:        model.XhsNoteTypeVideo,
		Title:           fmt.Sprintf("标题-%03d", i),
		Content:         fmt.Sprintf("正文-%03d", i),
		VideoTranscript: &transcript,
		LikeCount:       i,
		CollectCount:    i + 1,
		CommentCount:    i + 2,
		Comments:        []byte(fmt.Sprintf(`[{"text":"评论-%03d"}]`, i)),
		NoteURL:         fmt.Sprintf("https://example.test/note/%03d", i),
		AuthorName:      "不属于 Agent 投影",
		AITopicAngle:    "不读取已有富化字段",
		EnrichStatus:    model.XhsEnrichDone,
		CollectedAt:     &collectedAt,
		CrawledAt:       collectedAt,
	}
}

func TestXhsSnapshot_StableKeysetPaginationAndUserIsolation(t *testing.T) {
	db := newXhsTestDB(t)
	s := &xhsStore{db: db}
	ctx := context.Background()
	baseTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	rows := make([]model.XhsTopicNote, 0, 246)
	for i := 1; i <= 243; i++ {
		rows = append(rows, newXhsSnapshotNote(101, i, baseTime.Add(time.Duration(i)*time.Minute)))
	}
	for i := 1; i <= 3; i++ {
		rows = append(rows, newXhsSnapshotNote(202, i, baseTime.Add(time.Duration(i)*time.Minute)))
	}
	require.NoError(t, db.CreateInBatches(rows, 100).Error)

	query := XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 100}
	first, err := s.ListSnapshot(ctx, 101, query)
	require.NoError(t, err)
	require.Len(t, first.Notes, 100)
	assert.EqualValues(t, 243, first.SnapshotTotal)
	assert.True(t, first.HasMore)
	assert.Equal(t, first.Notes[99].ID, first.NextAfterID)
	require.NotZero(t, first.SnapshotMaxID)

	// A note captured after page 1 must be left for the next snapshot.
	late := newXhsSnapshotNote(101, 999, baseTime.Add(999*time.Minute))
	require.NoError(t, db.Create(&late).Error)
	assert.Greater(t, late.ID, first.SnapshotMaxID)

	gotIDs := make([]uint64, 0, 243)
	gotIDs = append(gotIDs, noteIDs(first.Notes)...)
	after := first.NextAfterID
	for first.HasMore {
		page, pageErr := s.ListSnapshot(ctx, 101, XhsSnapshotQuery{
			Projection:    XhsSnapshotProjectionIndex,
			AfterID:       after,
			SnapshotMaxID: first.SnapshotMaxID,
			SnapshotTotal: first.SnapshotTotal,
			Limit:         100,
		})
		require.NoError(t, pageErr)
		gotIDs = append(gotIDs, noteIDs(page.Notes)...)
		first = page
		after = page.NextAfterID
	}

	require.Len(t, gotIDs, 243)
	for i := 1; i < len(gotIDs); i++ {
		assert.Greater(t, gotIDs[i], gotIDs[i-1], "IDs must be strictly increasing without duplicates")
	}
	assert.NotContains(t, gotIDs, late.ID)
}

func TestXhsSnapshot_CombinedFiltersAndLiteralLikeEscaping(t *testing.T) {
	db := newXhsTestDB(t)
	s := &xhsStore{db: db}
	ctx := context.Background()
	from := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	to := from.Add(48 * time.Hour)

	matching := newXhsSnapshotNote(7, 1, from.Add(time.Hour))
	matching.XhsNoteID = "wanted"
	matching.Title = `literal 100%_\\marker`
	outsideTime := newXhsSnapshotNote(7, 2, to)
	outsideTime.XhsNoteID = "outside-time"
	outsideTime.Content = `literal 100%_\\marker`
	wrongKeyword := newXhsSnapshotNote(7, 3, from.Add(time.Hour))
	wrongKeyword.XhsNoteID = "wrong-keyword"
	wrongKeyword.Title = "literal 100-percent"
	otherUser := newXhsSnapshotNote(8, 4, from.Add(time.Hour))
	otherUser.XhsNoteID = "wanted"
	otherUser.Title = matching.Title
	require.NoError(t, db.Create([]*model.XhsTopicNote{&matching, &outsideTime, &wrongKeyword, &otherUser}).Error)

	page, err := s.ListSnapshot(ctx, 7, XhsSnapshotQuery{
		Projection: XhsSnapshotProjectionFull,
		Filter: XhsSnapshotFilter{
			XhsNoteIDs:    []string{"wanted", "outside-time", "wrong-keyword"},
			Keyword:       `100%_\\marker`,
			CollectedFrom: &from,
			CollectedTo:   &to,
		},
		Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, page.Notes, 1)
	assert.Equal(t, "wanted", page.Notes[0].XhsNoteID)
	assert.EqualValues(t, 1, page.SnapshotTotal)
	assert.False(t, page.HasMore)
}

func TestXhsSnapshot_ProjectionColumnsAndLimitPlusOne(t *testing.T) {
	db := newXhsTestDB(t)
	s := &xhsStore{db: db}
	ctx := context.Background()
	collected := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	rows := []model.XhsTopicNote{
		newXhsSnapshotNote(42, 1, collected),
		newXhsSnapshotNote(42, 2, collected.Add(time.Minute)),
		newXhsSnapshotNote(42, 3, collected.Add(2*time.Minute)),
	}
	require.NoError(t, db.Create(&rows).Error)

	index, err := s.ListSnapshot(ctx, 42, XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 2})
	require.NoError(t, err)
	require.Len(t, index.Notes, 2)
	assert.True(t, index.HasMore)
	assert.Equal(t, "", index.Notes[0].Title)
	assert.Equal(t, "", index.Notes[0].Content)
	assert.Equal(t, "", index.Notes[0].AuthorName)
	assert.NotNil(t, index.Notes[0].CollectedAt)

	full, err := s.ListSnapshot(ctx, 42, XhsSnapshotQuery{Projection: XhsSnapshotProjectionFull, Limit: 2})
	require.NoError(t, err)
	require.Len(t, full.Notes, 2)
	assert.Equal(t, "标题-001", full.Notes[0].Title)
	assert.Equal(t, "正文-001", full.Notes[0].Content)
	assert.Equal(t, "视频文字稿-001", *full.Notes[0].VideoTranscript)
	assert.Equal(t, "", full.Notes[0].AuthorName, "full projection must not expose unrelated author fields")
	assert.Equal(t, "", full.Notes[0].AITopicAngle, "full projection must not expose existing enrichment")

	last, err := s.ListSnapshot(ctx, 42, XhsSnapshotQuery{
		Projection:    XhsSnapshotProjectionFull,
		AfterID:       full.NextAfterID,
		SnapshotMaxID: full.SnapshotMaxID,
		SnapshotTotal: full.SnapshotTotal,
		Limit:         2,
	})
	require.NoError(t, err)
	require.Len(t, last.Notes, 1)
	assert.False(t, last.HasMore)
	assert.Equal(t, last.Notes[0].ID, last.NextAfterID)
}

func TestXhsSnapshot_EmptyDeletedRowAndInputBoundaries(t *testing.T) {
	db := newXhsTestDB(t)
	s := &xhsStore{db: db}
	ctx := context.Background()
	collected := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)

	empty, err := s.ListSnapshot(ctx, 9, XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, empty.Notes)
	assert.Zero(t, empty.SnapshotMaxID)
	assert.Zero(t, empty.SnapshotTotal)
	assert.False(t, empty.HasMore)

	rows := []model.XhsTopicNote{
		newXhsSnapshotNote(9, 1, collected),
		newXhsSnapshotNote(9, 2, collected.Add(time.Minute)),
		newXhsSnapshotNote(9, 3, collected.Add(2*time.Minute)),
	}
	require.NoError(t, db.Create(&rows).Error)
	first, err := s.ListSnapshot(ctx, 9, XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 1})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.NoError(t, db.Delete(&model.XhsTopicNote{}, rows[1].ID).Error)
	remaining, err := s.ListSnapshot(ctx, 9, XhsSnapshotQuery{
		Projection:    XhsSnapshotProjectionIndex,
		AfterID:       first.NextAfterID,
		SnapshotMaxID: first.SnapshotMaxID,
		SnapshotTotal: first.SnapshotTotal,
		Limit:         100,
	})
	require.NoError(t, err)
	require.Len(t, remaining.Notes, 1)
	assert.Equal(t, rows[2].ID, remaining.Notes[0].ID)
	assert.Equal(t, first.SnapshotTotal, remaining.SnapshotTotal, "snapshot total must remain fixed even if a row is deleted between pages")

	_, err = s.ListSnapshot(ctx, 9, XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 0})
	require.Error(t, err)
	_, err = s.ListSnapshot(ctx, 9, XhsSnapshotQuery{Projection: XhsSnapshotProjectionIndex, Limit: 101})
	require.Error(t, err)
	_, err = s.ListSnapshot(ctx, 9, XhsSnapshotQuery{Projection: XhsSnapshotProjection("bogus"), Limit: 1})
	require.Error(t, err)
}

func noteIDs(notes []model.XhsTopicNote) []uint64 {
	ids := make([]uint64, len(notes))
	for i := range notes {
		ids[i] = notes[i].ID
	}
	return ids
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
