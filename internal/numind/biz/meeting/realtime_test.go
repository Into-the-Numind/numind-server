// Package meeting — realtime 流式 ASR 编排单测（meeting-copilot，SPEC §1/§4）。
//
// 覆盖（be-quality review 明列 + testing.md / NDF Rule 6 要求）：
//   - handleFinal 的 seq 自增（连续 final → seq 1,2,3…，且落库）。
//   - 从 beginMs/endMs 算 duration_seconds 正确（end<=begin → 0）。
//   - recordUsage 幂等：并发触发只写一条 UsageRecord（usageDone 守卫）。
//   - handleResult（asr_stream_client.go）sentence_end 解析：interim vs final 分发。
//
// store 用 in-memory SQLite（与 meeting_test.go 同基建），不触外部服务；handleResult 是纯逻辑，
// 直接在零值 *asrStream 上调（不碰 conn）。
package meeting

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// newRealtimeTestBiz 起一个 in-memory store 支撑的 *meetingBiz（含 UsageRecord 表，供 recordUsage）。
func newRealtimeTestBiz(t *testing.T) (*meetingBiz, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.MeetingSession{},
		&model.MeetingSegment{},
		&model.UsageRecord{},
	), "AutoMigrate meeting + usage tables")

	return &meetingBiz{ds: store.NewTestStore(db)}, db
}

// ---------------------------------------------------------------------------
// (a) handleFinal seq 自增 + 落库
// ---------------------------------------------------------------------------

func TestHandleFinal_SeqAutoIncrementsAndPersists(t *testing.T) {
	b, _ := newRealtimeTestBiz(t)
	var got []SegmentDTO
	r := &realtimeASR{
		b:         b,
		sessionID: 42,
		nextSeq:   0, // 起始（StartRealtimeASR 用 maxSeq；这里从 0 起）
		handlers: RealtimeASRHandlers{
			OnFinal: func(seg SegmentDTO) { got = append(got, seg) },
		},
	}

	r.handleFinal("第一句", 0, 1000)
	r.handleFinal("第二句", 1000, 2500)
	r.handleFinal("第三句", 2500, 3000)

	// OnFinal 上抛的 seq 依次自增。
	require.Len(t, got, 3)
	assert.Equal(t, 1, got[0].Seq)
	assert.Equal(t, 2, got[1].Seq)
	assert.Equal(t, 3, got[2].Seq)
	assert.Equal(t, "第一句", got[0].Text)

	// 三条均已落库，seq 连续。
	segs, err := b.ds.Meetings().ListSegmentsBySession(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, segs, 3)
	assert.Equal(t, []int{1, 2, 3}, []int{segs[0].Seq, segs[1].Seq, segs[2].Seq})
}

func TestHandleFinal_RespectsStartingSeq(t *testing.T) {
	b, _ := newRealtimeTestBiz(t)
	var got []SegmentDTO
	r := &realtimeASR{
		b:         b,
		sessionID: 7,
		nextSeq:   5, // 已有 5 段（StartRealtimeASR 注入 maxSeq）
		handlers:  RealtimeASRHandlers{OnFinal: func(seg SegmentDTO) { got = append(got, seg) }},
	}
	r.handleFinal("续上", 0, 500)
	require.Len(t, got, 1)
	assert.Equal(t, 6, got[0].Seq, "从 maxSeq+1 续接")
}

// ---------------------------------------------------------------------------
// (a2) 滚动摘要折叠（FEEDBACK_V2_SPEC §2.2/§2.4）
// ---------------------------------------------------------------------------

// TestJoinSegmentRange 验证折叠区间拼接 (fromExclusive, toInclusive]，跳过区间外/空文本。
func TestJoinSegmentRange(t *testing.T) {
	segs := []model.MeetingSegment{
		{Seq: 1, Text: "旧句已折"},
		{Seq: 2, Text: "新句A"},
		{Seq: 3, Text: ""}, // 静音跳过
		{Seq: 4, Text: "新句B"},
		{Seq: 5, Text: "尚未到达"},
	}
	// 折叠 (1, 4]：取 seq 2,3,4，跳过 1（已折）、5（未到）、3（空）。
	got := joinSegmentRange(segs, 1, 4)
	assert.Equal(t, "新句A\n新句B", got)

	// 空区间。
	assert.Equal(t, "", joinSegmentRange(segs, 4, 4))
}

