package meeting

// 实时流式 ASR 编排（meeting-copilot，SPEC §1/§2/§4/§5）。
//
// 职责：在「我方 ws 端点（BE-2 controller）↔ 阿里 Paraformer-realtime ws（asr_stream_client.go）」
// 之间做编排——
//   - 从 registry `ali-dashscope` 取 key 开一条 dashscope 流；
//   - 中间结果（interim）经回调透传给上层（上层转 §2 的 {"type":"interim"}）；
//   - 句末（final）落 meeting_segment（seq=max+1, text, start_ms=beginMs,
//     duration_seconds=(endMs-beginMs)/1000, audio_url 空），并把已落库的 SegmentDTO 经回调
//     上抛（上层转 §2 的 {"type":"final","segment":...}）；
//   - 累计上行 PCM 字节 → 音频秒数（bytes/(16000*2)），结束时记一条 UsageRecord
//     （service_type=asr, model=paraformer-realtime-v2, userID=0 内部不扣费）+ Langfuse。
//
// 计费纪律（SPEC §4）：流式 ASR 不经 aiservice.ASR 统一入口（那是批量路径），故此处**直接**
// 写 UsageRecord（user_id=0）并自建 Langfuse trace/generation，复刻 internalCallCtx 的「仅记
// 录用量、不扣费、不会员门」语义——不调用 credits Reserve/Reconcile。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"gorm.io/gorm"
)

// asrBytesPerSecond 是 16kHz / 16bit / 单声道 PCM 每秒字节数（SPEC §1：16000*2）。
const asrBytesPerSecond = 16000 * 2

// rollingSummaryRuneThreshold 触发滚动摘要折叠的累计新增字数阈值（FEEDBACK_V2_SPEC §2.2：~1500 字）。
const rollingSummaryRuneThreshold = 1500

// rollingSummaryTimeThreshold 触发滚动摘要折叠的时间阈值（FEEDBACK_V2_SPEC §2.2：~90 秒）。
// 仅当自上次折叠后有过新增内容时才按时间触发（避免静默会话空跑）。
const rollingSummaryTimeThreshold = 90 * time.Second

// aliDashscopeProviderName 是 registry `llm_provider` 中阿里供应商的 name（SPEC §1：复用同账号 key）。
const aliDashscopeProviderName = "ali-dashscope"

// RealtimeASRHandlers 是编排层回给上层（ws controller）的事件回调（SPEC §2 后端→前端）。
type RealtimeASRHandlers struct {
	// OnReady：阿里就绪、可开始送音频（上层转 {"type":"ready"}）。
	OnReady func()
	// OnInterim：当前句中间文本，覆盖式（上层转 {"type":"interim","text":...}）。
	OnInterim func(text string)
	// OnFinal：句末定稿，已落 meeting_segment（上层转 {"type":"final","segment":...}）。
	OnFinal func(seg SegmentDTO)
	// OnError：不可恢复错误（上层转 {"type":"error","message":...} 并关连接）。
	OnError func(err error)
	// OnClosed：阿里 task-finished 正常收尾（上层转 {"type":"closed"}）。
	OnClosed func()
}

// IRealtimeASR 是编排层暴露给 ws controller（BE-2）的单连接会话句柄。
//
// 典型用法：StartRealtimeASR 拿到句柄 → 前端 PCM 帧调 SendAudio → 前端 finish 调 Finish →
// 连接关闭（任意原因）调 Close 兜底释放。所有方法并发安全。
type IRealtimeASR interface {
	// SendAudio 转发一帧前端 PCM 到阿里，并累计音频时长。
	SendAudio(pcm []byte) error
	// Finish 通知阿里输入结束（前端 {"action":"finish"}）；等其吐完末句后触发 OnClosed。
	Finish()
	// Close 强制收尾并记 UsageRecord（连接断开/出错时由 controller 兜底调用）。幂等。
	Close()
}

