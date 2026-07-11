package xhsscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestTranscribeRuntimeLimits(t *testing.T) {
	assert.Equal(t, 30*time.Minute, xhsScriptTranscribeTimeout)
	assert.Equal(t, int64(500*1024*1024), int64(defaultXhsScriptMaxVideoBytes))
	assert.Equal(t, 30*time.Minute, defaultXhsScriptMaxVideoDuration)
}

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
	assert.Equal(t, "transcript_empty", got.LastError)
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
	assert.Equal(t, "video_download_failed", got.LastError)
	assert.NotContains(t, got.LastError, "download boom")
}

func TestTranscribeNoteVideoTooLargeMarksSpecificError(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(104)
	downloadErr := errors.New("视频文件超过大小限制")

	note := createTranscribeTestNote(t, db, userID, "download-too-large")

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
	assert.Equal(t, model.XhsScriptTranscribeFailed, got.TranscribeStatus)
	assert.Equal(t, xhsScriptLastErrorVideoTooLarge, got.LastError)
}

func TestTranscribeNoteVideoTooLongMarksSpecificError(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(105)
	tmpDir := t.TempDir()

	note := createTranscribeTestNote(t, db, userID, "video-too-long")

	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(_ context.Context, _ string, noteID uint64) (string, func(), error) {
			assert.Equal(t, note.ID, noteID)
			videoPath := filepath.Join(tmpDir, "too-long.mp4")
			require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o600))
			return videoPath, func() { _ = os.Remove(videoPath) }, nil
		},
		probeVideoDuration: func(context.Context, string) (time.Duration, error) {
			return 31 * time.Minute, nil
		},
		mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string {
			t.Fatal("mirror should not be called when video is too long")
			return ""
		},
		extractAudio: func(context.Context, string, string) error {
			t.Fatal("extractAudio should not be called when video is too long")
			return nil
		},
		asr: func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
			t.Fatal("ASR should not be called when video is too long")
			return nil, nil
		},
	})

	err := svc.transcribeNote(ctx, userID, note.ID)
	require.Error(t, err)

	got := loadTranscribeTestNote(t, db, userID, note.ID)
	assert.Equal(t, model.XhsScriptTranscribeFailed, got.TranscribeStatus)
	assert.Equal(t, xhsScriptLastErrorVideoTooLong, got.LastError)
}

func TestRequestTranscriptionEnqueuesFailedNoteWhenQuotaAvailable(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(111)
	transcript := "重新转写后的逐字稿"
	tmpDir := t.TempDir()

	note := createTranscribeTestNote(t, db, userID, "manual-retry")
	require.NoError(t, db.Model(note).Updates(map[string]interface{}{
		"transcribe_status": model.XhsScriptTranscribeFailed,
		"last_error":        "asr_failed",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 1,
	}).Error)

	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(_ context.Context, _ string, noteID uint64) (string, func(), error) {
			assert.Equal(t, note.ID, noteID)
			videoPath := filepath.Join(tmpDir, fmt.Sprintf("manual-retry-%d.mp4", time.Now().UnixNano()))
			require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o600))
			return videoPath, func() { _ = os.Remove(videoPath) }, nil
		},
		mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string { return "" },
		extractAudio: func(_ context.Context, _ string, audioPath string) error {
			return os.WriteFile(audioPath, []byte("wav"), 0o600)
		},
		asr: func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
			return &aiservice.ASRResponse{Text: transcript}, nil
		},
	})

	dto, err := svc.RequestTranscription(ctx, userID, note.ID)
	require.NoError(t, err)
	require.NotNil(t, dto)
	assert.Contains(t, []string{"waiting_transcript", "transcribing", "ready_to_generate"}, dto.State)

	require.Eventually(t, func() bool {
		got := loadTranscribeTestNote(t, db, userID, note.ID)
		return got.TranscribeStatus == model.XhsScriptTranscribeReady &&
			got.VideoTranscript != nil &&
			*got.VideoTranscript == transcript &&
			got.GenerateStatus == model.XhsScriptGenerateReady
	}, time.Second, 10*time.Millisecond)
}

