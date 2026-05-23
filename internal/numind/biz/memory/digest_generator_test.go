package memory

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// ─── Test helpers ────────────────────────────────────────────────────────────

// newDigestTestStores spins up SQLite + auto-migrates the memory tables and
// installs an explicit agent_run DDL (avoids the `datetime(3)` MySQL-precision
// syntax issue that breaks SQLite scan — see store/agent_run_test.go pattern).
func newDigestTestStores(t *testing.T) (
	store.IMemoryDigestStore,
	store.IUserMemoryFactStore,
	store.IUserMemoryProfileStore,
	*gorm.DB,
) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)

	// AutoMigrate memory tables (use the GORM model definitions — no
	// datetime(3) issues here since our digest models declare plain time.Time).
	require.NoError(t, db.AutoMigrate(
		&model.UserMemoryDigestDaily{},
		&model.UserMemoryDigestWeekly{},
		&model.UserMemoryDigestMonthly{},
		&model.UserMemoryDigestQuarterly{},
		&model.UserMemoryFact{},
		&model.UserMemoryProfile{},
	))

	// Explicit agent_run DDL — mirrors store/agent_run_test.go pattern so SQLite
	// doesn't choke on the MySQL `datetime(3)` precision syntax from the GORM tag.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                   INTEGER NOT NULL,
			session_id                TEXT    NOT NULL DEFAULT '',
			status                    TEXT    NOT NULL DEFAULT 'running',
			state_reason              TEXT    NOT NULL DEFAULT '',
			messages                  TEXT    NOT NULL DEFAULT '[]',
			reservation_id            INTEGER,
			started_at                DATETIME NOT NULL,
			ended_at                  DATETIME,
			compact_state             TEXT,
			compact_summary           TEXT,
			terminal_metadata         TEXT,
			cancellation_requested_at DATETIME,
			agent_definition_id       INTEGER,
			pending_question_json     TEXT,
			pending_question_at       DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return store.NewMemoryDigestStore(db),
		store.NewUserMemoryFactStore(db),
		store.NewUserMemoryProfileStore(db),
		db
}

// validDigestJSON is a canonical, easily-recognisable LLM response.
const validDigestJSON = `{"summary":"用户在当日跟进了 3 家医院的合同条款, 主要讨论 CT 设备采购. 偏好简洁直接的话术.","key_topics":["医院合同","CT设备","跟进"]}`

// staticDigestChat returns a mock that always replies with the given text.
type mockDigestChat struct {
	mu      sync.Mutex
	calls   int
	respFn  func(callIdx int, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)
	lastReq aiservice.ChatRequest
}

func newMockDigestChat(fn func(int, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) *mockDigestChat {
	return &mockDigestChat{respFn: fn}
}

func (m *mockDigestChat) fn() digestChatFn {
	return func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		m.mu.Lock()
		idx := m.calls
		m.calls++
		m.lastReq = req
		m.mu.Unlock()
		return m.respFn(idx, req)
	}
}

func (m *mockDigestChat) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// seedAgentRun inserts an agent_run row with the given user/session/timestamp
// and a single user→assistant message pair.
func seedAgentRun(t *testing.T, db *gorm.DB, userID uint, sessionID string, started time.Time) {
	t.Helper()
	msgs := []map[string]any{
		{"role": "user", "content": "我今天想跟进 XX 医院的合同"},
		{"role": "assistant", "content": "好的, 我帮你列出关键条款"},
	}
	raw, _ := json.Marshal(msgs)
	run := &model.AgentRun{
		UserID:    userID,
		SessionID: sessionID,
		Status:    "terminated",
		Messages:  datatypes.JSON(raw),
		StartedAt: started,
	}
	require.NoError(t, db.Create(run).Error)
}

// ─── GenerateDaily ───────────────────────────────────────────────────────────

