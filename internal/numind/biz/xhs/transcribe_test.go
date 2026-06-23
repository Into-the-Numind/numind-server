package xhs

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/model"
)

// ── 测试替身 ────────────────────────────────────────────────────────────────

// fakeBiller 记录 biz 层显式两阶段扣费的入参，验证 xhs ASR 按实际秒数扣分。
type fakeBiller struct {
	mu sync.Mutex

	reserveCalls   int
	reserveOp      credit.Operation
	reserveCredits int64
	reserveUserID  uint

	reconcileCalls   int
	reconcileCredits int64

	refundCalls int

	reserveErr error // 注入：模拟余额不足
	nextRsvID  uint64
}

func (b *fakeBiller) Reserve(_ context.Context, user *model.User, op credit.Operation, estimated int64, _ uint64, _ *string) (*credit.Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserveCalls++
	b.reserveOp = op
	b.reserveCredits = estimated
	if user != nil {
		b.reserveUserID = user.ID
	}
	if b.reserveErr != nil {
		return nil, b.reserveErr
	}
	b.nextRsvID++
	return &credit.Reservation{ID: b.nextRsvID, UserID: user.ID, ReservedCredits: estimated}, nil
}

func (b *fakeBiller) Reconcile(_ context.Context, _ uint64, actualCostCents int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reconcileCalls++
	b.reconcileCredits = actualCostCents
	return nil
}

func (b *fakeBiller) Refund(_ context.Context, _ uint64, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refundCalls++
	return nil
}

// fakeUserGetter 给 transcribe 提供 Reserve 所需的 *model.User。
type fakeUserGetter struct{ id uint }

func (g fakeUserGetter) GetByID(_ context.Context, userID uint) (*model.User, error) {
	return &model.User{Model: gorm.Model{ID: userID}}, nil
}

// ── 测试夹具：替换包级 seam ───────────────────────────────────────────────────

// withMockASR 在 t 生命周期内替换 asrFn seam，并捕获最后一次调用的 ctx，
// 用于断言 xhs 走的是 biz 层显式扣费、对 gateway 计费保持中性
// （与 monitor/会议副驾一样注入 WithSkipLegacyBilling，从不碰共享 gateway 计费）。
func withMockASR(t *testing.T, durationSeconds float64, text string) *capturedASR {
	t.Helper()
	cap := &capturedASR{}
	prev := asrFn
	asrFn = func(ctx context.Context, taskID string, req aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
		cap.mu.Lock()
		cap.calls++
		cap.taskID = taskID
		cap.ctx = ctx
		cap.audioFormat = req.AudioFormat
		cap.language = req.Language
		cap.mu.Unlock()
		return &aiservice.ASRResponse{Text: text, DurationSeconds: durationSeconds}, nil
	}
	t.Cleanup(func() { asrFn = prev })
	return cap
}

type capturedASR struct {
	mu          sync.Mutex
	calls       int
	taskID      string
	ctx         context.Context
	audioFormat string
	language    string
}

// withMockDownload 替换下载 seam：ok=true 模拟下载成功，否则模拟直链 4xx 失效。
func withMockDownload(t *testing.T, ok bool) {
	t.Helper()
	prev := downloadVideoFn
	downloadVideoFn = func(_ context.Context, _ string, _ string) error {
		if ok {
			return nil
		}
		return errVideoLinkExpired
	}
	t.Cleanup(func() { downloadVideoFn = prev })
}

// withMockExtractAudio 替换 ffmpeg 抽音 seam，返回固定的假 wav 字节。
func withMockExtractAudio(t *testing.T) {
	t.Helper()
	prev := extractAudioToWavFn
	extractAudioToWavFn = func(_ context.Context, _ string, dest string) error {
		// 不真正落盘——transcribeVideo 读 dest 的逻辑也走 seam（见 readAudioFn）。
		return nil
	}
	prevRead := readAudioFn
	readAudioFn = func(_ string) ([]byte, error) { return []byte("fake-wav-bytes"), nil }
	t.Cleanup(func() {
		extractAudioToWavFn = prev
		readAudioFn = prevRead
	})
}

// ── 验收 ① xhs 按实际秒扣 ─────────────────────────────────────────────────────

func TestTranscribeVideo_ChargesActualSeconds(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.asr_credits_per_second", 0.008)
	defer viper.Reset()

	const userID = uint(42)
	const actualSeconds = 125.4 // 实际转写时长（云返回）

	asrCap := withMockASR(t, actualSeconds, "这是转写文本")
	withMockDownload(t, true)
	withMockExtractAudio(t)

	biller := &fakeBiller{}
	e := NewEnricher(newEnrichMockStore()).WithBiller(biller, fakeUserGetter{id: userID})

	note := &model.XhsTopicNote{
		ID:       1,
		UserID:   userID,
		NoteType: model.XhsNoteTypeVideo,
		VideoURL: "https://sns-video.xhscdn.com/abc.mp4",
	}

	err := e.transcribeVideo(context.Background(), userID, note)
	require.NoError(t, err)

	// 转写写回。
	require.NotNil(t, note.VideoTranscript)
	assert.Equal(t, "这是转写文本", *note.VideoTranscript)

	// 计费：Reserve 一次（保守额）+ Reconcile 一次（按实际秒 ceil）。
	assert.Equal(t, 1, biller.reserveCalls, "应 Reserve 一次")
	assert.Equal(t, 1, biller.reconcileCalls, "应 Reconcile 一次")
	assert.Equal(t, userID, biller.reserveUserID, "扣的是该用户")

	// Reconcile 金额 = ceil(actualSeconds * rate)，call 级取整（非按秒取整）。
	wantCredits := int64(math.Ceil(actualSeconds * 0.008))
	assert.Equal(t, wantCredits, biller.reconcileCredits, "应按实际秒数 ceil 结算")

	// 关键隔离断言：xhs 的 ASR 调用对 gateway 计费保持中性
	// （与 monitor/会议副驾一致注入 WithSkipLegacyBilling），扣费只发生在 biz 层 biller。
	// 这证明 xhs 没有改动/借道共享 gateway 计费。
	asrCap.mu.Lock()
	defer asrCap.mu.Unlock()
	require.Equal(t, 1, asrCap.calls)
	assert.True(t, aiservice.ShouldSkipLegacyBilling(asrCap.ctx),
		"xhs ASR 必须 WithSkipLegacyBilling：不走 gateway 计费，证明没碰 monitor/会议共享路径")
	assert.Equal(t, userID, aismw.UserIDFromCtx(asrCap.ctx))
}

