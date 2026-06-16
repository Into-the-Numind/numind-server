// Package meeting — biz/meeting 单测（meeting-copilot feature）。
//
// 覆盖：
//   - 纯逻辑：evalSentinel（NO_FEEDBACK sentinel 解析）、buildTranscriptWindow / joinTranscript
//     （转写窗口拼接）、computeDurationSeconds、DTO 映射。
//   - 生命周期 + 预设：CreateSession / GetSession / EndSession / 预设 CRUD（in-memory SQLite store）。
//   - 反馈流：consumeFeedbackStream 的 auto-skip / auto-generate / manual（注入 fake chatStreamFn）。
//   - 转写 ingest：IngestSegment 空转写也落库（注入 fake asrFn）。
//   - 纪要：generateSummary 空转写降级（不调 LLM）。
//
// LLM/ASR 依赖通过包级 var seam（asrFn / chatStreamFn / summaryChatFn）注入 mock，不触外部服务。
package meeting

import (
	"context"
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
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// 测试基建
// ---------------------------------------------------------------------------

func newMeetingTestBiz(t *testing.T) (IMeetingBiz, *gorm.DB) {
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
		&model.MeetingFeedback{},
		&model.MeetingPreset{},
	), "AutoMigrate meeting tables")

	return NewMeetingBiz(store.NewTestStore(db)), db
}

// withFakeChatStream 临时替换 chatStreamFn，并在测试结束恢复。
// deltas 是模型流式吐出的文本片段；err 是 ChatStream 调用本身的错误（非 nil 时不产 chunk）。
func withFakeChatStream(t *testing.T, deltas []string, callErr error) {
	t.Helper()
	orig := chatStreamFn
	t.Cleanup(func() { chatStreamFn = orig })
	chatStreamFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		if callErr != nil {
			return nil, callErr
		}
		ch := make(chan aiservice.ChatChunk, len(deltas)+1)
		for i, d := range deltas {
			ch <- aiservice.ChatChunk{Delta: d, Index: i}
		}
		ch <- aiservice.ChatChunk{
			IsFinal:      true,
			FinishReason: "stop",
			Usage:        &aiservice.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		close(ch)
		return ch, nil
	}
}

// captureSSE 收集 SSEHandler 推送的事件用于断言。
type captureSSE struct {
	events []sseEvent
}
type sseEvent struct {
	typ  string
	data interface{}
}

func (c *captureSSE) handler(typ string, data interface{}) error {
	c.events = append(c.events, sseEvent{typ: typ, data: data})
	return nil
}
func (c *captureSSE) types() []string {
	out := make([]string, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.typ)
	}
	return out
}

// ---------------------------------------------------------------------------
// 纯逻辑：evalSentinel
// ---------------------------------------------------------------------------

func TestEvalSentinel(t *testing.T) {
	cases := []struct {
		name        string
		cur         string
		final       bool
		wantDecided bool
		wantSkip    bool
	}{
		{"exact sentinel", "NO_FEEDBACK", false, true, true},
		{"sentinel with trailing", "NO_FEEDBACK 此刻无需", false, true, true},
		{"leading whitespace then sentinel", "  \nNO_FEEDBACK", false, true, true},
		{"clearly feedback content", "你刚才的论证有逻辑漏洞", false, true, false},
		{"partial prefix not final - undecided", "NO_FE", false, false, false},
		{"partial prefix final - not skip", "NO_FE", true, true, false},
		{"prefix mismatch decided not skip", "No problem", false, true, false},
		{"empty not final undecided", "", false, false, false},
		{"empty final decided not skip", "", true, true, false},
		{"whitespace only not final", "   ", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decided, skip := evalSentinel(tc.cur, tc.final)
			assert.Equal(t, tc.wantDecided, decided, "decided")
			assert.Equal(t, tc.wantSkip, skip, "skip")
		})
	}
}

// ---------------------------------------------------------------------------
// 纯逻辑：buildTranscriptWindow
// ---------------------------------------------------------------------------

func TestBuildTranscriptWindow(t *testing.T) {
	t.Run("empty segments", func(t *testing.T) {
		tr, anchor := buildTranscriptWindow(nil, 2000)
		assert.Equal(t, "", tr)
		assert.Equal(t, 0, anchor)
	})

	t.Run("orders by time and uses last seq as anchor", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: "第一句"},
			{Seq: 2, Text: ""}, // 静音段跳过拼接，但参与 anchor
			{Seq: 3, Text: "第三句"},
		}
		tr, anchor := buildTranscriptWindow(segs, 2000)
		assert.Equal(t, "第一句\n第三句", tr)
		assert.Equal(t, 3, anchor, "anchor 取最后一段 seq（含静音段）")
	})

	t.Run("window keeps most recent within rune cap", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: strings.Repeat("旧", 30)},
			{Seq: 2, Text: strings.Repeat("新", 10)},
		}
		tr, anchor := buildTranscriptWindow(segs, 15)
		// 从尾往前取：先取"新"(10)，再取"旧"会超 15 且已有内容 → 停。
		assert.Equal(t, strings.Repeat("新", 10), tr)
		assert.Equal(t, 2, anchor)
	})
}

