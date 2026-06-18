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
	"fmt"
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
// 纯逻辑：buildRecentTranscriptWindow（最近 5 分钟，按 created_at，FEEDBACK_V2_SPEC §2.3）
// ---------------------------------------------------------------------------

func TestBuildRecentTranscriptWindow(t *testing.T) {
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	window := 5 * time.Minute

	t.Run("empty segments", func(t *testing.T) {
		tr, anchor := buildRecentTranscriptWindow(nil, window, 8000, now)
		assert.Equal(t, "", tr)
		assert.Equal(t, 0, anchor)
	})

	t.Run("keeps only segments within last 5 minutes by created_at", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: "十分钟前的旧句", CreatedAt: now.Add(-10 * time.Minute)}, // 窗口外
			{Seq: 2, Text: "六分钟前", CreatedAt: now.Add(-6 * time.Minute)},     // 窗口外
			{Seq: 3, Text: "四分钟前", CreatedAt: now.Add(-4 * time.Minute)},     // 窗口内
			{Seq: 4, Text: "一分钟前", CreatedAt: now.Add(-1 * time.Minute)},     // 窗口内
		}
		tr, anchor := buildRecentTranscriptWindow(segs, window, 8000, now)
		assert.Equal(t, "四分钟前\n一分钟前", tr, "只保留 5 分钟内的段，按时间顺序")
		assert.Equal(t, 4, anchor, "anchor 取全部分段最后一段 seq（不受窗口影响）")
	})

	t.Run("boundary segment exactly at cutoff is kept", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: "恰在边界", CreatedAt: now.Add(-window)}, // created_at == cutoff，Before(cutoff)=false → 保留
		}
		tr, _ := buildRecentTranscriptWindow(segs, window, 8000, now)
		assert.Equal(t, "恰在边界", tr, "恰在 cutoff 的段保留（>= 边界）")
	})

	t.Run("skips empty segments but keeps anchor", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: "有内容", CreatedAt: now.Add(-1 * time.Minute)},
			{Seq: 2, Text: "   ", CreatedAt: now.Add(-30 * time.Second)}, // 静音
		}
		tr, anchor := buildRecentTranscriptWindow(segs, window, 8000, now)
		assert.Equal(t, "有内容", tr)
		assert.Equal(t, 2, anchor, "anchor 仍取最后一段（含静音）")
	})

	t.Run("zero created_at treated as in-window", func(t *testing.T) {
		// CreatedAt 零值（测试未设/未持久化）不应被误丢。
		segs := []model.MeetingSegment{{Seq: 1, Text: "无时间戳"}}
		tr, _ := buildRecentTranscriptWindow(segs, window, 8000, now)
		assert.Equal(t, "无时间戳", tr)
	})

	t.Run("over rune cap keeps tail only", func(t *testing.T) {
		segs := []model.MeetingSegment{
			{Seq: 1, Text: strings.Repeat("旧", 20), CreatedAt: now.Add(-2 * time.Minute)},
			{Seq: 2, Text: strings.Repeat("新", 10), CreatedAt: now.Add(-1 * time.Minute)},
		}
		// 窗口内总长 20+1(换行)+10=31 runes，cap 15 → 只留尾部 15。
		tr, _ := buildRecentTranscriptWindow(segs, window, 15, now)
		r := []rune(tr)
		require.Len(t, r, 15, "超长截到 cap")
		assert.Equal(t, strings.Repeat("新", 10), string(r[5:]), "尾部保留最新内容")
	})
}

// ---------------------------------------------------------------------------
// 纯逻辑：buildFeedbackUserMessage（三段上下文，FEEDBACK_V2_SPEC §2.3）
// ---------------------------------------------------------------------------

