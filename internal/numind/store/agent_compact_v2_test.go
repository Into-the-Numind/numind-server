package store

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
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/pkg/model"
)

// newTestAgentCompactV2DB creates a SQLite test database with the agent_run table
// (V1 + V2 columns) using explicit DDL. Mirrors newTestAgentRunStore pattern; SQLite
// doesn't accept MySQL `datetime(3)` literal precision, so we use plain DATETIME.
//
// Returns the *gorm.DB so individual tests can wire BOTH IAgentRunStore (to
// seed rows via Create) AND IAgentCompactV2Store (the V2-only impl under test).
func newTestAgentCompactV2DB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_compact_v2_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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
			updated_at                DATETIME,
			-- V2 columns added by task 2.1
			compact_state_v2          TEXT,
			total_tokens_used_v2      INTEGER NOT NULL DEFAULT 0,
			use_compact_v2            INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2   INTEGER
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedAgentRun inserts a minimal agent_run row and returns its id.
func seedAgentRun(t *testing.T, db *gorm.DB) uint64 {
	t.Helper()
	run := &model.AgentRun{
		UserID:    1,
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, newAgentRunStore(db).Create(context.Background(), run))
	require.NotZero(t, run.ID)
	return run.ID
}

// TestAgentCompactV2Store_UseFlag_DefaultFalse — spec 验证 case 5：
// 新建 agent_run 默认 use_compact_v2=false, compact_state_v2=NULL.
func TestAgentCompactV2Store_UseFlag_DefaultFalse(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	runID := seedAgentRun(t, db)
	v2 := newAgentCompactV2Store(db)

	state, err := v2.GetCompactStateV2(context.Background(), runID)
	require.NoError(t, err)
	assert.Nil(t, state, "compact_state_v2 should be NULL for fresh row")

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.False(t, row.UseCompactV2, "use_compact_v2 must default to false")
	assert.Equal(t, int64(0), row.TotalTokensUsedV2, "total_tokens_used_v2 must default to 0")
	assert.Nil(t, row.ContextWindowLimitV2, "context_window_limit_v2 should be NULL by default")
}

// TestAgentCompactV2Store_SetUseCompactV2True — spec 验证 case 6.
func TestAgentCompactV2Store_SetUseCompactV2True(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	runID := seedAgentRun(t, db)
	v2 := newAgentCompactV2Store(db)

	require.NoError(t, v2.SetUseCompactV2(context.Background(), runID, true))

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.True(t, row.UseCompactV2)

	// 二次调用不报错（schema 层不强制 frozen，由 runner 层保证）
	require.NoError(t, v2.SetUseCompactV2(context.Background(), runID, false))
	require.NoError(t, db.First(&row, runID).Error)
	assert.False(t, row.UseCompactV2)
}