// TestMaybeUpdateRollingSummary_RuneThresholdTriggersFold 验证累计字数达阈值时触发后台折叠，
// 折叠结果落到 running_summary，且折叠游标推进、计数清零。
func TestMaybeUpdateRollingSummary_RuneThresholdTriggersFold(t *testing.T) {
	b, db := newRealtimeTestBiz(t)
	// 建一个会话（折叠 goroutine 要 GetSession / UpdateSession）。
	s := &model.MeetingSession{UserID: 100, Title: "T", RolePrompt: "x", Status: model.MeetingSessionStatusActive}
	require.NoError(t, db.Create(s).Error)

	// fake 折叠 LLM：返回结构化摘要，并 capture 收到的增量（用 mu 保护跨 goroutine 访问，race-safe）。
	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	var capMu sync.Mutex
	var gotDelta string
	var foldCalls int
	done := make(chan struct{}, 4)
	summaryChatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		capMu.Lock()
		gotDelta = req.Messages[len(req.Messages)-1].Content.Text
		foldCalls++
		capMu.Unlock()
		out := &aiservice.ChatResponse{Content: "## 会议主题/目标\n- 已折叠"}
		done <- struct{}{}
		return out, nil
	}

	r := &realtimeASR{b: b, userID: 100, sessionID: s.ID, nextSeq: 0, summaryFoldedSeq: 0}

	// 落两段，第二段把累计字数推过阈值（1500）。先落一条短的，再落一条超长的。
	r.handleFinal("短句", 0, 500) // 几个字，不触发
	r.mu.Lock()
	pendingAfterShort := r.summaryRunesPending
	inflightAfterShort := r.summaryInFlight
	r.mu.Unlock()
	assert.Greater(t, pendingAfterShort, 0, "短句累计但未触发")
	assert.False(t, inflightAfterShort, "未达阈值不触发")

	// 超长段（>1500 字）→ 触发折叠。
	bigText := strings.Repeat("话", rollingSummaryRuneThreshold+10)
	r.handleFinal(bigText, 500, 2000)

	// 等后台折叠 goroutine 跑完（最多 ~2s）。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("折叠 goroutine 未触发")
	}
	// 给 goroutine 写库的时间（done 在 LLM 返回时发，但持久化在其后）。
	require.Eventually(t, func() bool {
		var reloaded model.MeetingSession
		if err := db.First(&reloaded, s.ID).Error; err != nil {
			return false
		}
		return strings.Contains(reloaded.RunningSummary, "已折叠")
	}, 2*time.Second, 20*time.Millisecond, "running_summary 应被回写")

	capMu.Lock()
	assert.Equal(t, 1, foldCalls, "触发一次折叠")
	assert.Contains(t, gotDelta, "短句", "增量含第一段")
	assert.Contains(t, gotDelta, "话", "增量含超长段")
	capMu.Unlock()

	// 折叠后：计数清零、游标推进到 seq 2。
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return !r.summaryInFlight && r.summaryFoldedSeq == 2 && r.summaryRunesPending == 0
	}, 2*time.Second, 20*time.Millisecond, "折叠后游标推进、计数清零、in-flight 释放")
}

// TestMaybeUpdateRollingSummary_NoReentryWhileInFlight 验证一次只允许一个折叠在途（防并发重入）。
func TestMaybeUpdateRollingSummary_NoReentryWhileInFlight(t *testing.T) {
	b, db := newRealtimeTestBiz(t)
	s := &model.MeetingSession{UserID: 100, Title: "T", RolePrompt: "x", Status: model.MeetingSessionStatusActive}
	require.NoError(t, db.Create(s).Error)

	r := &realtimeASR{b: b, userID: 100, sessionID: s.ID, nextSeq: 0, summaryFoldedSeq: 0}

	// 手动置 in-flight：模拟已有折叠在跑。
	r.mu.Lock()
	r.summaryInFlight = true
	r.mu.Unlock()

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	var calls int
	summaryChatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		calls++
		return &aiservice.ChatResponse{Content: "x"}, nil
	}

	// 即便超长内容达阈值，因 in-flight 也不再 spawn。
	r.handleFinal(strings.Repeat("字", rollingSummaryRuneThreshold+100), 0, 1000)

	time.Sleep(100 * time.Millisecond) // 给（不该发生的）goroutine 机会
	assert.Equal(t, 0, calls, "in-flight 时不重入折叠")
	// pending 仍累计（下次 in-flight 释放后可触发）。
	r.mu.Lock()
	assert.Greater(t, r.summaryRunesPending, 0)
	r.mu.Unlock()
}