func TestBuildFeedbackUserMessage(t *testing.T) {
	t.Run("all three sections present", func(t *testing.T) {
		fbs := []model.MeetingFeedback{
			{Content: "反馈一"},
			{Content: "反馈二"},
		}
		msg := buildFeedbackUserMessage("## 会议主题/目标\n- 决定上线日期", "最近这五分钟的对话", fbs, 10)
		assert.Contains(t, msg, "[会议滚动摘要]")
		assert.Contains(t, msg, "决定上线日期", "含滚动摘要内容")
		assert.Contains(t, msg, "[最近 5 分钟对话]")
		assert.Contains(t, msg, "最近这五分钟的对话", "含最近逐字")
		assert.Contains(t, msg, "[你已经给过的反馈（不要重复）]")
		assert.Contains(t, msg, "- 反馈一", "含已给反馈清单")
		assert.Contains(t, msg, "- 反馈二")
		// 顺序：摘要 → 逐字 → 已给反馈。
		iSummary := strings.Index(msg, "[会议滚动摘要]")
		iRecent := strings.Index(msg, "[最近 5 分钟对话]")
		iPrior := strings.Index(msg, "[你已经给过的反馈")
		assert.True(t, iSummary < iRecent && iRecent < iPrior, "三段顺序：摘要→逐字→已给反馈")
	})

	t.Run("empty running_summary shows 暂无", func(t *testing.T) {
		msg := buildFeedbackUserMessage("", "有对话", nil, 10)
		// 滚动摘要段为「（暂无）」。
		seg := msg[strings.Index(msg, "[会议滚动摘要]"):strings.Index(msg, "[最近 5 分钟对话]")]
		assert.Contains(t, seg, "（暂无）", "无滚动摘要时占位")
	})

	t.Run("empty transcript shows placeholder", func(t *testing.T) {
		msg := buildFeedbackUserMessage("摘要", "", nil, 10)
		assert.Contains(t, msg, "还没有可用的会议转写内容")
	})

	t.Run("no prior feedbacks shows 暂无", func(t *testing.T) {
		msg := buildFeedbackUserMessage("摘要", "对话", nil, 10)
		priorSeg := msg[strings.Index(msg, "[你已经给过的反馈"):]
		assert.Contains(t, priorSeg, "（暂无）", "无已给反馈时占位")
	})
}

// TestFormatPriorFeedbacks 验证已给反馈清单：取尾部 limit 条、跳过空、空时占位。
func TestFormatPriorFeedbacks(t *testing.T) {
	t.Run("empty returns placeholder", func(t *testing.T) {
		assert.Equal(t, "（暂无）", formatPriorFeedbacks(nil, 10))
	})

	t.Run("takes last N when over limit", func(t *testing.T) {
		var fbs []model.MeetingFeedback
		for i := 1; i <= 15; i++ {
			fbs = append(fbs, model.MeetingFeedback{Content: fmt.Sprintf("反馈%d", i)})
		}
		out := formatPriorFeedbacks(fbs, 10)
		// store 按 created_at ASC，尾部=最新；取最近 10 条 = 反馈6..反馈15。
		assert.NotContains(t, out, "反馈5", "超出 limit 的最早反馈被丢弃")
		assert.Contains(t, out, "反馈6")
		assert.Contains(t, out, "反馈15")
		assert.Equal(t, 10, strings.Count(out, "- "), "恰好 10 条")
	})

	t.Run("skips empty content", func(t *testing.T) {
		fbs := []model.MeetingFeedback{
			{Content: "有效反馈"},
			{Content: "   "},
		}
		out := formatPriorFeedbacks(fbs, 10)
		assert.Equal(t, "- 有效反馈", out, "空内容跳过")
	})
}