func TestRequestTranscriptionRejectsWhenQuotaExhausted(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptTranscribeTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(112)

	note := createTranscribeTestNote(t, db, userID, "manual-no-quota")
	require.NoError(t, db.Model(note).Updates(map[string]interface{}{
		"transcribe_status": model.XhsScriptTranscribeFailed,
		"last_error":        "download_failed",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{UserID: userID}).Error)

	withTranscribeTestDeps(t, transcribeTestDeps{
		downloadVideoToTemp: func(context.Context, string, uint64) (string, func(), error) {
			t.Fatal("manual transcription should not start without quota")
			return "", func() {}, nil
		},
	})

	dto, err := svc.RequestTranscription(ctx, userID, note.ID)
	require.ErrorIs(t, err, errno.ErrXhsScriptQuotaInsufficient)
	assert.Nil(t, dto)

	got := loadTranscribeTestNote(t, db, userID, note.ID)
	assert.Equal(t, model.XhsScriptTranscribeFailed, got.TranscribeStatus)
	assert.Equal(t, model.XhsScriptGenerateNotReady, got.GenerateStatus)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_name = ?", "transcribe_blocked_quota_insufficient").First(&event).Error)
}

func TestTranscribeNoteFailuresPersistSafeLastErrorCategories(t *testing.T) {
	t.Run("extract audio raw ffmpeg error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptTranscribeTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(104)
		note := createTranscribeTestNote(t, db, userID, "extract-failed")
		rawErr := errors.New("ffmpeg stderr: secret file path /tmp/private prompt text")

		withTranscribeTestDeps(t, transcribeTestDeps{
			downloadVideoToTemp: func(_ context.Context, _ string, _ uint64) (string, func(), error) {
				videoPath := filepath.Join(t.TempDir(), "video.mp4")
				require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o600))
				return videoPath, func() { _ = os.Remove(videoPath) }, nil
			},
			mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string { return "" },
			extractAudio: func(context.Context, string, string) error {
				return rawErr
			},
		})

		err := svc.transcribeNote(ctx, userID, note.ID)
		require.ErrorIs(t, err, rawErr)
		got := loadTranscribeTestNote(t, db, userID, note.ID)
		assert.Equal(t, "audio_extract_failed", got.LastError)
		assert.NotContains(t, got.LastError, "ffmpeg stderr")
		assert.NotContains(t, got.LastError, "/tmp/private")
	})

	t.Run("audio read raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptTranscribeTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(105)
		note := createTranscribeTestNote(t, db, userID, "read-failed")
		rawErr := errors.New("read /tmp/private.wav: permission denied with secret")

		withTranscribeTestDeps(t, transcribeTestDeps{
			downloadVideoToTemp: func(_ context.Context, _ string, _ uint64) (string, func(), error) {
				videoPath := filepath.Join(t.TempDir(), "video.mp4")
				require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o600))
				return videoPath, func() { _ = os.Remove(videoPath) }, nil
			},
			mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string { return "" },
			extractAudio:          func(context.Context, string, string) error { return nil },
			readFile: func(path string) ([]byte, error) {
				if strings.HasSuffix(path, ".wav") {
					return nil, rawErr
				}
				return []byte("video"), nil
			},
		})

		err := svc.transcribeNote(ctx, userID, note.ID)
		require.Error(t, err)
		got := loadTranscribeTestNote(t, db, userID, note.ID)
		assert.Equal(t, "audio_read_failed", got.LastError)
		assert.NotContains(t, got.LastError, "/tmp/private.wav")
	})

	t.Run("asr raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptTranscribeTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(106)
		note := createTranscribeTestNote(t, db, userID, "asr-failed")
		rawErr := errors.New("asr provider returned transcript text and credential detail")

		withTranscribeTestDeps(t, transcribeTestDeps{
			downloadVideoToTemp: func(_ context.Context, _ string, _ uint64) (string, func(), error) {
				videoPath := filepath.Join(t.TempDir(), "video.mp4")
				require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o600))
				return videoPath, func() { _ = os.Remove(videoPath) }, nil
			},
			mirrorVideoBytesToCOS: func(context.Context, uint, uint64, []byte) string { return "" },
			extractAudio: func(_ context.Context, _ string, audioPath string) error {
				return os.WriteFile(audioPath, []byte("wav"), 0o600)
			},
			asr: func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
				return nil, rawErr
			},
		})

		err := svc.transcribeNote(ctx, userID, note.ID)
		require.Error(t, err)
		got := loadTranscribeTestNote(t, db, userID, note.ID)
		assert.Equal(t, "asr_failed", got.LastError)
		assert.NotContains(t, got.LastError, "provider returned")
		assert.NotContains(t, got.LastError, "credential")
	})
}

type transcribeTestDeps struct {
	downloadVideoToTemp   func(context.Context, string, uint64) (string, func(), error)
	extractAudio          func(context.Context, string, string) error
	readFile              func(string) ([]byte, error)
	asr                   func(context.Context, string, aiservice.ASRRequest) (*aiservice.ASRResponse, error)
	mirrorVideoBytesToCOS func(context.Context, uint, uint64, []byte) string
	probeVideoDuration    func(context.Context, string) (time.Duration, error)
}

func withTranscribeTestDeps(t *testing.T, deps transcribeTestDeps) {
	t.Helper()
	prevDownload := downloadVideoToTempFn
	prevExtract := extractAudioFn
	prevRead := readFileFn
	prevASR := xhsScriptASRFn
	prevMirror := mirrorVideoBytesToCOSFn
	prevProbeDuration := probeVideoDurationFn

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
	if deps.probeVideoDuration != nil {
		probeVideoDurationFn = deps.probeVideoDuration
	}

	t.Cleanup(func() {
		downloadVideoToTempFn = prevDownload
		extractAudioFn = prevExtract
		readFileFn = prevRead
		xhsScriptASRFn = prevASR
		mirrorVideoBytesToCOSFn = prevMirror
		probeVideoDurationFn = prevProbeDuration
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
		&model.XhsScriptQuotaAccount{},
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