// realtimeASR 是 IRealtimeASR 的实现。
type realtimeASR struct {
	b         *meetingBiz
	userID    uint
	sessionID uint64
	handlers  RealtimeASRHandlers

	stream *asrStream

	// nextSeq 是下一条 final segment 的 seq；落库前自增（单 reader goroutine 串行调用，
	// 但 Close 可能并发触发 usage 记录，故用 mu 保护）。
	mu        sync.Mutex
	nextSeq   int
	audioByts int64
	usageDone bool
	startedAt time.Time

	// --- 滚动摘要节流状态（FEEDBACK_V2_SPEC §2.2，受 mu 保护）---
	// summaryRunesPending 自上次折叠以来累计的新增字数（节流计数器，达阈值触发）。
	summaryRunesPending int
	// summaryLastFoldAt 上次触发折叠的时间（90s 时间闸）。零值表示从未折叠。
	summaryLastFoldAt time.Time
	// summaryInFlight 防并发重入：一次只允许一个后台折叠 goroutine（in-memory per session）。
	summaryInFlight bool
	// summaryFoldedSeq 折叠游标：已折进 running_summary 的最后一段 seq（in-memory per session，
	// 重连重置可接受——折叠幂等性由「摘要+新增」prompt 容忍，FEEDBACK_V2_SPEC §2.2）。
	summaryFoldedSeq int

	// langfuse 观测句柄（优雅降级：tc 为 nil 时全程跳过）。
	traceID string
	genID   string
}

var _ IRealtimeASR = (*realtimeASR)(nil)

// StartRealtimeASR 开一条实时 ASR 编排会话（供 ws controller 调用）。
//
// 前置：会话必须存在、归属 userID、status=active（与 IngestSegment 一致）。归属/状态校验失败
// 直接返回 error（controller 据此拒绝升级 / 回 error 帧）。
//
// 成功返回后，dashscope 流已连上并发了 run-task；task-started 到达时经 handlers.OnReady 通知。
func (b *meetingBiz) StartRealtimeASR(ctx context.Context, userID uint, sessionID uint64, handlers RealtimeASRHandlers) (IRealtimeASR, error) {
	s, err := b.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if s.Status != model.MeetingSessionStatusActive {
		return nil, fmt.Errorf("StartRealtimeASR: session %d not active", sessionID)
	}

	apiKey, err := b.aliDashscopeAPIKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("StartRealtimeASR: resolve ali-dashscope key: %w", err)
	}

	maxSeq, err := b.ds.Meetings().GetMaxSegmentSeq(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("StartRealtimeASR: max seq: %w", err)
	}

	now := time.Now()
	r := &realtimeASR{
		b:         b,
		userID:    userID,
		sessionID: sessionID,
		handlers:  handlers,
		nextSeq:   maxSeq, // 落库时先 +1
		startedAt: now,
		// 折叠游标从 maxSeq 起：本连接只折叠新转写，已有段（若有）不重折——重连续接时它们
		// 多已在上次连接折进 running_summary（或可接受地略过，prompt 容忍）。
		summaryFoldedSeq: maxSeq,
		// summaryLastFoldAt 初始化为会议开始时间（非零）：90s 时间闸从开始就计时，首轮也能
		// 按时间触发。否则稀疏内容（5 分钟攒不到 1500 字）会永远等不到字数阈值、永不折叠。
		summaryLastFoldAt: now,
	}

	// Langfuse trace+generation（SPEC §4，优雅降级）。internalCallCtx 不注入 trace，故此处显式
	// 建。userID 仅做可观测归属，与 billing user_id=0 隔离。
	r.traceID = langfuse.TraceID()
	langfuse.CreateTrace(r.traceID, "meeting-realtime-asr",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("meeting", "realtime-asr"),
		langfuse.WithTraceInput(map[string]interface{}{"session_id": sessionID}),
	)
	if tc := langfuse.FromContext(langfuse.WithTrace(ctx, r.traceID)); tc != nil {
		r.genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, r.genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("meeting-realtime-asr"),
			langfuse.WithGenModel(asrModel),
			langfuse.WithGenInput(map[string]interface{}{"session_id": sessionID}),
		)
	}

	// 注意：dashscope 回调在其 reader goroutine 触发；落库用 context.Background() 派生，
	// 因为 controller 的请求 ctx 可能随 ws 升级生命周期变化——转写持久化不应被其取消打断。
	stream, err := startASRStream(ctx, asrStreamOptions{
		APIKey:    apiKey,
		OnReady:   r.handleReady,
		OnInterim: r.handleInterim,
		OnFinal:   r.handleFinal,
		OnError:   r.handleError,
		OnClosed:  r.handleClosed,
	})
	if err != nil {
		// 连接失败：记一条 0 时长 usage（含 error）以留痕，再返回。
		r.recordUsage(err)
		return nil, fmt.Errorf("StartRealtimeASR: %w", err)
	}
	r.stream = stream
	return r, nil
}