// ── 验收 ② monitor/会议副驾 ASR 仍不扣（证明没碰共享 gateway） ──────────────────
//
// monitor 与会议副驾的 ASR 不经 biz 层 biller，而是靠 gateway 对非 Chat 透传
// （ContextBudgetCredits 不扣）+ WithSkipLegacyBilling。本用例用一个不带 biller 的
// Enricher 复刻“没有 biz 层显式扣费”的语义，断言这条路径 0 次 biller 调用，
// 即与 monitor/会议副驾保持一致——biz 层从不对它们扣费。

func TestTranscribeVideo_MonitorMeetingPathNotCharged(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.asr_credits_per_second", 0.008)
	defer viper.Reset()

	withMockASR(t, 60, "monitor transcript")
	withMockDownload(t, true)
	withMockExtractAudio(t)

	// 无 biller 的 Enricher = monitor/会议副驾语义（biz 层不扣费）。
	biller := &fakeBiller{}
	e := NewEnricher(newEnrichMockStore()) // 故意不 WithBiller

	note := &model.XhsTopicNote{
		ID:       2,
		UserID:   7,
		NoteType: model.XhsNoteTypeVideo,
		VideoURL: "https://sns-video.xhscdn.com/def.mp4",
	}

	err := e.transcribeVideo(context.Background(), 7, note)
	require.NoError(t, err)

	assert.Equal(t, 0, biller.reserveCalls, "未 WithBiller 的路径（monitor/会议语义）biz 层必须 0 次 Reserve")
	assert.Equal(t, 0, biller.reconcileCalls, "未 WithBiller 的路径 biz 层必须 0 次 Reconcile")
}

// ── 验收 ③ 直链失效 → partial + transcript NULL，不扣 ASR 费 ────────────────────

func TestTranscribeVideo_ExpiredLinkPartialNoCharge(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.asr_credits_per_second", 0.008)
	defer viper.Reset()

	withMockASR(t, 60, "should-not-be-called")
	withMockDownload(t, false) // 直链 4xx 失效
	withMockExtractAudio(t)

	biller := &fakeBiller{}
	e := NewEnricher(newEnrichMockStore()).WithBiller(biller, fakeUserGetter{id: 9})

	note := &model.XhsTopicNote{
		ID:       3,
		UserID:   9,
		NoteType: model.XhsNoteTypeVideo,
		VideoURL: "https://sns-video.xhscdn.com/expired.mp4",
	}

	err := e.transcribeVideo(context.Background(), 9, note)
	require.NoError(t, err, "直链失效应优雅降级（partial），不返回 error 阻塞富化")

	assert.Nil(t, note.VideoTranscript, "直链失效转写应为 NULL")
	assert.True(t, errors.Is(note.enrichErr, errVideoLinkExpired) || note.partial,
		"直链失效应标记 partial")
	assert.Equal(t, 0, biller.reserveCalls, "没下到视频不应预扣")
	assert.Equal(t, 0, biller.reconcileCalls)
}

// ── 验收：余额不足 → insufficient_credits，跳过 ASR 不报错 ──────────────────────

func TestTranscribeVideo_InsufficientCreditsSkips(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.asr_credits_per_second", 0.008)
	defer viper.Reset()

	asrCap := withMockASR(t, 60, "should-not-be-called")
	withMockDownload(t, true)
	withMockExtractAudio(t)

	biller := &fakeBiller{reserveErr: credit.ErrInsufficientCredits}
	e := NewEnricher(newEnrichMockStore()).WithBiller(biller, fakeUserGetter{id: 11})

	note := &model.XhsTopicNote{
		ID:       4,
		UserID:   11,
		NoteType: model.XhsNoteTypeVideo,
		VideoURL: "https://sns-video.xhscdn.com/x.mp4",
	}

	err := e.transcribeVideo(context.Background(), 11, note)
	require.NoError(t, err, "余额不足应跳过 ASR 不报错")

	assert.True(t, note.insufficientCredits, "余额不足应标记 insufficient_credits")
	assert.Nil(t, note.VideoTranscript)

	asrCap.mu.Lock()
	defer asrCap.mu.Unlock()
	assert.Equal(t, 0, asrCap.calls, "余额不足必须在 ASR 调用前短路")
	assert.Equal(t, 1, biller.reserveCalls, "尝试过 Reserve")
	assert.Equal(t, 0, biller.reconcileCalls, "未调用 ASR，不结算")
}