// ---------------------------------------------------------------------------
// 纯逻辑：joinTranscript（首尾截断）
// ---------------------------------------------------------------------------

func TestJoinTranscript(t *testing.T) {
	t.Run("under cap returns full", func(t *testing.T) {
		segs := []model.MeetingSegment{{Seq: 1, Text: "abc"}, {Seq: 2, Text: "def"}}
		assert.Equal(t, "abc\ndef", joinTranscript(segs, 100))
	})
	t.Run("skips empty segments", func(t *testing.T) {
		segs := []model.MeetingSegment{{Seq: 1, Text: "abc"}, {Seq: 2, Text: "  "}, {Seq: 3, Text: "def"}}
		assert.Equal(t, "abc\ndef", joinTranscript(segs, 100))
	})
	t.Run("over cap truncates head and tail", func(t *testing.T) {
		segs := []model.MeetingSegment{{Seq: 1, Text: strings.Repeat("x", 100)}}
		out := joinTranscript(segs, 20)
		assert.Contains(t, out, "中间部分已省略")
		assert.True(t, len([]rune(out)) < 100, "应被截断")
	})
}

// ---------------------------------------------------------------------------
// 纯逻辑：computeDurationSeconds
// ---------------------------------------------------------------------------

func TestComputeDurationSeconds(t *testing.T) {
	start := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, 0, computeDurationSeconds(nil, start.Add(time.Hour)), "nil start → 0")
	assert.Equal(t, 90, computeDurationSeconds(&start, start.Add(90*time.Second)))
	assert.Equal(t, 0, computeDurationSeconds(&start, start.Add(-time.Second)), "end < start → 0")
}

// ---------------------------------------------------------------------------
// 纯逻辑：DTO 映射
// ---------------------------------------------------------------------------

func TestToSessionDTO(t *testing.T) {
	pid := uint64(7)
	now := time.Now()
	s := &model.MeetingSession{
		ID: 1, Title: "T", RolePrompt: "RP", PresetID: &pid,
		Status: model.MeetingSessionStatusActive, AutoIntervalSeconds: 30,
		DurationSeconds: 120, Summary: "SUM", SummaryStatus: model.MeetingSummaryStatusDone,
		StartedAt: &now,
	}
	dto := toSessionDTO(s)
	assert.Equal(t, uint64(1), dto.ID)
	assert.Equal(t, "RP", dto.RolePrompt)
	require.NotNil(t, dto.PresetID)
	assert.Equal(t, uint64(7), *dto.PresetID)
	assert.Equal(t, 30, dto.AutoIntervalSeconds)
	assert.Equal(t, "SUM", dto.Summary)
	assert.Equal(t, model.MeetingSummaryStatusDone, dto.SummaryStatus)
}

func TestToFeedbackDTO(t *testing.T) {
	fb := &model.MeetingFeedback{ID: 9, Trigger: model.MeetingFeedbackTriggerManual, AnchorSeq: 4, Content: "C"}
	dto := toFeedbackDTO(fb)
	assert.Equal(t, uint64(9), dto.ID)
	assert.Equal(t, "manual", dto.Trigger)
	assert.Equal(t, 4, dto.AnchorSeq)
	assert.Equal(t, "C", dto.Content)
}

// ---------------------------------------------------------------------------
// 生命周期 + 归属校验
// ---------------------------------------------------------------------------

func TestCreateAndGetSession(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()

	interval := 45
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{
		RolePrompt:          "你是辩论陪练",
		AutoIntervalSeconds: &interval,
	})
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusActive, dto.Status)
	assert.Equal(t, 45, dto.AutoIntervalSeconds)
	assert.NotNil(t, dto.StartedAt)
	assert.Contains(t, dto.Title, "未命名会议", "无标题时生成默认名")

	detail, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, dto.ID, detail.Session.ID)
	assert.Empty(t, detail.Segments)
	assert.Empty(t, detail.Feedbacks)
}

func TestCreateSession_DefaultInterval(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	dto, err := biz.CreateSession(context.Background(), 1, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, defaultAutoIntervalSeconds, dto.AutoIntervalSeconds)
}