func TestGenerateDaily_HappyPath(t *testing.T) {
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	const uid uint = 1001
	yesterday := time.Date(2026, 5, 22, 10, 30, 0, 0, loc)

	// Seed 2 agent_runs on the target day.
	seedAgentRun(t, db, uid, "sess-a", yesterday)
	seedAgentRun(t, db, uid, "sess-b", yesterday.Add(2*time.Hour))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock-qwen-plus"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	d, err := gen.GenerateDaily(ctx, uid, yesterday)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, uid, d.UserID)
	assert.Equal(t, 2, d.SessionCount, "2 distinct sessions")
	assert.Equal(t, 4, d.MessageCount, "2 runs × 2 msgs = 4")
	assert.Contains(t, d.Summary, "医院")
	assert.Equal(t, 1, mc.callCount(), "1 LLM call on happy path")
}

func TestGenerateDaily_NoActivity_StillReturnsDigest(t *testing.T) {
	// Cron has already filtered active users — but if a user has 0 runs in
	// the window (race / bug), GenerateDaily should still return a digest
	// (so Upsert produces a deterministic row).
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1002

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: `{"summary":"（无 substantive 活动）","key_topics":[]}`, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	d, err := gen.GenerateDaily(ctx, uid, fixedNow())
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, 0, d.SessionCount)
	assert.Equal(t, 0, d.MessageCount)
	assert.Contains(t, d.Summary, "无 substantive")
}

// ─── GenerateWeekly aggregates from daily ─────────────────────────────────────

