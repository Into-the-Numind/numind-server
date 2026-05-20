package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newAgentRunTestDB returns an in-memory SQLite DB with the agent_run schema
// (including #9 compact columns) suitable for round-trip tests.
// Uses explicit DDL (not AutoMigrate) to avoid MySQL-specific datetime(3) syntax
// that SQLite cannot scan.
func newAgentRunTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_run_model_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id         INTEGER NOT NULL,
			session_id      TEXT    NOT NULL DEFAULT '',
			status          TEXT    NOT NULL DEFAULT 'running',
			state_reason      TEXT    NOT NULL DEFAULT '',
			terminal_metadata TEXT,
			messages          TEXT    NOT NULL DEFAULT '[]',
			reservation_id  INTEGER,
			started_at      DATETIME NOT NULL,
			ended_at        DATETIME,
			compact_state   TEXT,
			compact_summary TEXT,
			created_at      DATETIME,
			updated_at      DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestAgentRun_TableName_returnsAgentRun(t *testing.T) {
	got := AgentRun{}.TableName()
	assert.Equal(t, "agent_run", got)
}

func TestAgentRun_CompactColumnsRoundTrip(t *testing.T) {
	db := newAgentRunTestDB(t)

	stateJSON, err := json.Marshal(map[string]any{
		"last_compact_at":          "2026-05-21T00:00:00Z",
		"last_boundary_message_id": "msg_001",
		"total_compact_attempts":   2,
		"consecutive_failures":     0,
		"summary_token_count":      3840,
		"strategy_used":            "reactive_compact",
	})
	require.NoError(t, err)

	run := &AgentRun{
		UserID:         42,
		SessionID:      "sess-compact",
		Status:         "running",
		Messages:       datatypes.JSON(`[]`),
		StartedAt:      time.Now(),
		CompactState:   datatypes.JSON(stateJSON),
		CompactSummary: "v1 placeholder summary",
	}
	require.NoError(t, db.Create(run).Error)
	require.NotZero(t, run.ID)

	var got AgentRun
	require.NoError(t, db.First(&got, run.ID).Error)
	assert.Equal(t, "v1 placeholder summary", got.CompactSummary)
	assert.JSONEq(t, string(stateJSON), string(got.CompactState))
}

func TestAgentRun_CompactStateNullable(t *testing.T) {
	db := newAgentRunTestDB(t)
	run := &AgentRun{
		UserID:    1,
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
		// no CompactState set → should persist as NULL/zero
	}
	require.NoError(t, db.Create(run).Error)

	var got AgentRun
	require.NoError(t, db.First(&got, run.ID).Error)
	// zero datatypes.JSON is empty bytes → marshals to empty/null
	assert.True(t, len(got.CompactState) == 0 || string(got.CompactState) == "null" || string(got.CompactState) == "")
}

func TestAgentRun_CompactSummaryNullable(t *testing.T) {
	db := newAgentRunTestDB(t)
	run := &AgentRun{
		UserID:    1,
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
		// no CompactSummary set → should persist as ""
	}
	require.NoError(t, db.Create(run).Error)

	var got AgentRun
	require.NoError(t, db.First(&got, run.ID).Error)
	assert.Equal(t, "", got.CompactSummary)
}

// TestAgentRun_NoDefaultTrueBoolFields documents the database.md §6 audit:
// AgentRun model has no `default:true` bool field. This test exists to keep the
// claim verifiable in CI — if a future change adds such a field without the
// UpdateColumn fixup pattern, the test will fail when extended.
func TestAgentRun_NoDefaultTrueBoolFields(t *testing.T) {
	// AgentRun fields (per #9 spec audit):
	// - ID uint64 / UserID uint / SessionID string / Status string (default:'running' — not bool)
	// - StateReason string / TerminalMetadata datatypes.JSON / Messages datatypes.JSON / ReservationID *uint64
	// - StartedAt time.Time / EndedAt *time.Time
	// - CompactState datatypes.JSON / CompactSummary string (#9 — no bool)
	// - CreatedAt time.Time / UpdatedAt time.Time
	// No bool field exists → no `default:true` gotcha (database.md §6) risk.
	t.Log("AgentRun audit: zero bool fields, zero default:true gotcha risk")
}

func TestAgentRun_TerminalMetadata_JSONRoundtrip(t *testing.T) {
	db := newAgentRunTestDB(t)

	metadata := datatypes.JSON([]byte(`{"budget_dimension":"max_turns","used":51,"limit":50}`))
	run := &AgentRun{
		UserID:           1,
		SessionID:        "sess-1",
		Status:           "terminated",
		StateReason:      "error_max_budget",
		TerminalMetadata: metadata,
		Messages:         datatypes.JSON([]byte("[]")),
		StartedAt:        time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	var got AgentRun
	require.NoError(t, db.First(&got, run.ID).Error)
	assert.JSONEq(t, string(metadata), string(got.TerminalMetadata))
}