// TestMaybeUpdateRollingSummary_TimeThresholdTriggersFold 验证「字数未达阈值但时间到点」也触发折叠
// （FEEDBACK_V2_SPEC §2.2 90s 时间闸）：稀疏会议（5 分钟攒不到 1500 字）也能按时间折叠，不会永远卡在
// 字数阈值上。构造 summaryLastFoldAt 在 91s 前、pending>0 但 <1500，调 maybe... 后断言触发后台折叠。
func TestMaybeUpdateRollingSummary_TimeThresholdTriggersFold(t *testing.T) {
	b, db := newRealtimeTestBiz(t)
	s := &model.MeetingSession{UserID: 100, Title: "T", RolePrompt: "x", Status: model.MeetingSessionStatusActive}
	require.NoError(t, db.Create(s).Error)

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	var capMu sync.Mutex
	var foldCalls int
	done := make(chan struct{}, 4)
	summaryChatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		capMu.Lock()
		foldCalls++
		capMu.Unlock()
		done <- struct{}{}
		return &aiservice.ChatResponse{Content: "## 会议主题/目标\n- 按时间折叠"}, nil
	}

	// 先落一条短句（pending>0 但远小于 1500 字阈值），并把 summaryLastFoldAt 拨到 91s 前
	// （模拟会议进行了 91 秒但内容稀疏），下一条短句应仅凭时间闸触发折叠。
	r := &realtimeASR{
		b: b, userID: 100, sessionID: s.ID, nextSeq: 0, summaryFoldedSeq: 0,
		summaryLastFoldAt: time.Now().Add(-91 * time.Second),
	}

	r.handleFinal("一句很短的话", 0, 1000) // 仅几个字，远不到 1500 字数闸

	// 时间闸（91s > 90s）应触发后台折叠。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("时间阈值未触发折叠 goroutine")
	}

	capMu.Lock()
	assert.Equal(t, 1, foldCalls, "时间到点触发一次折叠")
	capMu.Unlock()

	// 折叠期间字数远未达阈值，确认确是时间闸（而非字数闸）触发。
	assert.Less(t, len([]rune("一句很短的话")), rollingSummaryRuneThreshold, "前置：内容远小于字数阈值")

	// 折叠后：in-flight 释放、计数清零、游标推进、running_summary 落库。
	require.Eventually(t, func() bool {
		var reloaded model.MeetingSession
		if err := db.First(&reloaded, s.ID).Error; err != nil {
			return false
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return !r.summaryInFlight && r.summaryRunesPending == 0 &&
			r.summaryFoldedSeq == 1 && strings.Contains(reloaded.RunningSummary, "按时间折叠")
	}, 2*time.Second, 20*time.Millisecond, "时间触发折叠后状态收敛 + running_summary 落库")
}

// ---------------------------------------------------------------------------
// (b) duration_seconds 计算
// ---------------------------------------------------------------------------

func TestHandleFinal_DurationSeconds(t *testing.T) {
	cases := []struct {
		name     string
		beginMs  int64
		endMs    int64
		wantSecs float64
		wantStMs int
	}{
		{"1s", 0, 1000, 1.0, 0},
		{"1.5s with offset", 1000, 2500, 1.5, 1000},
		{"sub-second", 500, 750, 0.25, 500},
		{"end==begin → 0", 3000, 3000, 0, 3000},
		{"end<begin → 0 (clock skew)", 5000, 4000, 0, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, db := newRealtimeTestBiz(t)
			r := &realtimeASR{b: b, sessionID: 1, nextSeq: 0}
			r.handleFinal("x", tc.beginMs, tc.endMs)

			var seg model.MeetingSegment
			require.NoError(t, db.First(&seg, "session_id = ?", 1).Error)
			assert.InDelta(t, tc.wantSecs, seg.DurationSeconds, 0.0001, "duration_seconds")
			assert.Equal(t, tc.wantStMs, seg.StartMs, "start_ms = beginMs")
		})
	}
}