func TestGenerateWeekly_AggregatesFromDaily(t *testing.T) {
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	const uid uint = 1003

	// Seed 3 daily digests covering Mon-Wed of 2026-W21 (Mon = May 18).
	mon := time.Date(2026, 5, 18, 0, 0, 0, 0, loc)
	for i := 0; i < 3; i++ {
		require.NoError(t, digestStore.UpsertDaily(ctx, &model.UserMemoryDigestDaily{
			UserID:     uid,
			DigestDate: mon.AddDate(0, 0, i),
			Summary:    "day-" + string(rune('A'+i)) + " 内容",
			KeyTopics:  keyTopicsToJSON([]string{"topic-" + string(rune('A'+i))}),
		}))
	}

	mc := newMockDigestChat(func(_ int, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// Verify prompt contains the daily summaries we seeded.
		assert.Contains(t, req.Messages[0].Content.Text, "day-A")
		assert.Contains(t, req.Messages[0].Content.Text, "day-B")
		return &aiservice.ChatResponse{
			Content: `{"summary":"用户上周连续 3 天关注同一主题","key_topics":["主题A","主题B"]}`,
			Model:   "mock-qwen-plus",
		}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	w, err := gen.GenerateWeekly(ctx, uid, 2026, 21)
	require.NoError(t, err)
	require.NotNil(t, w)
	assert.Equal(t, 2026, w.ISOYear)
	assert.Equal(t, 21, w.ISOWeek)
	assert.Contains(t, w.Summary, "连续")
	assert.Equal(t, "2026-05-18", w.WeekStartDate.Format("2006-01-02"))
	assert.Equal(t, "2026-05-24", w.WeekEndDate.Format("2006-01-02"))
}

// ─── GenerateMonthly aggregates from weekly ───────────────────────────────────

func TestGenerateMonthly_AggregatesFromWeekly(t *testing.T) {
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1004

	// Seed 4 weekly digests covering May 2026 (W18-W22).
	for w := 18; w <= 21; w++ {
		mon := isoWeekStart(2026, w)
		sun := mon.AddDate(0, 0, 6)
		require.NoError(t, digestStore.UpsertWeekly(ctx, &model.UserMemoryDigestWeekly{
			UserID:        uid,
			ISOYear:       2026,
			ISOWeek:       w,
			WeekStartDate: mon,
			WeekEndDate:   sun,
			Summary:       "week W" + string(rune('0'+(w%10))) + " summary",
		}))
	}

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	m, err := gen.GenerateMonthly(ctx, uid, 2026, 5)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, 2026, m.Year)
	assert.Equal(t, 5, m.Month)
	assert.NotEmpty(t, m.Summary)
}

// ─── GenerateQuarterly aggregates from monthly ────────────────────────────────

func TestGenerateQuarterly_AggregatesFromMonthly(t *testing.T) {
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1005

	for mo := 4; mo <= 6; mo++ {
		require.NoError(t, digestStore.UpsertMonthly(ctx, &model.UserMemoryDigestMonthly{
			UserID:  uid,
			Year:    2026,
			Month:   mo,
			Summary: "month-" + string(rune('0'+mo)) + " summary",
		}))
	}

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	q, err := gen.GenerateQuarterly(ctx, uid, 2026, 2)
	require.NoError(t, err)
	require.NotNil(t, q)
	assert.Equal(t, 2, q.Quarter)
	assert.NotEmpty(t, q.Summary)
}

// ─── JSON parse retry + fallback ──────────────────────────────────────────────

func TestGenerate_JSON_Malformed_RetryThenFallback(t *testing.T) {
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1006

	// LLM returns garbage on both attempts → fallback summary.
	mc := newMockDigestChat(func(idx int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "this is not json (attempt " + string(rune('0'+idx)) + ")", Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	d, err := gen.GenerateDaily(ctx, uid, fixedNow())
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, digestParseFallback, d.Summary, "after 2 parse failures, summary uses fallback")
	assert.Equal(t, 2, mc.callCount(), "exactly 2 LLM calls (1 + 1 retry)")
}

func TestGenerate_JSON_Malformed_FirstRetrySucceeds(t *testing.T) {
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1007

	mc := newMockDigestChat(func(idx int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if idx == 0 {
			return &aiservice.ChatResponse{Content: "garbage", Model: "mock"}, nil
		}
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	d, err := gen.GenerateDaily(ctx, uid, fixedNow())
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Contains(t, d.Summary, "医院", "retry succeeded → real summary used")
	assert.NotEqual(t, digestParseFallback, d.Summary)
	assert.Equal(t, 2, mc.callCount())
}

// ─── parseDigestLLMOutput ─────────────────────────────────────────────────────

func TestParseDigestLLMOutput_Strict(t *testing.T) {
	out, err := parseDigestLLMOutput(validDigestJSON)
	require.NoError(t, err)
	assert.Contains(t, out.Summary, "医院")
	assert.Len(t, out.KeyTopics, 3)
}

func TestParseDigestLLMOutput_WithFence(t *testing.T) {
	wrapped := "```json\n" + validDigestJSON + "\n```"
	out, err := parseDigestLLMOutput(wrapped)
	require.NoError(t, err)
	assert.Contains(t, out.Summary, "医院")
}

func TestParseDigestLLMOutput_WithProse(t *testing.T) {
	wrapped := "下面是 JSON: " + validDigestJSON + " 希望对你有用"
	out, err := parseDigestLLMOutput(wrapped)
	require.NoError(t, err)
	assert.Contains(t, out.Summary, "医院")
}

func TestParseDigestLLMOutput_Empty(t *testing.T) {
	_, err := parseDigestLLMOutput("")
	require.Error(t, err)
}

// ─── UpsertDaily idempotent ──────────────────────────────────────────────────

func TestUpsertDaily_IdempotentSameDate(t *testing.T) {
	digestStore, _, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	const uid uint = 1008
	loc := shanghaiLoc
	date := time.Date(2026, 5, 22, 0, 0, 0, 0, loc)

	for i := 0; i < 2; i++ {
		require.NoError(t, digestStore.UpsertDaily(ctx, &model.UserMemoryDigestDaily{
			UserID:       uid,
			DigestDate:   date,
			SessionCount: i + 1,
			Summary:      "v" + string(rune('0'+i)),
		}))
	}
	d, err := digestStore.GetDaily(ctx, uid, date)
	require.NoError(t, err)
	assert.Equal(t, 2, d.SessionCount, "second upsert overwrites first")
	assert.Equal(t, "v1", d.Summary)
}
