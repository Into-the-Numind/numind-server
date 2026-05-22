package model

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentSessionMemory_TableName 验证 AgentSessionMemory.TableName() 返回正确表名。
func TestAgentSessionMemory_TableName(t *testing.T) {
	assert.Equal(t, "agent_session_memory", AgentSessionMemory{}.TableName())
}

// TestAgentSessionMemory_CreateTable 验证 SQLiteCreateAgentSessionMemoryDDL 能创建表。
// Note: AutoMigrate can't be used in SQLite tests due to `default:CURRENT_TIMESTAMP(3)`.
func TestAgentSessionMemory_CreateTable(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateAgentSessionMemoryDDL).Error)
	assert.True(t, db.Migrator().HasTable(&AgentSessionMemory{}),
		"table agent_session_memory should exist after creating via raw DDL")
}

// TestAgentSessionMemory_Create_ScoreZero 验证 GORM default:1.0 zero-value gotcha
// 以及 UpdateColumn fixup 两步法能将 Score=0.0 正确持久化。
//
// 背景（database.md §6）：
//   - AgentSessionMemory.Score 的 gorm tag 含 "default:1.0"。
//   - GORM v2 在 Create 时对 float64 零值（0.0）视为"未设置"，INSERT 使用 DB DEFAULT(1.0)。
//   - 修复方案（与 bool gotcha 同原理）：Create 后检测 wantScore != actual，
//     若被覆盖则 UpdateColumn("score", 0) 强制写入。
//   - 生产路径（MySQL）中 store 层使用 Select("*").Create() 强制所有列入 INSERT，
//     可直接避免此 gotcha；SQLite 单测中需用 UpdateColumn fixup 验证。
func TestAgentSessionMemory_Create_ScoreZero(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateAgentSessionMemoryDDL).Error)

	now := time.Now()
	m := &AgentSessionMemory{
		UserID:            1,
		AgentDefinitionID: 100,
		Kind:              "fact",
		Content:           "test content",
		Score:             0.0, // 显式零值
		SourceType:        "agent",
		RecencyAt:         now,
	}

	// 步骤 1：捕获 caller 意图
	wantScore := m.Score

	// 步骤 2：Create — GORM default:1.0 在 SQLite 会覆盖零值
	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)
	require.NotZero(t, m.ID)

	// 步骤 3：显式验证 GORM gotcha 真的触发了（P1-1：避免 SQLite 行为变化时静默通过）
	// 生产 MySQL 路径：store 层用 Select("*").Create() 直接规避，不需要此 fixup
	assert.NotEqual(t, wantScore, m.Score,
		"GORM gotcha expected: Score should have been overwritten by default:1.0 (if assertion fails, GORM/SQLite no longer triggers gotcha — revisit fixup design)")
	// UpdateColumn fixup 两步法（database.md §6 同款模式）
	require.NoError(t, db.Model(m).UpdateColumn("score", wantScore).Error)
	m.Score = wantScore

	assert.Equal(t, 0.0, m.Score, "struct.Score should be 0.0 after fixup")

	// 步骤 4：从 DB 读回，验证持久化正确
	var row AgentSessionMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.Equal(t, 0.0, row.Score, "score=0.0 should persist to DB after UpdateColumn fixup")
}

// TestAgentSessionMemory_Create_ScoreNonZero 验证 Score=0.75 正常写入（对照测试）。
func TestAgentSessionMemory_Create_ScoreNonZero(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateAgentSessionMemoryDDL).Error)

	now := time.Now()
	m := &AgentSessionMemory{
		UserID:            2,
		AgentDefinitionID: 200,
		Kind:              "learning",
		Content:           "non-zero score test",
		Score:             0.75,
		SourceType:        "agent_tool",
		RecencyAt:         now,
	}

	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)

	var row AgentSessionMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.InDelta(t, 0.75, row.Score, 1e-6, "score=0.75 should persist correctly without any fixup")
}

// TestAgentSessionMemory_Create_NullableFields 验证 ExpiresAt=nil /
// SourceAgentDefinitionID=nil / Embedding=nil 写入后仍为 NULL。
func TestAgentSessionMemory_Create_NullableFields(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateAgentSessionMemoryDDL).Error)

	now := time.Now()
	m := &AgentSessionMemory{
		UserID:                  3,
		AgentDefinitionID:       300,
		Kind:                    "preference",
		Content:                 "nullable fields test",
		Score:                   1.0,
		SourceType:              "user_explicit",
		RecencyAt:               now,
		ExpiresAt:               nil,
		SourceAgentDefinitionID: nil,
		Embedding:               nil,
	}

	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)

	var row AgentSessionMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.Nil(t, row.ExpiresAt, "expires_at should be NULL")
	assert.Nil(t, row.SourceAgentDefinitionID, "source_agent_definition_id should be NULL")
	assert.Nil(t, row.Embedding, "embedding should be NULL")
}