// ---------------------------------------------------------------------------
// (c) recordUsage 幂等（并发两次 → 只写一条）
// ---------------------------------------------------------------------------

func TestRecordUsage_IdempotentUnderConcurrency(t *testing.T) {
	b, db := newRealtimeTestBiz(t)
	r := &realtimeASR{
		b:         b,
		sessionID: 99,
		audioByts: int64(asrBytesPerSecond) * 3, // 3 秒音频
	}

	// handleClosed / handleError / Close 都可能并发触发 recordUsage——模拟竞争。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.recordUsage(nil)
		}()
	}
	wg.Wait()

	var recs []model.UsageRecord
	require.NoError(t, db.Where("biz_ref_id = ?", 99).Find(&recs).Error)
	require.Len(t, recs, 1, "并发触发只写一条 UsageRecord")

	rec := recs[0]
	assert.Equal(t, uint(0), rec.UserID, "内部不扣费 → user_id=0")
	assert.Equal(t, "asr", rec.ServiceType)
	assert.Equal(t, asrModel, rec.Model)
	assert.Equal(t, "meeting_session", rec.BizRefType)
	require.NotNil(t, rec.DurationSeconds)
	assert.InDelta(t, 3.0, *rec.DurationSeconds, 0.0001, "3 秒音频")
}

