package xhs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ── 计费 / 转写参数 ───────────────────────────────────────────────────────────

const (
	// defaultASRCreditsPerSecond 是 viper "xhs.asr_credits_per_second" 未配置时的兜底费率。
	// design §4.3：0.008 积分/秒（= 0.00008 元/秒 ÷ 0.01 元/积分）。
	defaultASRCreditsPerSecond = 0.008
	// asrReserveCapSeconds 是 Reserve 保守额的时长上限（秒）。视频时长未知时按此上限预扣，
	// ASR 后按实际 DurationSeconds 多退少补（design §4.3）。
	asrReserveCapSeconds = 600.0
	// asrDownloadTimeout 是下载 CDN 直链 + 整体转写的子超时（在 job 的 5min detached ctx 内）。
	asrDownloadTimeout = 90 * time.Second
)

// errVideoLinkExpired 表示小红书 CDN 视频直链已失效（HTTP 4xx）。
// 调用方据此把 enrich_status 置 partial、video_transcript 留 NULL，且不扣 ASR 费。
var errVideoLinkExpired = errors.New("xhs: video CDN link expired (4xx)")

// ── 依赖接口（窄接口，便于单测注入替身；生产由 credit.ICreditService / store.UserStore 满足） ──

// asrBiller 是 biz 层显式两阶段扣费所需的最小积分服务接口。
//
// design §4.3 的关键计费抉择：ASR 扣费**不动 gateway/context_budget**（那会误伤 monitor
// /会议副驾的 ASR 透传路径），改为在 xhs biz 层用 credit 服务显式 Reserve/Reconcile。
// credit.ICreditService 天然满足本接口（Reserve 接受显式积分额 estimated，
// Reconcile 接受显式 actualCostCents）。
type asrBiller interface {
	Reserve(ctx context.Context, user *model.User, op credit.Operation, estimated int64, coefID uint64, idempotencyKey *string) (*credit.Reservation, error)
	Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error
	Refund(ctx context.Context, reservationID uint64, reason string) error
}

// userGetter 按 ID 取 *model.User（Reserve 入参需要）。store.UserStore 满足本接口。
type userGetter interface {
	GetByID(ctx context.Context, userID uint) (*model.User, error)
}

// opXhsTranscribe 是 xhs 视频转写的计费操作标识。
//
// 故意不在 credit 包新增 Operation 枚举常量（避免改动他人/计费包）：credit 的
// referenceFromOp 对未知 Operation 有 default 分支（refType = string(op)），直接传字符串
// 即可，credit_reservation.reference_type 落 "xhs_transcribe"，账目可读且不侵入计费包。
const opXhsTranscribe = credit.Operation("xhs_transcribe")

// ── 可替换 seam（单测注入；生产指向真实实现） ─────────────────────────────────

var (
	// asrFn 默认指向 aiservice.ASR —— 唯一 AI 服务入口，保证 Langfuse 追踪 + 路由降级。
	// 禁止业务代码直接 import provider 包 / 裸 HTTP 调 ASR。
	asrFn = aiservice.ASR
	// downloadVideoFn 直接 HTTP GET 小红书 CDN 直链（design §4.3：**不经 monitor 的
	// xhs-service/8100**）。非 LLM/AI 调用，故走裸 HTTP 合规。
	downloadVideoFn = downloadVideoFromURL
	// extractAudioToWavFn 用 ffmpeg 抽 16kHz 单声道 wav。
	extractAudioToWavFn = extractAudioToWav
	// readAudioFn 读取抽好的 wav 字节（便于单测绕过真实文件 IO）。
	readAudioFn = os.ReadFile
)

// ── 转写结果 ──────────────────────────────────────────────────────────────────

// transcribeOutcome 描述视频转写阶段的终态（供 enrichOne 决定最终 enrich_status）。
type transcribeOutcome struct {
	// Status 是视频阶段的建议终态：done / partial / insufficient_credits。
	// enrichOne 据此聚合最终 enrich_status（AI 分析与视频段的状态合并）。
	Status string
}

// ── 计费费率 ──────────────────────────────────────────────────────────────────

