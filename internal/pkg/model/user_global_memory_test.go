package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUserGlobalMemory_TableName 验证 UserGlobalMemory.TableName() 返回正确表名。
func TestUserGlobalMemory_TableName(t *testing.T) {
	assert.Equal(t, "user_global_memory", UserGlobalMemory{}.TableName())
}

// TestUserGlobalMemory_CreateTable 验证 SQLiteCreateUserGlobalMemoryDDL 能创建 user_global_memory 表。
// Note: AutoMigrate cannot be used in SQLite tests because the model uses
// `default:CURRENT_TIMESTAMP(3)` for MySQL ms precision (not SQLite-parseable).
func TestUserGlobalMemory_CreateTable(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateUserGlobalMemoryDDL).Error)
	assert.True(t, db.Migrator().HasTable(&UserGlobalMemory{}),
		"table user_global_memory should exist after creating via raw DDL")
}

// TestUserGlobalMemory_Create_ConfidenceZero 验证 GORM default:1.0 zero-value gotcha
// 以及 UpdateColumn fixup 两步法能将 Confidence=0.0 正确持久化。
//
// 背景（database.md §6）：
//   - UserGlobalMemory.Confidence 的 gorm tag 含 "default:1.0"。
//   - GORM v2 在 Create 时对 float64 零值（0.0）视为"未设置"，INSERT 使用 DB DEFAULT(1.0)。
//   - 修复方案（与 bool gotcha 同原理）：Create 后检测 wantConfidence != actual，
//     若被覆盖则 UpdateColumn("confidence", 0) 强制写入。
//   - 生产路径（MySQL）中 store 层 Upsert 使用 Select("*") 强制所有列入 INSERT，
//     可直接避免此 gotcha；SQLite 单测中需用 UpdateColumn fixup 验证。
//   - biz 层 notepad.Write：opts.Confidence==nil 时默认 1.0；传 *float64=0.0 时存 0.0（P2-2）。
func TestUserGlobalMemory_Create_ConfidenceZero(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateUserGlobalMemoryDDL).Error)

	m := &UserGlobalMemory{
		UserID:     1,
		Kind:       "fact",
		KeyName:    "test-key",
		Value:      "test value",
		Confidence: 0.0, // 显式零值，agent 极低置信度（合法业务值）
		SourceType: "agent_tool",
	}

	// 步骤 1：捕获 caller 意图
	wantConfidence := m.Confidence

	// 步骤 2：Create — GORM default:1.0 在 SQLite 会覆盖零值
	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)
	require.NotZero(t, m.ID)

	// 步骤 3：显式验证 GORM gotcha 真的触发了（P1-1：避免 SQLite 行为变化时静默通过）
	// 生产 MySQL 路径：store 层 Upsert 用 Select("*").Create() 直接规避，不需要此 fixup
	assert.NotEqual(t, wantConfidence, m.Confidence,
		"GORM gotcha expected: Confidence should have been overwritten by default:1.0 (if assertion fails, GORM/SQLite no longer triggers gotcha — revisit fixup design)")
	// UpdateColumn fixup 两步法（database.md §6 同款模式）
	require.NoError(t, db.Model(m).UpdateColumn("confidence", wantConfidence).Error)
	m.Confidence = wantConfidence

	assert.Equal(t, 0.0, m.Confidence, "struct.Confidence should be 0.0 after fixup")

	// 步骤 4：从 DB 读回，验证持久化正确
	var row UserGlobalMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.Equal(t, 0.0, row.Confidence, "confidence=0.0 should persist to DB after UpdateColumn fixup")
}

// TestUserGlobalMemory_Create_ConfidenceNonZero 验证 Confidence=0.85 正常写入（对照测试）。
func TestUserGlobalMemory_Create_ConfidenceNonZero(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateUserGlobalMemoryDDL).Error)

	m := &UserGlobalMemory{
		UserID:     2,
		Kind:       "preference",
		KeyName:    "pref-key",
		Value:      "non-zero confidence test",
		Confidence: 0.85,
		SourceType: "agent",
	}

	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)

	var row UserGlobalMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.InDelta(t, 0.85, row.Confidence, 1e-6, "confidence=0.85 should persist correctly without any fixup")
}

// TestUserGlobalMemory_UniqueKey_userAndKeyName 验证 (user_id, key_name) UNIQUE 约束。
// 相同 user_id + key_name 重复插入应报错。
func TestUserGlobalMemory_UniqueKey_userAndKeyName(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateUserGlobalMemoryDDL).Error)

	m1 := &UserGlobalMemory{
		UserID:     10,
		Kind:       "fact",
		KeyName:    "dup-key",
		Value:      "first value",
		Confidence: 1.0,
		SourceType: "agent_tool",
	}
	require.NoError(t, db.Create(m1).Error)

	m2 := &UserGlobalMemory{
		UserID:     10,
		Kind:       "fact",
		KeyName:    "dup-key", // 相同 user_id + key_name
		Value:      "second value",
		Confidence: 1.0,
		SourceType: "agent_tool",
	}
	err := db.Create(m2).Error
	require.Error(t, err, "duplicate (user_id, key_name) must fail with UNIQUE constraint")
}

// TestUserGlobalMemory_Create_NullableFields 验证 SourceAgentDefinitionID=nil
// 写入后仍为 NULL。
func TestUserGlobalMemory_Create_NullableFields(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.Exec(SQLiteCreateUserGlobalMemoryDDL).Error)

	m := &UserGlobalMemory{
		UserID:                  20,
		Kind:                    "learning",
		KeyName:                 "nullable-test",
		Value:                   "test",
		Confidence:              1.0,
		SourceType:              "user_explicit",
		SourceAgentDefinitionID: nil,
	}

	require.NoError(t, db.WithContext(context.Background()).Create(m).Error)

	var row UserGlobalMemory
	require.NoError(t, db.First(&row, m.ID).Error)
	assert.Nil(t, row.SourceAgentDefinitionID, "source_agent_definition_id should be NULL")
}