func TestRecordUsage_SecondCallNoOp(t *testing.T) {
	b, db := newRealtimeTestBiz(t)
	r := &realtimeASR{b: b, sessionID: 11, audioByts: int64(asrBytesPerSecond)}

	r.recordUsage(nil)
	r.recordUsage(nil) // 第二次应被 usageDone 守卫吞掉

	var count int64
	require.NoError(t, db.Model(&model.UsageRecord{}).Where("biz_ref_id = ?", 11).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// ---------------------------------------------------------------------------
// (d) handleResult：sentence_end 解析 → interim vs final 分发
// ---------------------------------------------------------------------------

func mkFrame(text string, beginMs, endMs int64, sentenceEnd bool) asrServerFrame {
	var f asrServerFrame
	f.Header.Event = asrEventResultGenerated
	f.Payload.Output.Sentence = &asrSentence{
		Text:        text,
		BeginTime:   &beginMs,
		EndTime:     &endMs,
		SentenceEnd: sentenceEnd,
	}
	return f
}

func TestHandleResult_InterimVsFinalDispatch(t *testing.T) {
	type capture struct {
		interimText  string
		interimCount int
		finalText    string
		finalBegin   int64
		finalEnd     int64
		finalCount   int
	}

	t.Run("sentence_end=false → OnInterim", func(t *testing.T) {
		var c capture
		opts := asrStreamOptions{
			OnInterim: func(text string) { c.interimText = text; c.interimCount++ },
			OnFinal:   func(string, int64, int64) { c.finalCount++ },
		}
		(&asrStream{}).handleResult(mkFrame("中间结果", 0, 800, false), opts)
		assert.Equal(t, 1, c.interimCount, "interim 触发一次")
		assert.Equal(t, "中间结果", c.interimText)
		assert.Equal(t, 0, c.finalCount, "未触发 final")
	})

	t.Run("sentence_end=true → OnFinal with begin/end", func(t *testing.T) {
		var c capture
		opts := asrStreamOptions{
			OnInterim: func(string) { c.interimCount++ },
			OnFinal: func(text string, beginMs, endMs int64) {
				c.finalText = text
				c.finalBegin = beginMs
				c.finalEnd = endMs
				c.finalCount++
			},
		}
		(&asrStream{}).handleResult(mkFrame("定稿句", 1000, 2000, true), opts)
		assert.Equal(t, 1, c.finalCount, "final 触发一次")
		assert.Equal(t, "定稿句", c.finalText)
		assert.Equal(t, int64(1000), c.finalBegin)
		assert.Equal(t, int64(2000), c.finalEnd)
		assert.Equal(t, 0, c.interimCount, "未触发 interim")
	})

	t.Run("nil sentence → no dispatch", func(t *testing.T) {
		var c capture
		opts := asrStreamOptions{
			OnInterim: func(string) { c.interimCount++ },
			OnFinal:   func(string, int64, int64) { c.finalCount++ },
		}
		var f asrServerFrame
		f.Header.Event = asrEventResultGenerated // Payload.Output.Sentence == nil
		(&asrStream{}).handleResult(f, opts)
		assert.Equal(t, 0, c.interimCount)
		assert.Equal(t, 0, c.finalCount)
	})

	t.Run("final with nil begin/end → defaults to 0", func(t *testing.T) {
		var c capture
		opts := asrStreamOptions{
			OnFinal: func(_ string, beginMs, endMs int64) {
				c.finalBegin = beginMs
				c.finalEnd = endMs
				c.finalCount++
			},
		}
		var f asrServerFrame
		f.Header.Event = asrEventResultGenerated
		f.Payload.Output.Sentence = &asrSentence{Text: "无时间戳", SentenceEnd: true} // begin/end nil
		(&asrStream{}).handleResult(f, opts)
		require.Equal(t, 1, c.finalCount)
		assert.Equal(t, int64(0), c.finalBegin)
		assert.Equal(t, int64(0), c.finalEnd)
	})
}

// ---------------------------------------------------------------------------
// (P1-1 回归) SendPCM 在 ready 之前缓冲、task-started 后 flush
// ---------------------------------------------------------------------------

func TestSendPCM_BuffersBeforeReadyThenFlushes(t *testing.T) {
	// 用一个带容量的 writeCh 的 asrStream（不连 conn）：SendPCM 走 enqueue 投递到 writeCh，
	// 我们直接从 writeCh 取出来断言「task-started 之前缓冲、之后按序 flush」。
	s := &asrStream{
		writeCh: make(chan asrWriteMsg, 16),
		done:    make(chan struct{}),
	}

	// task-started 之前：帧应被缓冲，不进 writeCh。
	require.NoError(t, s.SendPCM([]byte{1, 1}))
	require.NoError(t, s.SendPCM([]byte{2, 2}))
	assert.Len(t, s.writeCh, 0, "ready 前不转发，进缓冲")
	s.readyMu.Lock()
	assert.Len(t, s.pendingPCM, 2, "两帧已缓冲")
	s.readyMu.Unlock()

	// task-started：flush 缓冲 → 置 ready。
	var readyCalled bool
	s.handleTaskStarted(asrStreamOptions{OnReady: func() { readyCalled = true }})
	assert.True(t, readyCalled, "OnReady 触发")

	// flush 后两帧按序进 writeCh。
	require.Len(t, s.writeCh, 2, "缓冲帧已 flush")
	m1 := <-s.writeCh
	m2 := <-s.writeCh
	assert.Equal(t, []byte{1, 1}, m1.data)
	assert.Equal(t, []byte{2, 2}, m2.data)

	// ready 之后：帧直接转发（不再缓冲）。
	require.NoError(t, s.SendPCM([]byte{3, 3}))
	s.readyMu.Lock()
	assert.Len(t, s.pendingPCM, 0, "ready 后不再缓冲")
	s.readyMu.Unlock()
	require.Len(t, s.writeCh, 1)
	m3 := <-s.writeCh
	assert.Equal(t, []byte{3, 3}, m3.data)
}

// TestSendPCM_BufferCopiesFrame 验证缓冲时复制底层 buffer（调用方复用切片不污染缓冲帧）。
func TestSendPCM_BufferCopiesFrame(t *testing.T) {
	s := &asrStream{writeCh: make(chan asrWriteMsg, 4), done: make(chan struct{})}
	frame := []byte{9, 9}
	require.NoError(t, s.SendPCM(frame))
	frame[0] = 0 // 调用方复用 buffer，改写原切片
	s.readyMu.Lock()
	buffered := s.pendingPCM[0]
	s.readyMu.Unlock()
	assert.Equal(t, []byte{9, 9}, buffered, "缓冲帧是副本，不受调用方改写影响")
}