// uploadBytesToCOS 是录音上传的 seam（测试可覆盖以注入"上传期间会话被结束"的竞态）。
var uploadBytesToCOS = util.UploadBytesToCOS

// UpdateRecordingURL 把整场录音上传到 COS（key `meeting-recordings/<userID>/<sessionID>/full.webm`）
// 并回写 meeting_session.recording_url（SPEC §3）。
//
// 归属校验复用 getOwnedSession（不存在→404，越权→403）。会话状态不限（结束后才上传录音是常态）。
// COS 未启用时 UploadBytesToCOS 返回空 URL，此处仍回写（空字符串），由前端按 recording_url 是否
// 为空决定是否展示回放——不当作错误。
func (b *meetingBiz) UpdateRecordingURL(ctx context.Context, userID uint, sessionID uint64, audio []byte, contentType string) (string, error) {
	if _, err := b.getOwnedSession(ctx, userID, sessionID); err != nil {
		return "", err
	}

	objectKey := fmt.Sprintf("meeting-recordings/%d/%d/full.webm", userID, sessionID)
	url, err := uploadBytesToCOS(ctx, objectKey, contentType, audio)
	if err != nil {
		return "", fmt.Errorf("UpdateRecordingURL: upload cos: %w", err)
	}

	// 定向只更新 recording_url 列（不能全行 Save：录音上传常在 EndSession 之后才完成，
	// 全行 Save 会用加载时的陈旧 active 态覆盖 status/ended_at/summary_status，把会话"反结束"）。
	if err := b.ds.Meetings().UpdateRecordingURL(ctx, sessionID, url); err != nil {
		return "", fmt.Errorf("UpdateRecordingURL: persist recording_url: %w", err)
	}
	return url, nil
}

// aliDashscopeAPIKey 从 registry `llm_provider`（name='ali-dashscope'）读 api_key。
//
// 该表的 LLMProvider().List/ListActive 受 provider_type='llm' 过滤，对 ASR 复用同账号 key
// 的 by-name 查询不适用，故直接走 ds.DB() 按 name 取行（key 字段 json:"-"，仅服务端可见）。
// 禁硬编码（CLAUDE.md §7 / SPEC §1）。
func (b *meetingBiz) aliDashscopeAPIKey(ctx context.Context) (string, error) {
	var p model.LLMProvider
	err := b.ds.DB().WithContext(ctx).
		Where("name = ?", aliDashscopeProviderName).
		First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("provider %q not found in llm_provider", aliDashscopeProviderName)
		}
		return "", err
	}
	if p.APIKey == "" {
		return "", fmt.Errorf("provider %q has empty api_key", aliDashscopeProviderName)
	}
	return p.APIKey, nil
}

// ---------------------------------------------------------------------------
// dashscope 回调处理
// ---------------------------------------------------------------------------

func (r *realtimeASR) handleReady() {
	if r.handlers.OnReady != nil {
		r.handlers.OnReady()
	}
}

func (r *realtimeASR) handleInterim(text string) {
	if r.handlers.OnInterim != nil {
		r.handlers.OnInterim(text)
	}
}

