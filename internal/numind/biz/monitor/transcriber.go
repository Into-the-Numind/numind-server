package monitor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ffmpegSem 包级别 FFmpeg 并发信号量
var (
	ffmpegSem     chan struct{}
	ffmpegSemOnce sync.Once
)

// initFFmpegSem 初始化 FFmpeg 并发信号量（需在启动时调用一次）
func initFFmpegSem() {
	n := viper.GetInt("monitor.crawler.max_concurrent_ffmpeg")
	if n <= 0 {
		n = 2
	}
	ffmpegSem = make(chan struct{}, n)
}

// ensureFFmpegSem 惰性初始化 FFmpeg 信号量
func ensureFFmpegSem() {
	ffmpegSemOnce.Do(initFFmpegSem)
}

// ffmpegPath 获取 FFmpeg 可执行文件路径
func ffmpegPath() string {
	p := viper.GetString("monitor.ffmpeg_path")
	if p == "" {
		return "ffmpeg"
	}
	return p
}

// tempDir 获取临时文件目录
func tempDir() string {
	d := viper.GetString("monitor.temp_dir")
	if d == "" {
		return "/tmp/numind-monitor/"
	}
	return d
}

// ensureTempDir 确保临时目录存在
func ensureTempDir() error {
	return os.MkdirAll(tempDir(), 0o755)
}

// CleanupTempDir 清理临时目录中超过 1 小时的旧文件
func (mb *MonitorBiz) CleanupTempDir() error {
	dir := tempDir()
	if err := ensureTempDir(); err != nil {
		return fmt.Errorf("CleanupTempDir: ensure dir: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("CleanupTempDir: read dir: %w", err)
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				log.Warnw("CleanupTempDir: remove old file failed", "path", path, "error", err)
			} else {
				log.Infow("CleanupTempDir: removed old temp file", "path", path)
			}
		}
	}
	return nil
}

// TranscribeVideo 视频转文字管道：下载视频 → FFmpeg 提取音频 → FunASR 识别 → 更新笔记
// 如果任何步骤失败，记录日志但不返回 error 以避免阻塞整体爬取流程。
func (mb *MonitorBiz) TranscribeVideo(ctx context.Context, note *model.MonitorNote) error {
	if err := ensureTempDir(); err != nil {
		log.Errorw("TranscribeVideo: ensure temp dir failed", "error", err)
		return fmt.Errorf("TranscribeVideo: ensure temp dir: %w", err)
	}

	ensureFFmpegSem()

	// 1. 下载视频到临时文件
	videoPath := filepath.Join(tempDir(), fmt.Sprintf("video_%d_%d.mp4", note.ID, time.Now().UnixNano()))
	if err := mb.downloadVideo(ctx, note.XhsNoteID, videoPath); err != nil {
		log.Errorw("TranscribeVideo: download video failed", "noteID", note.ID, "error", err)
		return fmt.Errorf("TranscribeVideo: download: %w", err)
	}
	defer os.Remove(videoPath)

	// 2. FFmpeg 提取音频
	audioPath := filepath.Join(tempDir(), fmt.Sprintf("audio_%d_%d.wav", note.ID, time.Now().UnixNano()))
	if err := mb.extractAudio(ctx, videoPath, audioPath); err != nil {
		log.Errorw("TranscribeVideo: extract audio failed", "noteID", note.ID, "error", err)
		return fmt.Errorf("TranscribeVideo: extract audio: %w", err)
	}
	defer os.Remove(audioPath)

	// 3. 读取音频文件字节并通过 AI Gateway 进行语音识别
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		log.Errorw("TranscribeVideo: read audio file failed", "noteID", note.ID, "error", err)
		return fmt.Errorf("TranscribeVideo: read audio: %w", err)
	}

	// 注入 Gateway 中间件上下文（userID 从 billing context 读取）
	asrCtx := ctx
	if bc := billing.FromContext(ctx); bc != nil {
		asrCtx = aismw.WithUserID(asrCtx, bc.UserID)
	}
	asrCtx = aiservice.WithSkipLegacyBilling(asrCtx)

	asrResp, err := aiservice.ASR(asrCtx, profile.MonitorTranscribe, aiservice.ASRRequest{
		AudioBytes:  audioBytes,
		AudioFormat: "wav",
		Language:    "zh",
	})
	if err != nil {
		log.Errorw("TranscribeVideo: recognize audio failed", "noteID", note.ID, "error", err)
		return fmt.Errorf("TranscribeVideo: recognize: %w", err)
	}
	transcript := asrResp.Text

	// 4. 更新笔记的 Transcript 字段
	note.Transcript = transcript
	if err := mb.store.Monitor().UpdateNote(ctx, note); err != nil {
		log.Errorw("TranscribeVideo: update note transcript failed", "noteID", note.ID, "error", err)
		return fmt.Errorf("TranscribeVideo: update note: %w", err)
	}

	log.Infow("TranscribeVideo: completed", "noteID", note.ID, "transcriptLen", len(transcript))
	return nil
}

// downloadVideo 从 xhs-service 下载视频到本地文件
func (mb *MonitorBiz) downloadVideo(ctx context.Context, xhsNoteID, destPath string) error {
	url := fmt.Sprintf("%s/xhs/download-video/%s", xhsServiceBaseURL(), xhsNoteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("downloadVideo: build request: %w", err)
	}

	client := newXhsHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloadVideo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("downloadVideo: status %d, body: %s", resp.StatusCode, string(body))
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("downloadVideo: create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("downloadVideo: write file: %w", err)
	}
	return nil
}

// extractAudio 使用 FFmpeg 从视频中提取音频
func (mb *MonitorBiz) extractAudio(ctx context.Context, videoPath, audioPath string) error {
	// 获取 FFmpeg 并发 slot
	ffmpegSem <- struct{}{}
	defer func() { <-ffmpegSem }()

	cmd := exec.CommandContext(ctx, ffmpegPath(),
		"-i", videoPath,
		"-vn",
		"-acodec", "pcm_s16le",
		"-ar", "16000",
		"-ac", "1",
		"-y", // 覆盖输出文件
		audioPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("extractAudio: ffmpeg failed: %w, output: %s", err, string(output))
	}
	return nil
}