func TestGetSession_OwnershipEnforced(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// 别的用户访问 → 403。
	_, err = biz.GetSession(ctx, 999, dto.ID)
	require.Error(t, err)

	// 不存在的会话 → 404-类错误。
	_, err = biz.GetSession(ctx, 100, 88888)
	require.Error(t, err)
}

func TestEndSession_GeneratesSummaryAndDuration(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// 塞一段转写，让 summary 走 LLM 路径。
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 1, Text: "讨论了上线时间"}).Error)

	// 把 started_at 调到过去，duration 才非 0。
	past := time.Now().Add(-2 * time.Minute)
	require.NoError(t, db.Model(&model.MeetingSession{}).Where("id = ?", dto.ID).Update("started_at", past).Error)

	// fake summary LLM。
	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	summaryChatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "## 要点\n- 上线时间\n\n## 决议\n- （无）\n\n## 待办\n- （无）"}, nil
	}

	ended, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusEnded, ended.Status)
	assert.Equal(t, model.MeetingSummaryStatusDone, ended.SummaryStatus)
	assert.Contains(t, ended.Summary, "## 要点")
	assert.NotNil(t, ended.EndedAt)
	assert.Greater(t, ended.DurationSeconds, 0)

	// 幂等：再次 end 不报错，状态保持 ended。
	again, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusEnded, again.Status)
}

func TestEndSession_EmptyTranscriptDegradesSummary(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// 不塞任何转写 → generateSummary 走降级占位（不调 LLM），summary_status=done。
	ended, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusDone, ended.SummaryStatus)
	assert.Contains(t, ended.Summary, "没有可用的转写内容")
}

// ---------------------------------------------------------------------------
// 预设 CRUD
// ---------------------------------------------------------------------------

func TestPresetCRUD(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()

	// seed 一个系统内置预设。
	require.NoError(t, db.Create(&model.MeetingPreset{
		UserID: 0, Name: "辩论陪练", RolePrompt: "你是辩论陪练", AutoIntervalSeconds: 60, IsBuiltin: true,
	}).Error)

	// 用户存自己的预设。
	saved, err := biz.SavePreset(ctx, 100, &SavePresetReq{Name: "我的访谈", RolePrompt: "你是研究员"})
	require.NoError(t, err)
	assert.False(t, saved.IsBuiltin)
	assert.Equal(t, uint(100), saved.UserID)

	// 列表含内置 + 本人，内置在前。
	list, err := biz.ListPresets(ctx, 100)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.True(t, list[0].IsBuiltin, "内置预设排在前")

	// 删除本人预设成功。
	require.NoError(t, biz.DeletePreset(ctx, 100, saved.ID))

	// 删内置预设失败（store 守卫 is_builtin=0）。
	builtinList, _ := biz.ListPresets(ctx, 200) // 另一用户也能看到内置
	var builtinID uint64
	for _, p := range builtinList {
		if p.IsBuiltin {
			builtinID = p.ID
		}
	}
	require.NotZero(t, builtinID)
	require.Error(t, biz.DeletePreset(ctx, 200, builtinID), "内置预设不可删")
}

func TestCreateSession_RejectsForeignPreset(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	// 用户 500 的私有预设。
	require.NoError(t, db.Create(&model.MeetingPreset{
		UserID: 500, Name: "私有", RolePrompt: "rp", AutoIntervalSeconds: 60,
	}).Error)
	var p model.MeetingPreset
	require.NoError(t, db.First(&p, "user_id = ?", 500).Error)

	// 用户 100 引用别人的预设 → 403。
	pid := p.ID
	_, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "rp", PresetID: &pid})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// 转写 ingest（空转写也落库）
// ---------------------------------------------------------------------------