// handleFinal 落 meeting_segment 并把 DTO 上抛（SPEC §2 final）。
//
// 持久化用独立 background ctx：ws 连接关闭不应丢掉已转写的句子。空文本句也落库（保留时间轴，
// 与分段路径 §3 一致）。
func (r *realtimeASR) handleFinal(text string, beginMs, endMs int64) {
	r.mu.Lock()
	r.nextSeq++
	seq := r.nextSeq
	r.mu.Unlock()

	durationSeconds := float64(0)
	if endMs > beginMs {
		durationSeconds = float64(endMs-beginMs) / 1000.0
	}

	seg := &model.MeetingSegment{
		SessionID:       r.sessionID,
		Seq:             seq,
		Text:            text,
		StartMs:         int(beginMs),
		DurationSeconds: durationSeconds,
		AudioURL:        "", // 流式不逐段存音频（整场录音走 /recording，SPEC §3）
	}

	// 持久化用独立 background ctx：ws 连接关闭不应丢掉已转写的句子。故用全局 logger 记录
	// （log.C(ctx) 在 background ctx 下丢失 trace 关联，徒留误导）——与 handleError 风格一致。
	ctx := context.Background()
	if err := r.b.ds.Meetings().CreateSegment(ctx, seg); err != nil {
		// 单句落库失败**不**调 OnError（那会杀掉整条用户可见流）。仅服务端 log 记录并跳过该句，
		// 流继续转写后续句子。OnError 只保留给真正不可恢复错误（dashscope task-failed / 连接断）。
		log.Errorw("meeting realtime: persist final segment failed, skipping sentence",
			"session_id", r.sessionID, "seq", seq, "error", err)
		return
	}

	if r.handlers.OnFinal != nil {
		r.handlers.OnFinal(toSegmentDTO(seg))
	}

	// 节流触发滚动摘要折叠（FEEDBACK_V2_SPEC §2.2）：纯后台，绝不阻塞中继/录音。
	r.maybeUpdateRollingSummary(text)
}

// ---------------------------------------------------------------------------
// 滚动摘要节流折叠（FEEDBACK_V2_SPEC §2.2/§2.4）
// ---------------------------------------------------------------------------

// maybeUpdateRollingSummary 在每条 final 段落库后调用：累计新增字数，达字数(~1500)或时间(~90s)
// 阈值且当前无折叠在途时，spawn 一个后台 goroutine 把新增转写折进 running_summary。
//
// 绝不阻塞调用方（reader goroutine）：阈值判断/计数在锁内 O(1) 完成，真正的 LLM 调用在独立
// goroutine + context.Background() 里跑。每会话 summaryInFlight 防并发重入；失败仅 log。
func (r *realtimeASR) maybeUpdateRollingSummary(latestText string) {
	runes := len([]rune(strings.TrimSpace(latestText)))

	r.mu.Lock()
	r.summaryRunesPending += runes
	now := time.Now()

	// 时间闸：仅在有新增内容（pending>0）时才按时间触发，避免静默会话空跑。summaryLastFoldAt 在
	// StartRealtimeASR 已初始化为会议开始时间（非零），故首轮也能按 90s 时间触发——稀疏会议（攒不到
	// 1500 字）不会永远等不到字数阈值。!IsZero() 守卫保留兜底（防御零值构造的实例）。
	timeReady := r.summaryRunesPending > 0 &&
		!r.summaryLastFoldAt.IsZero() &&
		now.Sub(r.summaryLastFoldAt) >= rollingSummaryTimeThreshold
	runeReady := r.summaryRunesPending >= rollingSummaryRuneThreshold

	// 字数闸或时间闸任一达标即触发；in-flight 时一律不重入。
	if r.summaryInFlight || (!runeReady && !timeReady) {
		r.mu.Unlock()
		return
	}

	// 抢占折叠窗口：清零计数、记时间、置 in-flight，capture 折叠区间上界（当前 max seq）。
	r.summaryInFlight = true
	r.summaryRunesPending = 0
	r.summaryLastFoldAt = now
	fromSeqExclusive := r.summaryFoldedSeq
	toSeqInclusive := r.nextSeq
	r.mu.Unlock()

	go r.runRollingSummaryFold(fromSeqExclusive, toSeqInclusive)
}

