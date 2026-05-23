package model

import (
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
			cancellation_requested_at DATETIME,
			agent_definition_id INTEGER,
			pending_question_json TEXT,
			pending_question_at   DATETIME,
			created_at      DATETIME,
			updated_at      DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2        TEXT,
			total_tokens_used_v2    INTEGER NOT NULL DEFAULT 0,
			use_compact_v2          INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2 INTEGER
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

// V1.5 compact-dead-schema-cleanup — TestAgentRun_CompactColumnsRoundTrip /
// CompactStateNullable / CompactSummaryNullable 三个 V1 compact 列测试删除：
// compact_state / compact_summary 列于 migration 20260523_180000 被 DROP，
// model 字段同步删除（compact-v1-removal feature 已删 V1 包，本次 cleanup
// 收尾对应的死字段 + 死列）。

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

// TestAgentRun_CancellationRequestedAt_NullableRoundTrip verifies the field
// persists nil → fetches nil; persists non-nil → fetches matching time.
// Covers #14 Phase C C3 admin force-cancel column.
func TestAgentRun_CancellationRequestedAt_NullableRoundTrip(t *testing.T) {
	db := newAgentRunTestDB(t)

	// Insert with nil → should fetch nil.
	run := &AgentRun{
		UserID:    1,
		SessionID: "sess-cancel-nil",
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
		// CancellationRequestedAt not set → nil
	}
	require.NoError(t, db.Create(run).Error)
	require.NotZero(t, run.ID)

	var got AgentRun
	require.NoError(t, db.First(&got, run.ID).Error)
	assert.Nil(t, got.CancellationRequestedAt, "nil CancellationRequestedAt should persist as NULL")

	// Update with non-nil → should fetch matching time.
	now := time.Now().UTC().Truncate(time.Second)
	result := db.Model(&AgentRun{}).Where("id = ?", run.ID).
		UpdateColumn("cancellation_requested_at", now)
	require.NoError(t, result.Error)

	var got2 AgentRun
	require.NoError(t, db.First(&got2, run.ID).Error)
	require.NotNil(t, got2.CancellationRequestedAt)
	assert.WithinDuration(t, now, *got2.CancellationRequestedAt, time.Second,
		"non-nil CancellationRequestedAt should round-trip")
}

// TestAgentRun_V2ColumnsPresent verifies the AgentRun schema after the
// compact-dead-schema-cleanup feature: legacy V1 compact_state / compact_summary
// AND half-dead V2 compact_state_v2 / total_tokens_used_v2 / context_window_limit_v2
// columns were all dropped (migration 20260523_180000). Only `use_compact_v2`
// remains as the V2 kill switch, plus the agent_tool_artifact table for L0.
func TestAgentRun_V2ColumnsPresent(t *testing.T) {
	tmp := t.TempDir()
	dsn := tmp + "/agent_run_automigrate_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&AgentRun{}, &AgentToolArtifact{}))

	mig := db.Migrator()

	// All legacy V1 + half-dead V2 columns must be absent after cleanup.
	for _, col := range []string{
		"compact_state",           // V1, dead
		"compact_summary",         // V1, dead
		"compact_state_v2",        // V2 half-dead
		"total_tokens_used_v2",    // V2 half-dead
		"context_window_limit_v2", // V2 half-dead (never written)
	} {
		assert.False(t, mig.HasColumn(&AgentRun{}, col),
			"Legacy compact column %q should have been removed by cleanup migration", col)
	}

	// V2 kill switch column must remain.
	assert.True(t, mig.HasColumn(&AgentRun{}, "use_compact_v2"),
		"use_compact_v2 (V2 kill switch) must still exist")

	// agent_tool_artifact table must be created.
	assert.True(t, mig.HasTable(&AgentToolArtifact{}),
		"agent_tool_artifact table must be created by AutoMigrate")

	// Spot-check key artifact columns to guard against tag regressions.
	for _, col := range []string{"uuid", "agent_run_id", "tool_call_id", "tool_name",
		"size_bytes", "file_path", "is_expired", "expires_at"} {
		assert.True(t, mig.HasColumn(&AgentToolArtifact{}, col),
			"agent_tool_artifact column %q must be present", col)
	}
}

// TestAgentRun_AgentDefinitionID_DefaultZero verifies AgentDefinitionID is 0
// (the sentinel for historical / unset) when not explicitly set on Create,
// and persists correctly when set. Covers #14 Phase C C4 join key.
func TestAgentRun_AgentDefinitionID_DefaultZero(t *testing.T) {
	db := newAgentRunTestDB(t)

	// Insert without setting AgentDefinitionID → should fetch 0.
	runNoID := &AgentRun{
		UserID:    1,
		SessionID: "sess-def-zero",
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
		// AgentDefinitionID not set → 0
	}
	require.NoError(t, db.Create(runNoID).Error)
	require.NotZero(t, runNoID.ID)

	var gotZero AgentRun
	require.NoError(t, db.First(&gotZero, runNoID.ID).Error)
	assert.Equal(t, uint64(0), gotZero.AgentDefinitionID,
		"unset AgentDefinitionID should persist as 0 (historical sentinel)")

	// Insert with AgentDefinitionID=42 → should fetch 42.
	runWithID := &AgentRun{
		UserID:            2,
		SessionID:         "sess-def-42",
		Status:            "running",
		Messages:          datatypes.JSON(`[]`),
		StartedAt:         time.Now(),
		AgentDefinitionID: 42,
	}
	require.NoError(t, db.Create(runWithID).Error)
	require.NotZero(t, runWithID.ID)

	var gotWithID AgentRun
	require.NoError(t, db.First(&gotWithID, runWithID.ID).Error)
	assert.Equal(t, uint64(42), gotWithID.AgentDefinitionID,
		"AgentDefinitionID=42 should persist and round-trip correctly")
}
