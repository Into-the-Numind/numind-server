package xhsscript

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
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestGenerateScriptFailuresPersistSafeLastErrorCategories(t *testing.T) {
	t.Run("llm raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(201)
		note := createGenerateReadyNote(t, db, userID)
		rawErr := errors.New("llm provider leaked prompt: 我的完整产品定位和视频转写")

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return nil, rawErr
		})

		dto, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errno.ErrInternalServer)
		assert.NotErrorIs(t, err, rawErr)
		assert.NotContains(t, err.Error(), "llm provider")
		assert.NotContains(t, err.Error(), "完整产品定位")
		assert.NotContains(t, err.Error(), "视频转写")
		assert.Nil(t, dto)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_failed", got.LastError)
		assert.NotContains(t, got.LastError, "完整产品定位")
		assert.NotContains(t, got.LastError, "视频转写")
	})

	t.Run("empty llm response uses stable category", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(202)
		note := createGenerateReadyNote(t, db, userID)

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: " \n\t "}, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errno.ErrInternalServer)
		assert.NotContains(t, err.Error(), "xhs_script_quota_ledger")
		assert.NotContains(t, err.Error(), "no such table")

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_empty", got.LastError)
		assert.NotContains(t, got.LastError, "生成结果为空")
	})

	t.Run("quota insufficient uses stable category", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(203)
		note := createGenerateReadyNote(t, db, userID)
		require.NoError(t, db.Model(&model.XhsScriptQuotaAccount{}).
			Where("user_id = ?", userID).
			Updates(map[string]interface{}{"free_remaining": 0, "paid_remaining": 0}).Error)

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			t.Fatal("chat should not be called when quota is insufficient")
			return nil, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "quota_insufficient", got.LastError)
	})

	t.Run("commit raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(204)
		note := createGenerateReadyNote(t, db, userID)
		require.NoError(t, db.Migrator().DropTable(&model.XhsScriptQuotaLedger{}))

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: "安全口播稿"}, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_commit_failed", got.LastError)
		assert.NotContains(t, got.LastError, "xhs_script_quota_ledger")
		assert.NotContains(t, got.LastError, "no such table")
	})
}

func withGenerateTestChat(t *testing.T, chat func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	prev := xhsScriptChatFn
	xhsScriptChatFn = chat
	t.Cleanup(func() { xhsScriptChatFn = prev })
}

func newXhsScriptGenerateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_script_generate_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptUserProfile{},
		&model.XhsScriptQuotaAccount{},
		&model.XhsScriptQuotaLedger{},
		&model.XhsScriptNote{},
		&model.XhsScriptGeneration{},
		&model.XhsScriptAnalyticsEvent{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func createGenerateReadyNote(t *testing.T, db *gorm.DB, userID uint) *model.XhsScriptNote {
	t.Helper()
	transcript := "这是一段可用于生成的安全转写"
	require.NoError(t, db.Create(&model.XhsScriptUserProfile{
		UserID:      userID,
		ProfileText: "产品定位",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 1,
	}).Error)
	note := &model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     "generate-ready",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "可生成视频",
		VideoURL:         "https://sns-video.xhscdn.com/generate-ready.mp4",
		VideoTranscript:  &transcript,
		TranscribeStatus: model.XhsScriptTranscribeReady,
		GenerateStatus:   model.XhsScriptGenerateReady,
	}
	require.NoError(t, db.Create(note).Error)
	return note
}

func loadGenerateTestNote(t *testing.T, db *gorm.DB, userID uint, noteID uint64) model.XhsScriptNote {
	t.Helper()
	var note model.XhsScriptNote
	require.NoError(t, db.Where("user_id = ? AND id = ?", userID, noteID).First(&note).Error)
	return note
}