// runRollingSummaryFold 后台折叠一次滚动摘要：读 (fromSeqExclusive, toSeqInclusive] 区间的新增
// 转写 + 已有 running_summary → updateRunningSummary → 回写 meeting_session.running_summary。
//
// 独立 context.Background()（ws 关闭不该打断），recover() 防 panic，失败仅 log（绝不影响
// 转写/反馈）。结束时释放 summaryInFlight 并推进折叠游标。
func (r *realtimeASR) runRollingSummaryFold(fromSeqExclusive, toSeqInclusive int) {
	defer func() {
		// recover() 必须在 defer 闭包**最先**调用才能捕获 body 的 panic（Go 语义）；放在
		// mutex 复位之后会让 panic 逃逸、goroutine 崩进程。故先 recover、再复位 in-flight 标记。
		if rec := recover(); rec != nil {
			log.Errorw("meeting realtime: rolling summary fold panicked",
				"session_id", r.sessionID, "panic", rec)
		}
		// 无论成功失败/是否 panic 都释放在途标记，下一拍可再触发。
		r.mu.Lock()
		r.summaryInFlight = false
		r.mu.Unlock()
	}()

	ctx := context.Background()

	// 取当前会话（拿已有 running_summary）。
	s, err := r.b.ds.Meetings().GetSession(ctx, r.sessionID)
	if err != nil {
		log.Warnw("meeting realtime: rolling summary fold load session failed",
			"session_id", r.sessionID, "error", err)
		return
	}

	// 取新增转写区间 (fromSeqExclusive, toSeqInclusive]。复用全量 ListSegments + 内存过滤
	// （会话内分段量级有限；store 无 by-seq-range 方法，避免为此扩接口）。
	segs, err := r.b.ds.Meetings().ListSegmentsBySession(ctx, r.sessionID)
	if err != nil {
		log.Warnw("meeting realtime: rolling summary fold list segments failed",
			"session_id", r.sessionID, "error", err)
		return
	}
	delta := joinSegmentRange(segs, fromSeqExclusive, toSeqInclusive)
	if strings.TrimSpace(delta) == "" {
		return // 该区间无实质内容（全静音），不调 LLM。
	}

	updated, err := r.b.updateRunningSummary(ctx, r.userID, r.sessionID, s.RunningSummary, delta)
	if err != nil {
		log.Warnw("meeting realtime: rolling summary fold LLM failed",
			"session_id", r.sessionID, "error", err)
		return
	}
	if strings.TrimSpace(updated) == "" || updated == s.RunningSummary {
		// 内容未变化（理论上 updateRunningSummary 已处理空增量），推进游标即可。
		r.advanceFoldCursor(toSeqInclusive)
		return
	}

	// 定向只更 running_summary 一列（不用全行 Save）：与 finalizeSummaryAsync 并发跑时，避免把
	// 它刚写的 summary/summary_status 用本 goroutine 读到的旧值覆盖（丢更新竞争）。
	if err := r.b.ds.Meetings().UpdateRunningSummary(ctx, r.sessionID, updated); err != nil {
		log.Warnw("meeting realtime: rolling summary persist failed",
			"session_id", r.sessionID, "error", err)
		return
	}
	r.advanceFoldCursor(toSeqInclusive)
}

// advanceFoldCursor 把折叠游标前移到 seq（单调不回退；并发安全）。
func (r *realtimeASR) advanceFoldCursor(seq int) {
	r.mu.Lock()
	if seq > r.summaryFoldedSeq {
		r.summaryFoldedSeq = seq
	}
	r.mu.Unlock()
}