// TestBuildFeedbackSystemPrompt_AppendsRollingSummaryNote 验证两种 trigger 都在 role_prompt 后
// 追加「参考滚动摘要、不要重复已给反馈」一句（FEEDBACK_V2_SPEC §2.3），且 auto 仍含 sentinel。
func TestBuildFeedbackSystemPrompt_AppendsRollingSummaryNote(t *testing.T) {
	const note = "请参考下面的会议滚动摘要"

	auto := buildFeedbackSystemPrompt("你是辩论陪练", model.MeetingFeedbackTriggerAuto)
	assert.Contains(t, auto, "你是辩论陪练", "保留 role_prompt")
	assert.Contains(t, auto, noFeedbackSentinel, "auto 仍提供 NO_FEEDBACK 选项（机制不动）")
	assert.Contains(t, auto, note, "auto 追加滚动摘要提示")
	assert.Contains(t, auto, "不要逐字重复你已经给过的反馈", "auto 规则内含去重提示(软化版)")

	manual := buildFeedbackSystemPrompt("你是辩论陪练", model.MeetingFeedbackTriggerManual)
	assert.Contains(t, manual, "必须给出一条反馈", "manual 必须给反馈")
	assert.NotContains(t, manual, noFeedbackSentinel, "manual 不提供 NO_FEEDBACK 选项")
	assert.Contains(t, manual, note, "manual 也追加滚动摘要去重提示")
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

// withSyncSummarySpawn 把异步纪要派发改为同步执行（FEEDBACK_V2_SPEC §3.1 异步段），让测试能
// 在 EndSession 返回后立即断言最终状态（done/failed）。
func withSyncSummarySpawn(t *testing.T) {
	t.Helper()
	orig := asyncSummarySpawn
	t.Cleanup(func() { asyncSummarySpawn = orig })
	asyncSummarySpawn = func(fn func()) { fn() }
}

// TestEndSession_AsyncStateMachineDone 验证 §3.1 两段式：同步段秒回 generating，异步段跑完置 done。
//
// 用 captureSpawn 捕获异步派发但延后执行，从而能先断言「同步段秒回 generating」，再手动驱动
// 异步段断言 done——避免真 goroutine 在 t.Cleanup 关库后悬挂访问（in-memory SQLite 测试隔离）。
func TestEndSession_AsyncStateMachineDone(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// 塞一段转写，让 summary 走 LLM 路径（无 running_summary → 回退全稿）。
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

	// 捕获异步段（延后到测试线程内手动执行，不起真 goroutine）。
	origSpawn := asyncSummarySpawn
	t.Cleanup(func() { asyncSummarySpawn = origSpawn })
	var deferred func()
	asyncSummarySpawn = func(fn func()) { deferred = fn }

	// 同步段秒回：返回时是 generating。
	ended, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusEnded, ended.Status)
	assert.Equal(t, model.MeetingSummaryStatusGenerating, ended.SummaryStatus, "同步段秒回 generating")
	assert.NotNil(t, ended.EndedAt)
	assert.Greater(t, ended.DurationSeconds, 0)
	// 同步段已持久化 ended+generating。
	persisted, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusEnded, persisted.Session.Status)
	assert.Equal(t, model.MeetingSummaryStatusGenerating, persisted.Session.SummaryStatus)

	// 驱动异步段（在测试线程内同步执行），断言落到 done。
	require.NotNil(t, deferred, "EndSession 应派发异步段")
	deferred()
	done, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusDone, done.Session.SummaryStatus, "异步段跑完置 done")
	assert.Contains(t, done.Session.Summary, "## 要点")

	// 幂等：再次 end 不报错，状态保持 ended、纪要不被重置（已 ended 直接返回，不再派发）。
	deferred = nil
	again, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSessionStatusEnded, again.Status)
	assert.Equal(t, model.MeetingSummaryStatusDone, again.SummaryStatus, "幂等 end 不重置已完成纪要")
	assert.Nil(t, deferred, "已 ended 的会话不再派发异步段")
}

// TestEndSession_SyncSpawnEndToEnd 用 sync spawn seam 验证 EndSession 一次调用即跑完 done。
func TestEndSession_SyncSpawnEndToEnd(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	withSyncSummarySpawn(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 1, Text: "讨论了上线时间"}).Error)

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	summaryChatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "## 要点\n- 上线时间\n\n## 决议\n- （无）\n\n## 待办\n- （无）"}, nil
	}

	_, err = biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)

	// sync spawn → 异步段已在 EndSession 内同步跑完。
	done, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusDone, done.Session.SummaryStatus)
	assert.Contains(t, done.Session.Summary, "## 要点")
}

// TestEndSession_AsyncStateMachineFailed 验证异步段 LLM 失败时置 failed（状态机 generating→failed）。
func TestEndSession_AsyncStateMachineFailed(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)
	// 有转写 → 走 LLM 路径（空转写会降级成功，测不到 failed）。
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: dto.ID, Seq: 1, Text: "有内容"}).Error)

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	summaryChatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, assertErr("llm down")
	}

	// 捕获异步段，延后到测试线程内执行（避免悬挂 goroutine）。
	origSpawn := asyncSummarySpawn
	t.Cleanup(func() { asyncSummarySpawn = origSpawn })
	var deferred func()
	asyncSummarySpawn = func(fn func()) { deferred = fn }

	ended, err := biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusGenerating, ended.SummaryStatus, "同步段仍秒回 generating")

	// 驱动异步段：LLM 失败 → failed。
	require.NotNil(t, deferred)
	deferred()
	failed, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusFailed, failed.Session.SummaryStatus, "LLM 失败置 failed")
}

