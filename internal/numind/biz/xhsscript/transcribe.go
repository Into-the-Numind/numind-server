package xhsscript

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

func (s *Service) enqueueTranscription(userID uint, noteID uint64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		if err := s.transcribeNote(ctx, userID, noteID); err != nil {
			log.Errorw("xhs-script transcribe failed", "user_id", userID, "note_id", noteID, "error", err)
		}
	}()
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
	if err := s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeTranscribing, nil, ""); err != nil {
		return err
	}
	_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateNotReady, "")

	videoPath, cleanup, err := downloadVideoToTemp(ctx, note.VideoURL, noteID)
	if err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, err.Error())
		return err
	}
	defer cleanup()

	audioPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".wav"
	if err := extractAudio(ctx, videoPath, audioPath); err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, err.Error())
		return err
	}
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, err.Error())
		return fmt.Errorf("read extracted audio: %w", err)
	}

	aiCtx := aimw.WithUserID(ctx, userID)
	aiCtx = aiservice.WithSkipLegacyBilling(aiCtx)
	resp, err := aiservice.ASR(aiCtx, profile.XhsTranscribe, aiservice.ASRRequest{
		AudioBytes:  audioBytes,
		AudioFormat: "wav",
		Language:    "zh",
	})
	if err != nil {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeFailed, nil, err.Error())
		return fmt.Errorf("asr: %w", err)
	}
	transcript := strings.TrimSpace(resp.Text)
	if transcript == "" {
		_ = s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeEmpty, nil, "视频转写结果为空")
		return fmt.Errorf("asr transcript empty")
	}
	if err := s.ds.XhsScript().UpdateTranscribeStatus(ctx, userID, noteID, model.XhsScriptTranscribeReady, &transcript, ""); err != nil {
		return err
	}
	return s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateReady, "")
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

	maxBytes := viper.GetInt64("xhs_script.max_video_bytes")
	if maxBytes <= 0 {
		maxBytes = 120 * 1024 * 1024
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", cleanup, err
	}
	if written > maxBytes {
		return "", cleanup, fmt.Errorf("视频文件超过大小限制")
	}
	return videoPath, cleanup, nil
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