// asrCreditsPerSecond 读取 viper "xhs.asr_credits_per_second"，<=0 兜底默认值。
func asrCreditsPerSecond() float64 {
	r := viper.GetFloat64("xhs.asr_credits_per_second")
	if r <= 0 {
		return defaultASRCreditsPerSecond
	}
	return r
}

// creditsForSeconds 按 call 级（非按秒）取整：ceil(seconds × 费率)（design §4.3 P2）。
// 结果至少为 1（有实际转写就至少扣 1 积分，避免极短视频 0 扣费）。
func creditsForSeconds(seconds, rate float64) int64 {
	if seconds <= 0 {
		return 0
	}
	c := int64(math.Ceil(seconds * rate))
	if c < 1 {
		c = 1
	}
	return c
}

// reserveCredits 计算 Reserve 保守额 = min(估时, 600s) × 费率，向上取整（至少 1）。
// 视频时长事先未知，按上限 600s 预扣，ASR 后按实际秒数 Reconcile 多退少补。
func reserveCredits(rate float64) int64 {
	return creditsForSeconds(asrReserveCapSeconds, rate)
}

// ── 转写主流程 ────────────────────────────────────────────────────────────────

// transcribeVideo 对一条视频笔记执行 ASR 转写并在 biz 层显式扣费。
//
// 流程（design §4.3）：
//  1. 下载 CDN 直链（downloadVideoFn，不经 xhs-service）；4xx → partial、transcript NULL、不扣费。
//  2. ffmpeg 抽 16kHz 单声道 wav（受 ffmpegSem 限流，复用 T3b 的本包信号量，不引 monitor 私有 sem）。
//  3. Reserve 保守额（biller 已 wired 时）；余额不足 → insufficient_credits、跳过 ASR、不报错。
//  4. aiservice.ASR 转写（注入 WithSkipLegacyBilling + userID —— 与 monitor/会议副驾一致，
//     对 gateway 计费保持中性，扣费只发生在本 biz 层）。
//  5. Reconcile ceil(实际秒 × 费率)（call 级取整）多退少补。
//
// 计费隔离不变量：本路径**绝不**改动 gateway / context_budget / billing 中间件——那会误伤
// monitor / 会议副驾的 ASR 路径。biller 为 nil（未 WithBiller）时退化为 monitor/会议语义：
// 不在 biz 层扣费。
//
// 返回 transcribeOutcome.Status（done/partial/insufficient_credits）+ error。仅基础设施级
// 错误（ffmpeg/读文件失败）返回 error；业务降级（直链失效/余额不足/ASR 失败）不返回 error，
// 以 Status 表达，避免阻塞整条富化（原始采集数据已落库）。
func (e *Enricher) transcribeVideo(ctx context.Context, userID uint, note *model.XhsTopicNote) (transcribeOutcome, error) {
	// 子超时：限制下载 + 转写整体时长（在 job 的 5min detached ctx 之内）。
	ctx, cancel := context.WithTimeout(ctx, asrDownloadTimeout)
	defer cancel()

	if err := ensureXhsTempDir(); err != nil {
		return transcribeOutcome{Status: model.XhsEnrichFailed}, fmt.Errorf("transcribeVideo: ensure temp dir: %w", err)
	}

	// 1. 下载视频到临时文件。
	videoPath := filepath.Join(xhsTempDir(), fmt.Sprintf("xhs_video_%d_%d.mp4", note.ID, time.Now().UnixNano()))
	if err := downloadVideoFn(ctx, note.VideoURL, videoPath); err != nil {
		if errors.Is(err, errVideoLinkExpired) {
			// 直链失效：partial + transcript NULL，不扣费（没调成 ASR）。
			log.C(ctx).Warnw("xhs transcribe: video link expired, marking partial",
				"note_id", note.ID, "video_url", note.VideoURL)
			recordTranscribeSpanError(ctx, userID, note.ID, err)
			note.VideoTranscript = nil
			return transcribeOutcome{Status: model.XhsEnrichPartial}, nil
		}
		// 其它下载错误（网络/IO）：视为部分失败，不阻塞 AI 分析结果落库。
		log.C(ctx).Errorw("xhs transcribe: download failed, marking partial", "note_id", note.ID, "error", err)
		recordTranscribeSpanError(ctx, userID, note.ID, err)
		return transcribeOutcome{Status: model.XhsEnrichPartial}, nil
	}
	defer os.Remove(videoPath)

	// 2. ffmpeg 抽音。受本包 ffmpegSem 限流（design §4.1：独立信号量，不引 monitor 私有 sem）：
	//    在此获取 slot（而非在 helper 内），让 extractAudioToWavFn seam 保持无状态便于单测替换。
	//    ctx 取消时不阻塞在 sem 上，避免 detached ctx 超时后仍卡队列。
	audioPath := filepath.Join(xhsTempDir(), fmt.Sprintf("xhs_audio_%d_%d.wav", note.ID, time.Now().UnixNano()))
	if err := e.withFFmpegSlot(ctx, func() error {
		return extractAudioToWavFn(ctx, videoPath, audioPath)
	}); err != nil {
		return transcribeOutcome{Status: model.XhsEnrichFailed}, fmt.Errorf("transcribeVideo: extract audio: %w", err)
	}
	defer os.Remove(audioPath)

	audioBytes, err := readAudioFn(audioPath)
	if err != nil {
		return transcribeOutcome{Status: model.XhsEnrichFailed}, fmt.Errorf("transcribeVideo: read audio: %w", err)
	}

	rate := asrCreditsPerSecond()

	// 3. Reserve 保守额（仅当 biller wired）。余额不足 → insufficient_credits，跳过 ASR。
	var reservation *credit.Reservation
	if e.biller != nil && e.userStore != nil {
		user, uErr := e.userStore.GetByID(ctx, userID)
		if uErr != nil {
			return transcribeOutcome{Status: model.XhsEnrichFailed}, fmt.Errorf("transcribeVideo: load user: %w", uErr)
		}
		rsv, rErr := e.biller.Reserve(ctx, user, opXhsTranscribe, reserveCredits(rate), 0, nil)
		if rErr != nil {
			if errors.Is(rErr, credit.ErrInsufficientCredits) {
				log.C(ctx).Infow("xhs transcribe: insufficient credits, skipping ASR",
					"note_id", note.ID, "user_id", userID)
				note.VideoTranscript = nil
				return transcribeOutcome{Status: model.XhsEnrichInsufficientCredits}, nil
			}
			return transcribeOutcome{Status: model.XhsEnrichFailed}, fmt.Errorf("transcribeVideo: reserve: %w", rErr)
		}
		reservation = rsv
	}

	// 4. ASR 转写。注入 gateway 中间件 ctx：WithSkipLegacyBilling + userID —— 与 monitor /
	//    会议副驾完全一致，对 gateway 计费保持中性（gateway 不对非 Chat 扣费）。扣费只发生
	//    在本 biz 层的 Reserve/Reconcile，确保不碰共享 gateway 计费路径。
	asrCtx := aismw.WithUserID(ctx, userID)
	asrCtx = aiservice.WithSkipLegacyBilling(asrCtx)

	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "xhs-video-transcribe",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("xhs-collector"),
		langfuse.WithTraceInput(map[string]interface{}{"note_id": note.ID}),
	)
	asrCtx = langfuse.WithTrace(asrCtx, traceID)

	asrResp, asrErr := asrFn(asrCtx, profile.MonitorTranscribe, aiservice.ASRRequest{
		AudioBytes:  audioBytes,
		AudioFormat: "wav",
		Language:    "zh",
	})
	if asrErr != nil {
		// ASR 失败：退还预扣（没产生实际消耗），置 partial 不阻塞 AI 结果。
		if reservation != nil {
			if rfErr := e.biller.Refund(ctx, reservation.ID, "xhs_asr_failed"); rfErr != nil {
				log.C(ctx).Errorw("xhs transcribe: refund after ASR failure failed",
					"note_id", note.ID, "reservation_id", reservation.ID, "error", rfErr)
			}
		}
		log.C(ctx).Errorw("xhs transcribe: ASR failed, marking partial", "note_id", note.ID, "error", asrErr)
		recordTranscribeSpanError(ctx, userID, note.ID, asrErr)
		note.VideoTranscript = nil
		return transcribeOutcome{Status: model.XhsEnrichPartial}, nil
	}

	// 5. Reconcile 按实际秒数 ceil 结算（call 级取整），多退少补。
	if reservation != nil {
		actual := creditsForSeconds(asrResp.DurationSeconds, rate)
		if rcErr := e.biller.Reconcile(ctx, reservation.ID, actual); rcErr != nil {
			// 对账失败仅告警：转写已成功、原始数据已落库；不回滚转写结果。
			log.C(ctx).Errorw("xhs transcribe: reconcile failed",
				"note_id", note.ID, "reservation_id", reservation.ID, "actual_credits", actual, "error", rcErr)
		}
	}

	transcript := asrResp.Text
	note.VideoTranscript = &transcript
	return transcribeOutcome{Status: model.XhsEnrichDone}, nil
}

