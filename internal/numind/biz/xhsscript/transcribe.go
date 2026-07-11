package xhsscript

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

var (
	downloadVideoToTempFn   = downloadVideoToTemp
	extractAudioFn          = extractAudio
	readFileFn              = os.ReadFile
	xhsScriptASRFn          = aiservice.ASR
	mirrorVideoBytesToCOSFn = mirrorVideoBytesToCOS
	probeVideoDurationFn    = probeVideoDuration
)

const (
	xhsScriptTranscribeTimeout       = 30 * time.Minute
	defaultXhsScriptMaxVideoBytes    = 500 * 1024 * 1024
	defaultXhsScriptMaxVideoDuration = 30 * time.Minute
	xhsScriptLastErrorVideoTooLarge  = "video_too_large"
	xhsScriptLastErrorVideoTooLong   = "video_too_long"
	xhsScriptLastErrorDownloadFailed = "video_download_failed"
)

func (s *Service) enqueueTranscription(userID uint, noteID uint64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), xhsScriptTranscribeTimeout)
		defer cancel()
		if err := s.transcribeNote(ctx, userID, noteID); err != nil {
			log.Errorw("xhs-script transcribe failed", "user_id", userID, "note_id", noteID, "error", err)
		}
	}()
}

func (s *Service) RequestTranscription(ctx context.Context, userID uint, noteID uint64) (*NoteDTO, error) {
	note, err := s.ds.XhsScript().GetNote(ctx, userID, noteID)
	if err != nil {
		return nil, errno.ErrXhsScriptNoteNotFound
	}
	if note.NoteType != model.XhsScriptNoteTypeVideo || strings.TrimSpace(note.VideoURL) == "" {
		return nil, errno.ErrXhsScriptVideoOnly
	}
	if note.TranscribeStatus == model.XhsScriptTranscribeReady && note.VideoTranscript != nil && strings.TrimSpace(*note.VideoTranscript) != "" {
		dto, dtoErr := s.noteDTO(ctx, note)
		if dtoErr != nil {
			return nil, dtoErr
		}
		return &dto, nil
	}
	if note.TranscribeStatus == model.XhsScriptTranscribeTranscribing {
		dto, dtoErr := s.noteDTO(ctx, note)
		if dtoErr != nil {
			return nil, dtoErr
		}
		return &dto, nil
	}

	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	remaining := account.FreeRemaining + account.PaidRemaining
	if remaining <= 0 {
		s.RecordEventBestEffort(ctx, userID, "transcribe_blocked_quota_insufficient", mergeAnalyticsProperties(transcribeNoteProperties(note), map[string]interface{}{
			"remaining": remaining,
		}))
		return nil, errno.ErrXhsScriptQuotaInsufficient
	}

	if err := s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribePending, nil, ""); err != nil {
		return nil, err
	}
	if err := s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateNotReady, ""); err != nil {
		return nil, err
	}
	s.RecordEventBestEffort(ctx, userID, "video_transcribe_requested", transcribeNoteProperties(note))
	s.enqueueTranscription(userID, noteID)
	return s.GetNoteDTO(ctx, userID, noteID)
}