// TestAgentCompactV2Store_SetUseCompactV2_NotFound verifies error when run doesn't exist.
func TestAgentCompactV2Store_SetUseCompactV2_NotFound(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	err := v2.SetUseCompactV2(context.Background(), 99999, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no row matched")
}

// TestAgentCompactV2Store_UpdateCompactStateV2_RoundTrip — spec 验证 case 4：
// 写 V2 state → 读回相等；并验证 V1 字段 compact_state 未被触碰.
func TestAgentCompactV2Store_UpdateCompactStateV2_RoundTrip(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	ctx := context.Background()

	// 先用 V1 路径写入 compact_state (raw SQL，绕开 V2 接口)
	runID := seedAgentRun(t, db)
	v1JSON := `{"strategy_used":"reactive_compact","consecutive_failures":1}`
	require.NoError(t, db.Exec("UPDATE agent_run SET compact_state = ? WHERE id = ?", v1JSON, runID).Error)

	// 再走 V2 接口写 compact_state_v2
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	state := &compactv2.CompactStateV2{
		CurrentPhase:                   "L2_microcompacted",
		EstimatedTokens:                30_000,
		ConsecutiveAutocompactFailures: 0,
		SummaryMessageUUID:             "uuid-summary-xyz",
		LastCompactionAt:               now,
		TotalAutocompactRuns:           2,
	}
	require.NoError(t, v2.UpdateCompactStateV2(ctx, runID, state))

	// 读回 V2 → 字段相等
	got, err := v2.GetCompactStateV2(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, state.CurrentPhase, got.CurrentPhase)
	assert.Equal(t, state.EstimatedTokens, got.EstimatedTokens)
	assert.Equal(t, state.SummaryMessageUUID, got.SummaryMessageUUID)
	assert.Equal(t, state.TotalAutocompactRuns, got.TotalAutocompactRuns)
	assert.True(t, state.LastCompactionAt.Equal(got.LastCompactionAt))

	// 验证 V1 compact_state 完全没动
	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.JSONEq(t, v1JSON, string(row.CompactState), "V1 compact_state must not be touched by V2 write")
}

// TestAgentCompactV2Store_UpdateCompactStateV2_NotFound verifies error path.
func TestAgentCompactV2Store_UpdateCompactStateV2_NotFound(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	err := v2.UpdateCompactStateV2(context.Background(), 99999, &compactv2.CompactStateV2{
		CurrentPhase: "active",
	})
	require.Error(t, err)
}

// TestAgentCompactV2Store_UpdateCompactStateV2_NilState verifies nil input rejected.
func TestAgentCompactV2Store_UpdateCompactStateV2_NilState(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)
	err := v2.UpdateCompactStateV2(context.Background(), runID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

// TestAgentCompactV2Store_IncrementTokensUsedV2_Atomic — spec 验证 case 3：
// 必须 expr UPDATE col=col+? 原子，并发安全。
func TestAgentCompactV2Store_IncrementTokensUsedV2_Atomic(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)

	// 2 goroutine 各 +5 各 1 次 → 期望 total=10（顺序累加）
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = v2.IncrementTokensUsedV2(context.Background(), runID, 5)
	}()
	go func() {
		defer wg.Done()
		_ = v2.IncrementTokensUsedV2(context.Background(), runID, 5)
	}()
	wg.Wait()

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.Equal(t, int64(10), row.TotalTokensUsedV2, "两次 +5 必须累加为 10（atomic UPDATE col=col+?）")
}

// TestAgentCompactV2Store_IncrementTokensUsedV2_Repeated verifies many sequential
// increments produce the correct total.
func TestAgentCompactV2Store_IncrementTokensUsedV2_Repeated(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)

	for i := 0; i < 7; i++ {
		require.NoError(t, v2.IncrementTokensUsedV2(context.Background(), runID, 3))
	}

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.Equal(t, int64(21), row.TotalTokensUsedV2)
}

// TestAgentCompactV2Store_IncrementTokensUsedV2_NotFound verifies error path.
func TestAgentCompactV2Store_IncrementTokensUsedV2_NotFound(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	err := v2.IncrementTokensUsedV2(context.Background(), 99999, 5)
	require.Error(t, err)
}

// TestAgentCompactV2Store_SetContextWindowLimitV2 verifies the limit is persisted.
func TestAgentCompactV2Store_SetContextWindowLimitV2(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)

	require.NoError(t, v2.SetContextWindowLimitV2(context.Background(), runID, 128_000))

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	require.NotNil(t, row.ContextWindowLimitV2)
	assert.Equal(t, 128_000, *row.ContextWindowLimitV2)
}

// TestAgentCompactV2Store_SetContextWindowLimitV2_NotFound verifies error path.
func TestAgentCompactV2Store_SetContextWindowLimitV2_NotFound(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	err := v2.SetContextWindowLimitV2(context.Background(), 99999, 100_000)
	require.Error(t, err)
}

