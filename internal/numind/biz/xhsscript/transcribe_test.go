package xhsscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/model"
)

func TestTranscribeNoteSuccessMirrorsVideoAndMarksReady(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(101)
	videoBytes := []byte("fake-mp4-bytes")
	wavBytes := []byte("fake-wav-bytes")
	transcript := "这是一段中文口播转写"

	note := createTranscribeTestNote(t, db, userID, "success")
	cosURL := fmt.Sprintf("https://cos.example/xhs-script-media/%d/%d/video.mp4", userID, note.ID)

	var mirroredBytes []byte
	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(_ context.Context, _ string, noteID uint64) (string, func(), error) {
			assert.Equal(t, note.ID, noteID)
			videoPath := filepath.Join(t.TempDir(), "video.mp4")
			require.NoError(t, os.WriteFile(videoPath, videoBytes, 0o600))
			return videoPath, func() { _ = os.Remove(videoPath) }, nil
		},
		mirrorVideoBytesToCOS: func(_ context.Context, gotUserID uint, gotNoteID uint64, data []byte) string {
			assert.Equal(t, userID, gotUserID)
			assert.Equal(t, note.ID, gotNoteID)
			mirroredBytes = append([]byte(nil), data...)
			return cosURL
		},
		extractAudio: func(_ context.Context, videoPath, audioPath string) error {
			assert.True(t, strings.HasSuffix(videoPath, ".mp4"))
			return os.WriteFile(audioPath, wavBytes, 0o600)
		},
		asr: func(_ context.Context, taskID string, req aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
			assert.Equal(t, profile.XhsTranscribe, taskID)
			assert.Equal(t, wavBytes, req.AudioBytes)
			assert.Equal(t, "wav", req.AudioFormat)
			assert.Equal(t, "zh", req.Language)
			return &aiservice.ASRResponse{Text: transcript}, nil
		},
	})

	require.NoError(t, svc.transcribeNote(ctx, userID, note.ID))

	got := loadTranscribeTestNote(t, db, userID, note.ID)
	assert.Equal(t, videoBytes, mirroredBytes)
	assert.Equal(t, cosURL, got.VideoURL)
	require.NotNil(t, got.VideoTranscript)
	assert.Equal(t, transcript, *got.VideoTranscript)
	assert.Equal(t, model.XhsScriptTranscribeReady, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateReady, got.GenerateStatus)
	assert.Equal(t, "", got.LastError)
}

func TestTranscribeNoteEmptyTranscriptMarksEmptyAndDoesNotGenerateReady(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(102)
	videoBytes := []byte("fake-mp4-empty")
	wavBytes := []byte("fake-wav-empty")

	note := createTranscribeTestNote(t, db, userID, "empty")
	cosURL := fmt.Sprintf("https://cos.example/xhs-script-media/%d/%d/video.mp4", userID, note.ID)

	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(_ context.Context, _ string, _ uint64) (string, func(), error) {
			videoPath := filepath.Join(t.TempDir(), "video.mp4")
			require.NoError(t, os.WriteFile(videoPath, videoBytes, 0o600))
			return videoPath, func() { _ = os.Remove(videoPath) }, nil
		},
		mirrorVideoBytesToCOS: func(_ context.Context, _ uint, _ uint64, data []byte) string {
			assert.Equal(t, videoBytes, data)
			return cosURL
		},
		extractAudio: func(_ context.Context, _ string, audioPath string) error {
			return os.WriteFile(audioPath, wavBytes, 0o600)
		},
		asr: func(_ context.Context, _ string, req aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
			assert.Equal(t, wavBytes, req.AudioBytes)
			return &aiservice.ASRResponse{Text: " \n\t "}, nil
		},
	})

	err := svc.transcribeNote(ctx, userID, note.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	got := loadTranscribeTestNote(t, db, userID, note.ID)
	assert.Equal(t, cosURL, got.VideoURL)
	if got.VideoTranscript != nil {
		assert.Equal(t, "", strings.TrimSpace(*got.VideoTranscript))
	}
	assert.Equal(t, model.XhsScriptTranscribeEmpty, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateNotReady, got.GenerateStatus)
	assert.Contains(t, got.LastError, "为空")
}

func TestTranscribeNoteDownloadFailureMarksFailed(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(103)
	downloadErr := errors.New("download boom")

	note := createTranscribeTestNote(t, db, userID, "download-failed")

	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(context.Context, string, uint64) (string, func(), error) {
			return "", func() {}, downloadErr
		},
		mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string {
			t.Fatal("mirror should not be called when download fails")
			return ""
		},
		extractAudio: func(context.Context, string, string) error {
			t.Fatal("extractAudio should not be called when download fails")
			return nil
		},
		asr: func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
			t.Fatal("ASR should not be called when download fails")
			return nil, nil
		},
	})

	err := svc.transcribeNote(ctx, userID, note.ID)
	require.ErrorIs(t, err, downloadErr)

	got := loadTranscribeTestNote(t, db, userID, note.ID)
	assert.Equal(t, "https://sns-video.xhscdn.com/download-failed.mp4", got.VideoURL)
	assert.Nil(t, got.VideoTranscript)
	assert.Equal(t, model.XhsScriptTranscribeFailed, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateNotReady, got.GenerateStatus)
	assert.Contains(t, got.LastError, "download boom")
}

type transcribeTestDeps struct {
	downloadVideoToTemp   func(context.Context, string, uint64) (string, func(), error)
	extractAudio          func(context.Context, string, string) error
	readFile              func(string) ([]byte, error)
	asr                   func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error)
	mirrorVideoBytesToCOS func(context.Context, uint, uint64, []byte) string
}

func withTranscribeTestDeps(t *testing.T, deps transcribeTestDeps) {
	t.Helper()
	prevDownload := downloadVideoToTempFn
	prevExtract := extractAudioFn
	prevRead := readFileFn
	prevASR := xhsScriptASRFn
	prevMirror := mirrorVideoBytesToCOSFn

	if deps.downloadVideoToTemp != nil {
		downloadVideoToTempFn = deps.downloadVideoToTemp
	}
	if deps.extractAudio != nil {
		extractAudioFn = deps.extractAudio
	}
	if deps.readFile != nil {
		readFileFn = deps.readFile
	}
	if deps.asr != nil {
		xhsScriptASRFn = deps.asr
	}
	if deps.mirrorVideoBytesToCOS != nil {
		mirrorVideoBytesToCOSFn = deps.mirrorVideoBytesToCOS
	}

	t.Cleanup(func() {
		downloadVideoToTempFn = prevDownload
		extractAudioFn = prevExtract
		readFileFn = prevRead
		xhsScriptASRFn = prevASR
		mirrorVideoBytesToCOSFn = prevMirror
	})
}

func newXhsScriptTranscribeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_script_transcribe_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptNote{},
		&model.XhsScriptAnalyticsEvent{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func createTranscribeTestNote(t *testing.T, db *gorm.DB, userID uint, sourceNoteID string) *model.XhsScriptNote {
	t.Helper()
	note := &model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     sourceNoteID,
		NoteURL:          "https://www.xiaohongshu.com/explore/" + sourceNoteID,
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "视频笔记",
		VideoURL:         "https://sns-video.xhscdn.com/" + sourceNoteID + ".mp4",
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}
	require.NoError(t, db.Create(note).Error)
	return note
}

func loadTranscribeTestNote(t *testing.T, db *gorm.DB, userID uint, noteID uint64) model.XhsScriptNote {
	t.Helper()
	var note model.XhsScriptNote
	require.NoError(t, db.Where("user_id = ? AND id = ?", userID, noteID).First(&note).Error)
	return note
}