func TestIngestSegment_EmptyTranscriptStillPersists(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// fake ASR 返回空文本（静音段）。
	origASR := asrFn
	t.Cleanup(func() { asrFn = origASR })
	asrFn = func(_ context.Context, _ string, _ aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
		return &aiservice.ASRResponse{Text: "", DurationSeconds: 9.5}, nil
	}

	seg, err := biz.IngestSegment(ctx, 100, dto.ID, &IngestSegmentReq{AudioBytes: []byte("RIFFfakewav"), StartMs: 0})
	require.NoError(t, err)
	assert.Equal(t, 1, seg.Seq, "首段 seq 自增为 1")
	assert.Equal(t, "", seg.Text)
	assert.InDelta(t, 9.5, seg.DurationSeconds, 0.001)

	// 确认空转写已落库。
	var count int64
	require.NoError(t, db.Model(&model.MeetingSegment{}).Where("session_id = ?", dto.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "空转写也落库（保留时间轴）")

	// 第二段 seq 自增为 2。
	seg2, err := biz.IngestSegment(ctx, 100, dto.ID, &IngestSegmentReq{AudioBytes: []byte("x")})
	require.NoError(t, err)
	assert.Equal(t, 2, seg2.Seq)
}

func TestIngestSegment_ASRFailureDegradesToEmpty(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	origASR := asrFn
	t.Cleanup(func() { asrFn = origASR })
	asrFn = func(_ context.Context, _ string, _ aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
		return nil, assertErr("funasr down")
	}

	// ASR 失败时不报错，落空段。
	seg, err := biz.IngestSegment(ctx, 100, dto.ID, &IngestSegmentReq{AudioBytes: []byte("x")})
	require.NoError(t, err)
	assert.Equal(t, "", seg.Text)
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// ---------------------------------------------------------------------------
// 反馈流：auto-skip / auto-generate / manual
// ---------------------------------------------------------------------------

func TestGenerateFeedback_AutoSkip(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "你是陪练"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 1, Text: "闲聊"}).Error)

	// 判官输出 NO_FEEDBACK → skip，不落库。
	withFakeChatStream(t, []string{"NO_FEEDBACK"}, nil)
	cap := &captureSSE{}
	require.NoError(t, biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "auto"}, cap.handler))

	assert.Equal(t, []string{"skip"}, cap.types())
	var count int64
	require.NoError(t, db.Model(&model.MeetingFeedback{}).Where("session_id = ?", dto.ID).Count(&count).Error)
	assert.Equal(t, int64(0), count, "skip 不落库")
}

func TestGenerateFeedback_AutoGenerate(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "你是陪练"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 1, Text: "对方在偷换概念"}).Error)
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 2, Text: "我没反驳"}).Error)

	// 判官认为应反馈：流式吐出正文。
	withFakeChatStream(t, []string{"指出对方", "偷换了概念"}, nil)
	cap := &captureSSE{}
	require.NoError(t, biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "auto"}, cap.handler))

	types := cap.types()
	assert.Contains(t, types, "token", "应有 token 流")
	assert.Equal(t, "done", types[len(types)-1], "最后一个事件是 done")

	// done payload 是 FeedbackDTO 且已落库。
	last := cap.events[len(cap.events)-1]
	doneDTO, ok := last.data.(FeedbackDTO)
	require.True(t, ok)
	assert.Equal(t, "指出对方偷换了概念", doneDTO.Content)
	assert.Equal(t, 2, doneDTO.AnchorSeq, "anchor 取最后一段 seq")

	var stored model.MeetingFeedback
	require.NoError(t, db.First(&stored, "session_id = ?", dto.ID).Error)
	assert.Equal(t, "指出对方偷换了概念", stored.Content)
	assert.Equal(t, "auto", stored.Trigger)
}

func TestGenerateFeedback_ManualAlwaysGenerates(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "你是陪练"})
	require.NoError(t, err)

	// manual 不做 sentinel 判断：即便模型吐出以 NO_FEEDBACK 开头，也按正文处理（系统提示已不给该选项）。
	withFakeChatStream(t, []string{"现在", "给你一条建议"}, nil)
	cap := &captureSSE{}
	require.NoError(t, biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "manual"}, cap.handler))

	types := cap.types()
	assert.Contains(t, types, "token")
	assert.Equal(t, "done", types[len(types)-1])
	var count int64
	require.NoError(t, db.Model(&model.MeetingFeedback{}).Where("session_id = ?", dto.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "manual 总是落库")
}

func TestGenerateFeedback_InvalidTrigger(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)
	cap := &captureSSE{}
	err = biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "bogus"}, cap.handler)
	require.Error(t, err)
}

// TestConsumeFeedbackStream_AutoSkipSplitAcrossChunks 验证 sentinel 跨多个 chunk 分片到达时仍能正确判定 skip。
func TestConsumeFeedbackStream_AutoSkipSplitAcrossChunks(t *testing.T) {
	b := &meetingBiz{}
	ch := make(chan aiservice.ChatChunk, 5)
	// "NO_FEEDBACK" 被切成多片，模拟流式分片。
	for _, d := range []string{"NO", "_FE", "ED", "BACK"} {
		ch <- aiservice.ChatChunk{Delta: d}
	}
	ch <- aiservice.ChatChunk{IsFinal: true}
	close(ch)

	cap := &captureSSE{}
	content, skipped, err := b.consumeFeedbackStream(context.Background(), ch, model.MeetingFeedbackTriggerAuto, cap.handler)
	require.NoError(t, err)
	assert.True(t, skipped, "分片 sentinel 也应判 skip")
	assert.Empty(t, content)
	assert.Empty(t, cap.events, "skip 不发任何 token")
}