func (s *Service) transcribeNote(ctx context.Context, userID uint, noteID uint64) error {
	select {
	case s.transcribeSem <- struct{}{}:
		defer func() { <-s.transcribeSem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	note, err := s.ds.XhsScript().GetNote(ctx, userID, noteID)
	if err != nil {
		return err
	}
	if note.TranscribeStatus == model.XhsScriptTranscribeReady && note.VideoTranscript != nil && strings.TrimSpace(*note.VideoTranscript) != "" {
		return nil
	}
	baseProps := transcribeNoteProperties(note)
	s.RecordEventBestEffort(ctx, userID, "video_transcribe_started", baseProps)
	if err := s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeTranscribing, nil, ""); err != nil {
		s.recordTranscribeFail(ctx, userID, baseProps, "mark_transcribing", err)
		return err
	}
	_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateNotReady, "")

	videoPath, cleanup, err := downloadVideoToTempFn(ctx, note.VideoURL, noteID)
	if err != nil {
		errorCategory := analyticsErrorCategory(err)
		lastError := xhsScriptLastErrorDownloadFailed
		if errorCategory == xhsScriptLastErrorVideoTooLarge {
			lastError = xhsScriptLastErrorVideoTooLarge
		}
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, lastError)
		props := mergeAnalyticsProperties(baseProps, map[string]interface{}{
			"stage":          "download",
			"error_category": errorCategory,
		})
		s.RecordEventBestEffort(ctx, userID, "video_download_fail", props)
		s.RecordEventBestEffort(ctx, userID, "video_transcribe_fail", props)
		return err
	}
	defer cleanup()
	var videoSize int64
	if stat, statErr := os.Stat(videoPath); statErr == nil {
		videoSize = stat.Size()
	}
	videoDuration, durationErr := probeVideoDurationFn(ctx, videoPath)
	if durationErr != nil {
		log.C(ctx).Warnw("xhs-script video duration probe failed, continue transcribing", "user_id", userID, "note_id", noteID, "error", durationErr)
	} else if videoDuration > maxXhsScriptVideoDuration() {
		err := fmt.Errorf("视频时长超过限制")
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, xhsScriptLastErrorVideoTooLong)
		props := mergeAnalyticsProperties(baseProps, map[string]interface{}{
			"stage":                  "validate_duration",
			"error_category":         analyticsErrorCategory(err),
			"video_duration_seconds": videoDuration.Seconds(),
		})
		s.RecordEventBestEffort(ctx, userID, "video_transcribe_fail", props)
		return err
	}
	downloadSuccessProps := map[string]interface{}{
		"video_size_bytes": videoSize,
	}
	if videoDuration > 0 {
		downloadSuccessProps["video_duration_seconds"] = videoDuration.Seconds()
	}
	s.RecordEventBestEffort(ctx, userID, "video_download_success", mergeAnalyticsProperties(baseProps, downloadSuccessProps))

	if videoBytes, readErr := readFileFn(videoPath); readErr != nil {
		log.C(ctx).Warnw("xhs-script mirror video read failed, keep original url", "user_id", userID, "note_id", noteID, "error", readErr)
	} else if cosURL := mirrorVideoBytesToCOSFn(ctx, userID, noteID, videoBytes); strings.TrimSpace(cosURL) != "" {
		if err := s.ds.XhsScript().UpdateNoteVideoURL(ctx, userID, noteID, cosURL); err != nil {
			log.C(ctx).Warnw("xhs-script mirror video url persist failed, keep transcribing", "user_id", userID, "note_id", noteID, "error", err)
		} else {
			note.VideoURL = cosURL
		}
	}

	audioPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".wav"
	if err := extractAudioFn(ctx, videoPath, audioPath); err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, "audio_extract_failed")
		s.recordTranscribeFail(ctx, userID, baseProps, "extract_audio", err)
		return err
	}
	audioBytes, err := readFileFn(audioPath)
	if err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, "audio_read_failed")
		s.recordTranscribeFail(ctx, userID, baseProps, "read_audio", err)
		return fmt.Errorf("read extracted audio: %w", err)
	}

	aiCtx := aimw.WithUserID(ctx, userID)
	aiCtx = aiservice.WithSkipLegacyBilling(aiCtx)
	resp, err := xhsScriptASRFn(aiCtx, profile.XhsTranscribe, aiservice.ASRRequest{
		AudioBytes:  audioBytes,
		AudioFormat: "wav",
		Language:    "zh",
	})
	if err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, "asr_failed")
		s.recordTranscribeFail(ctx, userID, baseProps, "asr", err)
		return fmt.Errorf("asr: %w", err)
	}
	transcript := strings.TrimSpace(resp.Text)
	if transcript == "" {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeEmpty, nil, "transcript_empty")
		props := mergeAnalyticsProperties(baseProps, map[string]interface{}{
			"stage":          "asr",
			"error_category": "transcript_empty",
		})
		s.RecordEventBestEffort(ctx, userID, "video_transcript_empty", props)
		return fmt.Errorf("asr transcript empty")
	}
	if err := s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeReady, &transcript, ""); err != nil {
		s.recordTranscribeFail(ctx, userID, baseProps, "mark_ready", err)
		return err
	}
	if err := s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateReady, ""); err != nil {
		s.recordTranscribeFail(ctx, userID, baseProps, "mark_generate_ready", err)
		return err
	}
	s.RecordEventBestEffort(ctx, userID, "video_transcribe_success", mergeAnalyticsProperties(baseProps, map[string]interface{}{
		"transcript_length": textLength(transcript),
	}))
	return nil
}

func mirrorVideoBytesToCOS(ctx context.Context, userID uint, noteID uint64, data []byte) string {
	if len(data) == 0 || !util.IsCOSEnabled() {
		return ""
	}
	key := fmt.Sprintf("xhs-script-media/%d/%d/video.mp4", userID, noteID)
	cosURL, err := util.UploadBytesToCOS(ctx, key, "video/mp4", data)
	if err != nil {
		log.C(ctx).Warnw("xhs-script mirror video upload failed", "user_id", userID, "note_id", noteID, "error", err)
		return ""
	}
	return cosURL
}

