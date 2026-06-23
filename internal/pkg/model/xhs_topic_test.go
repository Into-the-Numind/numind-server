package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newXhsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&XhsTopicNote{}), "AutoMigrate should validate all GORM tags")
	return db
}

func TestXhsTopicNote_TableName(t *testing.T) {
	assert.Equal(t, "xhs_topic_note", XhsTopicNote{}.TableName())
}

// TestXhsTopicNote_AutoMigrateAndRoundTrip exercises every GORM tag (AutoMigrate would
// error on a malformed composite-index priority) and confirms nullable pointer fields
// (VideoTranscript *string, CollectedAt/PublishedAt *time.Time) round-trip correctly.
func TestXhsTopicNote_AutoMigrateAndRoundTrip(t *testing.T) {
	db := newXhsTestDB(t)

	transcript := "口播稿转写内容"
	collected := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	published := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)

	note := XhsTopicNote{
		UserID:          42,
		XhsNoteID:       "note_abc123",
		ContentHash:     "deadbeef",
		NoteType:        XhsNoteTypeVideo,
		Title:           "标题",
		Content:         "正文",
		Tags:            datatypes.JSON([]byte(`["美食","探店"]`)),
		Comments:        datatypes.JSON([]byte(`[{"nick":"u","text":"好","likes":3}]`)),
		VideoTranscript: &transcript,
		LikeCount:       100,
		CollectedAt:     &collected,
		PublishedAt:     &published,
		CrawledAt:       time.Now().UTC().Truncate(time.Second),
		EnrichStatus:    XhsEnrichPending,
	}
	require.NoError(t, db.Create(&note).Error)
	assert.NotZero(t, note.ID, "autoincrement id should be set")

	var got XhsTopicNote
	require.NoError(t, db.First(&got, note.ID).Error)
	assert.Equal(t, uint(42), got.UserID)
	assert.Equal(t, "note_abc123", got.XhsNoteID)
	assert.Equal(t, XhsNoteTypeVideo, got.NoteType)
	require.NotNil(t, got.VideoTranscript)
	assert.Equal(t, transcript, *got.VideoTranscript)
	require.NotNil(t, got.CollectedAt)
	require.NotNil(t, got.PublishedAt)
	assert.Equal(t, XhsEnrichPending, got.EnrichStatus)
	assert.JSONEq(t, `["美食","探店"]`, string(got.Tags))
}

// TestXhsTopicNote_UniqueUserNote verifies the uk_xtn_user_note composite unique index
// rejects a duplicate (user_id, xhs_note_id) pair while allowing the same note_id for a
// different user.
func TestXhsTopicNote_UniqueUserNote(t *testing.T) {
	db := newXhsTestDB(t)

	base := XhsTopicNote{UserID: 1, XhsNoteID: "dup", CrawledAt: time.Now(), EnrichStatus: XhsEnrichPending}
	require.NoError(t, db.Create(&base).Error)

	dup := XhsTopicNote{UserID: 1, XhsNoteID: "dup", CrawledAt: time.Now(), EnrichStatus: XhsEnrichPending}
	assert.Error(t, db.Create(&dup).Error, "same (user_id, xhs_note_id) must violate uk_xtn_user_note")

	otherUser := XhsTopicNote{UserID: 2, XhsNoteID: "dup", CrawledAt: time.Now(), EnrichStatus: XhsEnrichPending}
	assert.NoError(t, db.Create(&otherUser).Error, "same note_id under different user is allowed")
}

// TestXhsTopicNote_NullableTranscript confirms a nil VideoTranscript persists as NULL.
func TestXhsTopicNote_NullableTranscript(t *testing.T) {
	db := newXhsTestDB(t)

	note := XhsTopicNote{UserID: 7, XhsNoteID: "no_video", CrawledAt: time.Now(), EnrichStatus: XhsEnrichPartial}
	require.NoError(t, db.Create(&note).Error)

	var got XhsTopicNote
	require.NoError(t, db.First(&got, note.ID).Error)
	assert.Nil(t, got.VideoTranscript, "nil transcript should stay NULL")
	assert.Nil(t, got.CollectedAt, "nil collected_at should stay NULL")
}