// TestAgentCompactV2Store_UpdateMessagesV2_RoundTrip verifies V2 messages with
// uuid+meta are persisted and reading back via the V1-style raw column shows
// the new fields are present (V1 reader would ignore them per spec).
func TestAgentCompactV2Store_UpdateMessagesV2_RoundTrip(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)

	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	msgs := []compactv2.MessageV2{
		{
			UUID:    "msg-1",
			Role:    "user",
			Content: "hi",
		},
		{
			UUID:       "msg-2",
			Role:       "tool",
			Content:    "ok",
			ToolCallID: "call-1",
			Meta: &compactv2.MessageMetaV2{
				IsCompacted:     true,
				CompactionPhase: "L0",
				ArtifactRef:     "art-uuid-abc",
				ToolName:        "file_read",
				CompactedAt:     now,
			},
		},
	}
	require.NoError(t, v2.UpdateMessagesV2(context.Background(), runID, msgs))

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)

	// 直接读 messages JSON，验证 V2 字段 (uuid + meta) 持久化
	raw := string(row.Messages)
	assert.Contains(t, raw, `"uuid":"msg-1"`)
	assert.Contains(t, raw, `"uuid":"msg-2"`)
	assert.Contains(t, raw, `"meta":`)
	assert.Contains(t, raw, `"compaction_phase":"L0"`)
	assert.Contains(t, raw, `"artifact_ref":"art-uuid-abc"`)
}

// TestAgentCompactV2Store_UpdateMessagesV2_Empty verifies nil/empty slices are
// persisted as `[]` not `null` (matches V1 default value).
func TestAgentCompactV2Store_UpdateMessagesV2_Empty(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	runID := seedAgentRun(t, db)

	require.NoError(t, v2.UpdateMessagesV2(context.Background(), runID, nil))

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.Equal(t, `[]`, string(row.Messages))
}

// TestV1Path_NotTouchingV2Fields — spec 验证 case 10：
// 直接 raw SQL 写 V1 字段 → 验证 V2 字段保持 NULL/0.
func TestV1Path_NotTouchingV2Fields(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	runID := seedAgentRun(t, db)

	// 模拟 V1 包路径：直接写 compact_state / compact_summary
	require.NoError(t, db.Exec(
		"UPDATE agent_run SET compact_state = ?, compact_summary = ? WHERE id = ?",
		`{"strategy_used":"reactive_compact"}`, "v1 summary text", runID,
	).Error)

	// 用 V2 store 验证 V2 字段未被触碰
	v2 := newAgentCompactV2Store(db)
	state, err := v2.GetCompactStateV2(context.Background(), runID)
	require.NoError(t, err)
	assert.Nil(t, state, "V2 compact_state_v2 must remain NULL after V1 write")

	var row model.AgentRun
	require.NoError(t, db.First(&row, runID).Error)
	assert.Equal(t, int64(0), row.TotalTokensUsedV2, "total_tokens_used_v2 must remain 0 after V1 write")
	assert.False(t, row.UseCompactV2, "use_compact_v2 must remain false after V1 write")
	assert.Nil(t, row.ContextWindowLimitV2, "context_window_limit_v2 must remain NULL after V1 write")
}

// TestAgentCompactV2Store_GetCompactStateV2_NotFound verifies error path.
func TestAgentCompactV2Store_GetCompactStateV2_NotFound(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	v2 := newAgentCompactV2Store(db)
	_, err := v2.GetCompactStateV2(context.Background(), 99999)
	require.Error(t, err)
}

// TestAgentCompactV2Store_GetCompactStateV2_NullJSON verifies that literal
// `null` JSON value is treated as nil state (defensive — different MySQL/SQLite
// drivers may serialize NULL differently).
func TestAgentCompactV2Store_GetCompactStateV2_NullJSON(t *testing.T) {
	db := newTestAgentCompactV2DB(t)
	runID := seedAgentRun(t, db)
	require.NoError(t, db.Exec("UPDATE agent_run SET compact_state_v2 = ? WHERE id = ?", "null", runID).Error)

	v2 := newAgentCompactV2Store(db)
	state, err := v2.GetCompactStateV2(context.Background(), runID)
	require.NoError(t, err)
	assert.Nil(t, state)
}

// helper: marshal CompactStateV2 to confirm JSON encoding works as expected
// (used as a sanity check; the real test is round-trip in
// TestAgentCompactV2Store_UpdateCompactStateV2_RoundTrip).
func TestCompactStateV2_JSONShape(t *testing.T) {
	state := &compactv2.CompactStateV2{
		CurrentPhase:    "active",
		EstimatedTokens: 100,
	}
	b, err := json.Marshal(state)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"current_phase":"active"`)
	assert.Contains(t, string(b), `"estimated_tokens":100`)
}