// ── 下载 / ffmpeg / 临时目录 helper ───────────────────────────────────────────

// downloadVideoFromURL 直接 HTTP GET 小红书 CDN 视频直链到本地文件（design §4.3：
// **不经 monitor 的 xhs-service/8100**）。HTTP 4xx → 返回 errVideoLinkExpired，
// 调用方据此置 partial、transcript NULL、不扣费。
//
// 这是对 CDN 静态资源的裸 HTTP 下载（非 AI 服务调用），不在 aiservice 入口约束范围内。
func downloadVideoFromURL(ctx context.Context, videoURL, destPath string) error {
	if videoURL == "" {
		return fmt.Errorf("downloadVideoFromURL: empty video url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return fmt.Errorf("downloadVideoFromURL: build request: %w", err)
	}

	client := &http.Client{Timeout: asrDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloadVideoFromURL: %w", err)
	}
	defer resp.Body.Close()

	// 4xx → 直链失效（被签名过期 / 鉴权拒绝）；统一映射为 errVideoLinkExpired。
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: status %d", errVideoLinkExpired, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloadVideoFromURL: unexpected status %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("downloadVideoFromURL: create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("downloadVideoFromURL: write file: %w", err)
	}
	return nil
}

// withFFmpegSlot 在持有本包 ffmpegSem 并发 slot 的前提下执行 fn（design §4.1：
// 独立信号量限流视频转写的本地资源占用，不引用 monitor 包私有 sem）。
// ctx 取消时不阻塞在 sem 上而是返回 ctx.Err()，避免 detached ctx 超时后仍卡队列。
func (e *Enricher) withFFmpegSlot(ctx context.Context, fn func() error) error {
	select {
	case e.ffmpegSem <- struct{}{}:
		defer func() { <-e.ffmpegSem }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// extractAudioToWav 用 ffmpeg 从视频抽 16kHz 单声道 PCM wav（ASR 输入格式）。
// 并发限流由调用方（transcribeVideo via withFFmpegSlot）负责，本函数本身无状态。
func extractAudioToWav(ctx context.Context, videoPath, audioPath string) error {
	cmd := exec.CommandContext(ctx, xhsFFmpegPath(),
		"-i", videoPath,
		"-vn",
		"-acodec", "pcm_s16le",
		"-ar", "16000",
		"-ac", "1",
		"-y",
		audioPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("extractAudioToWav: ffmpeg failed: %w, output: %s", err, string(output))
	}
	return nil
}

// xhsFFmpegPath 复用 monitor 的 ffmpeg 路径配置（同机同二进制），未配置兜底 "ffmpeg"。
func xhsFFmpegPath() string {
	if p := viper.GetString("xhs.ffmpeg_path"); p != "" {
		return p
	}
	if p := viper.GetString("monitor.ffmpeg_path"); p != "" {
		return p
	}
	return "ffmpeg"
}

// xhsTempDir 返回 xhs 转写临时目录（与 monitor 隔离），未配置兜底 /tmp/numind-xhs/。
func xhsTempDir() string {
	if d := viper.GetString("xhs.temp_dir"); d != "" {
		return d
	}
	return "/tmp/numind-xhs/"
}

// ensureXhsTempDir 确保临时目录存在。
func ensureXhsTempDir() error {
	return os.MkdirAll(xhsTempDir(), 0o755)
}

// recordTranscribeSpanError 把视频转写失败记到 Langfuse span（优雅降级：langfuse 禁用时 no-op）。
func recordTranscribeSpanError(ctx context.Context, userID uint, noteID uint64, cause error) {
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "xhs-video-transcribe-error",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanOutput(map[string]string{"error": cause.Error()}),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}
}