func transcribeNoteProperties(note *model.XhsScriptNote) map[string]interface{} {
	return map[string]interface{}{
		"note_id":           note.ID,
		"source_note_id":    note.SourceNoteID,
		"note_type":         note.NoteType,
		"has_video_url":     strings.TrimSpace(note.VideoURL) != "",
		"transcribe_status": note.TranscribeStatus,
		"generate_status":   note.GenerateStatus,
	}
}

func (s *Service) recordTranscribeFail(ctx context.Context, userID uint, baseProps map[string]interface{}, stage string, err error) {
	s.RecordEventBestEffort(ctx, userID, "video_transcribe_fail", mergeAnalyticsProperties(baseProps, map[string]interface{}{
		"stage":          stage,
		"error_category": analyticsErrorCategory(err),
	}))
}

func downloadVideoToTemp(ctx context.Context, videoURL string, noteID uint64) (string, func(), error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return "", func() {}, fmt.Errorf("视频地址为空")
	}
	dir := strings.TrimSpace(viper.GetString("xhs_script.temp_dir"))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "numind-xhs-script")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}, err
	}
	videoPath := filepath.Join(dir, fmt.Sprintf("note-%d-%d.mp4", noteID, time.Now().UnixNano()))
	cleanup := func() {
		_ = os.Remove(videoPath)
		_ = os.Remove(strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".wav")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return "", cleanup, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/126 Safari/537.36")
	req.Header.Set("Referer", "https://www.xiaohongshu.com/")

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", cleanup, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", cleanup, fmt.Errorf("下载视频失败: HTTP %d", resp.StatusCode)
	}

	file, err := os.Create(videoPath)
	if err != nil {
		return "", cleanup, err
	}
	defer file.Close()

	maxBytes := maxXhsScriptVideoBytes()
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", cleanup, err
	}
	if written > maxBytes {
		return "", cleanup, fmt.Errorf("视频文件超过大小限制")
	}
	return videoPath, cleanup, nil
}

func maxXhsScriptVideoBytes() int64 {
	maxBytes := viper.GetInt64("xhs_script.max_video_bytes")
	if maxBytes > 0 {
		return maxBytes
	}
	return defaultXhsScriptMaxVideoBytes
}

func maxXhsScriptVideoDuration() time.Duration {
	maxSeconds := viper.GetFloat64("xhs_script.max_video_duration_seconds")
	if maxSeconds > 0 {
		return time.Duration(maxSeconds * float64(time.Second))
	}
	return defaultXhsScriptMaxVideoDuration
}

func probeVideoDuration(ctx context.Context, videoPath string) (time.Duration, error) {
	ffprobe := ffprobePath()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe 获取视频时长失败: %s: %w", limitForPrompt(string(out), 500), err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe 视频时长解析失败: %w", err)
	}
	if seconds <= 0 {
		return 0, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func ffprobePath() string {
	if ffprobe := strings.TrimSpace(viper.GetString("xhs_script.ffprobe_path")); ffprobe != "" {
		return ffprobe
	}
	if ffprobe := siblingFFprobePath(viper.GetString("xhs_script.ffmpeg_path")); ffprobe != "" {
		return ffprobe
	}
	if ffprobe := strings.TrimSpace(viper.GetString("monitor.ffprobe_path")); ffprobe != "" {
		return ffprobe
	}
	if ffprobe := siblingFFprobePath(viper.GetString("monitor.ffmpeg_path")); ffprobe != "" {
		return ffprobe
	}
	return "ffprobe"
}

func siblingFFprobePath(ffmpeg string) string {
	ffmpeg = strings.TrimSpace(ffmpeg)
	if ffmpeg == "" {
		return ""
	}
	dir, base := filepath.Split(ffmpeg)
	if !strings.Contains(base, "ffmpeg") {
		return ""
	}
	return filepath.Join(dir, strings.Replace(base, "ffmpeg", "ffprobe", 1))
}

func extractAudio(ctx context.Context, videoPath, audioPath string) error {
	ffmpeg := strings.TrimSpace(viper.GetString("xhs_script.ffmpeg_path"))
	if ffmpeg == "" {
		ffmpeg = strings.TrimSpace(viper.GetString("monitor.ffmpeg_path"))
	}
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-i", videoPath, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", audioPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 提取音频失败: %s: %w", limitForPrompt(string(out), 500), err)
	}
	return nil
}