// TestEndSession_EmptyTranscriptDegradesSummary 无转写时异步段走降级占位（不调 LLM），置 done。
func TestEndSession_EmptyTranscriptDegradesSummary(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	withSyncSummarySpawn(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "x"})
	require.NoError(t, err)

	// 不塞任何转写 → generateFinalSummary 回退 generateSummary 降级占位（不调 LLM），summary_status=done。
	_, err = biz.EndSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	done, err := biz.GetSession(ctx, 100, dto.ID)
	require.NoError(t, err)
	assert.Equal(t, model.MeetingSummaryStatusDone, done.Session.SummaryStatus)
	assert.Contains(t, done.Session.Summary, "没有可用的转写内容")
}

// TestGenerateFinalSummary_UsesRunningSummary 验证有 running_summary 时走滚动摘要路径（不读全稿）。
func TestGenerateFinalSummary_UsesRunningSummary(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	var gotUserMsg string
	summaryChatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// capture 用户消息，断言它含 running_summary 而非全稿。
		gotUserMsg = req.Messages[len(req.Messages)-1].Content.Text
		return &aiservice.ChatResponse{Content: "## 要点\n- 来自滚动摘要\n\n## 决议\n- （无）\n\n## 待办\n- （无）"}, nil
	}

	s := &model.MeetingSession{ID: 1, Title: "T", RunningSummary: "## 会议主题/目标\n- 决定上线日期"}
	out, err := biz.(*meetingBiz).generateFinalSummary(ctx, 100, s, nil)
	require.NoError(t, err)
	assert.Contains(t, out, "来自滚动摘要")
	assert.Contains(t, gotUserMsg, "会议滚动摘要", "应基于滚动摘要而非全稿")
	assert.Contains(t, gotUserMsg, "决定上线日期")
}

// TestGenerateFinalSummary_FallbackToFullTranscript 验证无 running_summary 时回退读全稿。
func TestGenerateFinalSummary_FallbackToFullTranscript(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()

	origSummary := summaryChatFn
	t.Cleanup(func() { summaryChatFn = origSummary })
	var gotUserMsg string
	summaryChatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		gotUserMsg = req.Messages[len(req.Messages)-1].Content.Text
		return &aiservice.ChatResponse{Content: "## 要点\n- 来自全稿"}, nil
	}

	s := &model.MeetingSession{ID: 2, Title: "T"} // RunningSummary 空
	segs := []model.MeetingSegment{{Seq: 1, Text: "全稿里的内容"}}
	out, err := biz.(*meetingBiz).generateFinalSummary(ctx, 100, s, segs)
	require.NoError(t, err)
	assert.Contains(t, out, "来自全稿")
	assert.Contains(t, gotUserMsg, "完整转写", "回退路径喂全稿")
	assert.Contains(t, gotUserMsg, "全稿里的内容")
}

// TestClampAutoInterval 验证间隔 clamp 到 [5,60]（FEEDBACK_V2_SPEC §1）。
func TestClampAutoInterval(t *testing.T) {
	assert.Equal(t, 5, clampAutoInterval(1), "下限 5")
	assert.Equal(t, 5, clampAutoInterval(5))
	assert.Equal(t, 15, clampAutoInterval(15))
	assert.Equal(t, 60, clampAutoInterval(60))
	assert.Equal(t, 60, clampAutoInterval(120), "上限 60")
}

// TestCreateSession_ClampsInterval 验证 CreateSession 把越界间隔 clamp（5-60）。
func TestCreateSession_ClampsInterval(t *testing.T) {
	biz, _ := newMeetingTestBiz(t)
	ctx := context.Background()

	tooHigh := 999
	dto, err := biz.CreateSession(ctx, 1, &CreateSessionReq{RolePrompt: "x", AutoIntervalSeconds: &tooHigh})
	require.NoError(t, err)
	assert.Equal(t, 60, dto.AutoIntervalSeconds, "越上限 clamp 到 60")

	tooLow := 2
	dto2, err := biz.CreateSession(ctx, 1, &CreateSessionReq{RolePrompt: "x", AutoIntervalSeconds: &tooLow})
	require.NoError(t, err)
	assert.Equal(t, 5, dto2.AutoIntervalSeconds, "越下限 clamp 到 5")
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

// withCapturingChatStream 替换 chatStreamFn，捕获网关收到的 ChatRequest（断言三段上下文注入），
// 并按 deltas 流式回放。返回一个指向被捕获请求的指针（调用后读取）。
func withCapturingChatStream(t *testing.T, deltas []string) *aiservice.ChatRequest {
	t.Helper()
	orig := chatStreamFn
	t.Cleanup(func() { chatStreamFn = orig })
	captured := &aiservice.ChatRequest{}
	chatStreamFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		*captured = req
		ch := make(chan aiservice.ChatChunk, len(deltas)+1)
		for i, d := range deltas {
			ch <- aiservice.ChatChunk{Delta: d, Index: i}
		}
		ch <- aiservice.ChatChunk{IsFinal: true, FinishReason: "stop"}
		close(ch)
		return ch, nil
	}
	return captured
}

