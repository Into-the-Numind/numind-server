package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newXhsScriptModelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&XhsScriptUserProfile{},
		&XhsScriptQuotaAccount{},
		&XhsScriptNote{},
		&XhsScriptGeneration{},
		&XhsScriptQuotaLedger{},
		&XhsScriptAnalyticsEvent{},
	), "AutoMigrate should validate all XHS script GORM tags")
	return db
}

func TestXhsScriptModels_TableNames(t *testing.T) {
	assert.Equal(t, "xhs_script_user_profile", XhsScriptUserProfile{}.TableName())
	assert.Equal(t, "xhs_script_quota_account", XhsScriptQuotaAccount{}.TableName())
	assert.Equal(t, "xhs_script_note", XhsScriptNote{}.TableName())
	assert.Equal(t, "xhs_script_generation", XhsScriptGeneration{}.TableName())
	assert.Equal(t, "xhs_script_quota_ledger", XhsScriptQuotaLedger{}.TableName())
	assert.Equal(t, "xhs_script_analytics_event", XhsScriptAnalyticsEvent{}.TableName())
}

func TestXhsScriptModels_AutoMigrateAndRoundTrip(t *testing.T) {
	db := newXhsScriptModelTestDB(t)

	profile := XhsScriptUserProfile{UserID: 42, ProfileText: "理性、专业、适合口播"}
	require.NoError(t, db.Create(&profile).Error)
	assert.NotZero(t, profile.ID)

	quota := XhsScriptQuotaAccount{UserID: 42}
	require.NoError(t, db.Create(&quota).Error)
	assert.EqualValues(t, 3, quota.FreeRemaining)
	assert.EqualValues(t, 0, quota.PaidRemaining)

	transcript := "视频转写文本"
	note := XhsScriptNote{
		UserID:           42,
		SourceNoteID:     "xhs_abc",
		NoteURL:          "https://www.xiaohongshu.com/explore/xhs_abc",
		NoteType:         XhsScriptNoteTypeVideo,
		Title:            "爆款口播",
		Description:      "描述",
		Tags:             datatypes.JSON([]byte(`["口播","职场"]`)),
		HotComments:      datatypes.JSON([]byte(`[{"text":"很有用"}]`)),
		VideoTranscript:  &transcript,
		TranscribeStatus: XhsScriptTranscribeReady,
		GenerateStatus:   XhsScriptGenerateReady,
	}
	require.NoError(t, db.Create(&note).Error)
	assert.NotZero(t, note.ID)

	generation := XhsScriptGeneration{
		UserID:     42,
		NoteID:     note.ID,
		Version:    1,
		ScriptText: "第一版口播稿",
	}
	require.NoError(t, db.Create(&generation).Error)

	ledger := XhsScriptQuotaLedger{
		UserID:  42,
		Delta:   -1,
		Bucket:  XhsScriptQuotaBucketFree,
		Reason:  XhsScriptLedgerReasonGeneration,
		RefType: XhsScriptLedgerRefTypeGeneration,
		RefID:   "generation-1",
	}
	require.NoError(t, db.Create(&ledger).Error)

	event := XhsScriptAnalyticsEvent{
		EventID:     "evt_1",
		EventName:   "script_note_captured",
		AnonymousID: "anon_1",
		UserID:      ptrUint(42),
		SessionID:   "sess_1",
		Path:        "/xhs/script",
		Properties:  datatypes.JSON([]byte(`{"source":"extension"}`)),
	}
	require.NoError(t, db.Create(&event).Error)

	var got XhsScriptNote
	require.NoError(t, db.First(&got, note.ID).Error)
	assert.Equal(t, XhsScriptNoteTypeVideo, got.NoteType)
	require.NotNil(t, got.VideoTranscript)
	assert.Equal(t, transcript, *got.VideoTranscript)
	assert.JSONEq(t, `["口播","职场"]`, string(got.Tags))
}

func TestXhsScriptModels_UniqueUserScopedRecords(t *testing.T) {
	db := newXhsScriptModelTestDB(t)

	require.NoError(t, db.Create(&XhsScriptUserProfile{UserID: 7}).Error)
	assert.Error(t, db.Create(&XhsScriptUserProfile{UserID: 7}).Error, "profile user_id should be unique")

	require.NoError(t, db.Create(&XhsScriptQuotaAccount{UserID: 7}).Error)
	assert.Error(t, db.Create(&XhsScriptQuotaAccount{UserID: 7}).Error, "quota account user_id should be unique")

	require.NoError(t, db.Create(&XhsScriptAnalyticsEvent{EventID: "evt_unique", EventName: "open"}).Error)
	assert.Error(t, db.Create(&XhsScriptAnalyticsEvent{EventID: "evt_unique", EventName: "open"}).Error, "event_id should be unique")
}

func TestXhsScriptNote_UniqueUserSourceNote(t *testing.T) {
	db := newXhsScriptModelTestDB(t)

	first := XhsScriptNote{
		UserID:           9,
		SourceNoteID:     "source_unique",
		NoteType:         XhsScriptNoteTypeVideo,
		TranscribeStatus: XhsScriptTranscribePending,
		GenerateStatus:   XhsScriptGenerateNotReady,
	}
	require.NoError(t, db.Create(&first).Error)

	duplicate := XhsScriptNote{
		UserID:           9,
		SourceNoteID:     "source_unique",
		NoteType:         XhsScriptNoteTypeVideo,
		TranscribeStatus: XhsScriptTranscribePending,
		GenerateStatus:   XhsScriptGenerateNotReady,
	}
	assert.Error(t, db.Create(&duplicate).Error, "same (user_id, source_note_id) must be unique")

	otherUser := XhsScriptNote{
		UserID:           10,
		SourceNoteID:     "source_unique",
		NoteType:         XhsScriptNoteTypeVideo,
		TranscribeStatus: XhsScriptTranscribePending,
		GenerateStatus:   XhsScriptGenerateNotReady,
	}
	assert.NoError(t, db.Create(&otherUser).Error, "same source_note_id is allowed for another user")
}

func ptrUint(v uint) *uint { return &v }