// joinSegmentRange 拼接 (fromSeqExclusive, toSeqInclusive] 区间内非空分段文本，按 seq 时间顺序。
func joinSegmentRange(segs []model.MeetingSegment, fromSeqExclusive, toSeqInclusive int) string {
	var parts []string
	for i := range segs {
		seq := segs[i].Seq
		if seq <= fromSeqExclusive || seq > toSeqInclusive {
			continue
		}
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n")
}

func (r *realtimeASR) handleError(err error) {
	log.Errorw("meeting realtime: dashscope stream error", "session_id", r.sessionID, "error", err)
	r.recordUsage(err)
	if r.handlers.OnError != nil {
		r.handlers.OnError(err)
	}
}

func (r *realtimeASR) handleClosed() {
	r.recordUsage(nil)
	if r.handlers.OnClosed != nil {
		r.handlers.OnClosed()
	}
}

// ---------------------------------------------------------------------------
// IRealtimeASR 实现
// ---------------------------------------------------------------------------

// SendAudio 转发 PCM 并累计音频字节（SPEC §1.2 / §4）。
func (r *realtimeASR) SendAudio(pcm []byte) error {
	if r.stream == nil {
		return fmt.Errorf("realtime asr: stream not started")
	}
	r.mu.Lock()
	r.audioByts += int64(len(pcm))
	r.mu.Unlock()
	return r.stream.SendPCM(pcm)
}

// Finish 转 dashscope finish-task（SPEC §1.4）。usage 在 task-finished（handleClosed）记。
func (r *realtimeASR) Finish() {
	if r.stream != nil {
		r.stream.Finish()
	}
}

// Close 兜底收尾：关 dashscope 流并记 usage（若尚未记）。幂等。controller 在连接断开/异常时调用。
func (r *realtimeASR) Close() {
	if r.stream != nil {
		r.stream.close()
	}
	r.recordUsage(nil)
}

// ---------------------------------------------------------------------------
// 用量 / 观测
// ---------------------------------------------------------------------------

// recordUsage 按累计音频秒数写一条 UsageRecord（service_type=asr, user_id=0 内部不扣费）并收尾
// Langfuse generation。幂等（usageDone 守卫）：handleClosed / handleError / Close 都可能触发，
// 只记一次。callErr 非空时附带到 Langfuse 与日志。
func (r *realtimeASR) recordUsage(callErr error) {
	r.mu.Lock()
	if r.usageDone {
		r.mu.Unlock()
		return
	}
	r.usageDone = true
	totalBytes := r.audioByts
	// 持锁 capture finalSeq：nextSeq 由 reader goroutine 的 handleFinal 并发自增，禁止锁外读。
	finalSeq := r.nextSeq
	r.mu.Unlock()

	audioSeconds := float64(totalBytes) / float64(asrBytesPerSecond)

	ctx := context.Background()
	provider := aliDashscopeProviderName
	durationPtr := audioSeconds
	rec := &model.UsageRecord{
		UserID:          0, // 内部试用：不扣费、用量记到 user_id=0（与 internalCallCtx 一致）
		ServiceType:     "asr",
		Provider:        provider,
		Model:           asrModel,
		Operation:       "meeting.realtime_asr",
		BizRefType:      "meeting_session",
		BizRefID:        uint(r.sessionID),
		DurationSeconds: &durationPtr,
	}
	if err := r.b.ds.Billing().CreateUsageRecord(ctx, rec); err != nil {
		// 同 handleFinal：落库走 background ctx，log.C(ctx) 无 trace 关联 → 用全局 logger。
		log.Warnw("meeting realtime: record asr usage failed",
			"session_id", r.sessionID, "audio_seconds", audioSeconds, "error", err)
	}

	// Langfuse generation 收尾（优雅降级：genID 为空时 EndGeneration no-op 也安全，
	// 但显式守卫更清晰）。
	if r.genID != "" {
		opts := []langfuse.GenOption{
			langfuse.WithGenOutput(map[string]interface{}{
				"audio_seconds": audioSeconds,
				"final_seq":     finalSeq,
			}),
		}
		if callErr != nil {
			opts = append(opts, langfuse.WithGenError(callErr.Error()))
		}
		langfuse.EndGeneration(r.traceID, r.genID, opts...)
	}
}