// chatUserText 取 ChatRequest 最后一条（user）消息文本。
func chatUserText(req *aiservice.ChatRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Content.Text
}

// TestGenerateFeedback_ThreeSectionContext 验证 §2.3：判官收到的 user 消息含三段——滚动摘要 +
// 最近 5 分钟逐字（窗口外旧段被排除）+ 已给反馈清单。
func TestGenerateFeedback_ThreeSectionContext(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "你是陪练"})
	require.NoError(t, err)

	// 写 running_summary。
	require.NoError(t, db.Model(&model.MeetingSession{}).Where("id = ?", dto.ID).
		Update("running_summary", "## 会议主题/目标\n- 讨论上线节奏").Error)

	// 一段在 5 分钟窗口外（旧），一段在窗口内（新）。created_at 由 db 直接控制。
	require.NoError(t, db.Create(&model.MeetingSegment{
		SessionID: dto.ID, Seq: 1, Text: "窗口外的旧对话", CreatedAt: time.Now().Add(-10 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.MeetingSegment{
		SessionID: dto.ID, Seq: 2, Text: "窗口内的最新对话", CreatedAt: time.Now().Add(-1 * time.Minute),
	}).Error)

	// 一条已给反馈。
	require.NoError(t, db.Create(&model.MeetingFeedback{
		SessionID: dto.ID, Trigger: "auto", AnchorSeq: 1, Content: "之前提醒过你注意逻辑",
	}).Error)

	captured := withCapturingChatStream(t, []string{"新", "反馈"})
	cap := &captureSSE{}
	require.NoError(t, biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "auto"}, cap.handler))

	userMsg := chatUserText(captured)
	assert.Contains(t, userMsg, "[会议滚动摘要]")
	assert.Contains(t, userMsg, "讨论上线节奏", "滚动摘要注入")
	assert.Contains(t, userMsg, "[最近 5 分钟对话]")
	assert.Contains(t, userMsg, "窗口内的最新对话", "窗口内逐字注入")
	assert.NotContains(t, userMsg, "窗口外的旧对话", "5 分钟窗口外的旧段被排除")
	assert.Contains(t, userMsg, "[你已经给过的反馈（不要重复）]")
	assert.Contains(t, userMsg, "之前提醒过你注意逻辑", "已给反馈注入")

	// 系统提示含去重提示。
	require.NotEmpty(t, captured.Messages)
	sysMsg := captured.Messages[0].Content.Text
	assert.Contains(t, sysMsg, "不要逐字重复你已经给过的反馈")

	// anchor 仍取最后一段 seq（不受窗口影响）。
	last := cap.events[len(cap.events)-1]
	doneDTO, ok := last.data.(FeedbackDTO)
	require.True(t, ok)
	assert.Equal(t, 2, doneDTO.AnchorSeq, "anchor 取最后一段 seq（含窗口外段）")
}

// TestGenerateFeedback_NoRunningSummaryShowsPlaceholder 验证无 running_summary 时滚动摘要段为占位。
func TestGenerateFeedback_NoRunningSummaryShowsPlaceholder(t *testing.T) {
	biz, db := newMeetingTestBiz(t)
	ctx := context.Background()
	dto, err := biz.CreateSession(ctx, 100, &CreateSessionReq{RolePrompt: "你是陪练"})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.MeetingSegment{
		SessionID: dto.ID, Seq: 1, Text: "对话内容", CreatedAt: time.Now(),
	}).Error)

	captured := withCapturingChatStream(t, []string{"反馈"})
	cap := &captureSSE{}
	require.NoError(t, biz.GenerateFeedback(ctx, 100, dto.ID, &FeedbackReq{Trigger: "manual"}, cap.handler))

	userMsg := chatUserText(captured)
	summarySeg := userMsg[strings.Index(userMsg, "[会议滚动摘要]"):strings.Index(userMsg, "[最近 5 分钟对话]")]
	assert.Contains(t, summarySeg, "（暂无）", "无 running_summary 时占位")
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
