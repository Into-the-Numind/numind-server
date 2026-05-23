package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newMigrationTestDB 复制自 internal/numind/biz/skill/artifact/testhelper_test.go 的 SQLite
// DDL（MySQL ENUM / JSON 默认值在 SQLite 上不支持），保证 RunMigration 单测可在 SQLite 跑。
// 与 internal/pkg/model/credit_reservation_test.go 同模式。
func newMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ddls := []string{
		`CREATE TABLE skill (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id      INTEGER NOT NULL,
			name                TEXT    NOT NULL,
			description         TEXT    NOT NULL DEFAULT '',
			when_to_use         TEXT    NOT NULL DEFAULT '',
			allowed_tools       TEXT    NOT NULL DEFAULT '[]',
			body_md             TEXT    NOT NULL DEFAULT '',
			source_type         TEXT    NOT NULL DEFAULT 'custom',
			source_template_id  INTEGER,
			version             INTEGER NOT NULL DEFAULT 1,
			is_active           INTEGER NOT NULL DEFAULT 1,
			created_by          INTEGER NOT NULL,
			created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_skill_parent_active ON skill (parent_user_id, is_active, updated_at)`,
		`CREATE INDEX idx_skill_source_template ON skill (source_template_id)`,

		`CREATE TABLE skill_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			skill_id    INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			snapshot    TEXT    NOT NULL,
			created_by  INTEGER NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE UNIQUE INDEX uk_skill_version ON skill_history (skill_id, version)`,
		`CREATE INDEX idx_history_skill_created ON skill_history (skill_id, created_at)`,

		`CREATE TABLE agent_skill_binding (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id    INTEGER NOT NULL,
			skill_id    INTEGER NOT NULL,
			sort_order  INTEGER NOT NULL DEFAULT 0,
			is_active   INTEGER NOT NULL DEFAULT 1,
			bound_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			unbound_at  DATETIME
		)`,
		`CREATE UNIQUE INDEX uk_agent_skill ON agent_skill_binding (agent_id, skill_id)`,
		`CREATE INDEX idx_binding_agent_active_sort ON agent_skill_binding (agent_id, is_active, sort_order)`,
		`CREATE INDEX idx_binding_skill_active ON agent_skill_binding (skill_id, is_active)`,

		`CREATE TABLE agent_definition (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id         INTEGER NOT NULL,
			name                   TEXT    NOT NULL,
			description            TEXT    NOT NULL DEFAULT '',
			icon_url               TEXT    NOT NULL DEFAULT '',
			welcome_message        TEXT    NOT NULL DEFAULT '',
			starters               TEXT,
			questionnaire_answers  TEXT,
			generated_skill_body   TEXT    NOT NULL DEFAULT '',
			advanced_mode          INTEGER NOT NULL DEFAULT 0,
			custom_skill_body      TEXT    NOT NULL DEFAULT '',
			tool_flags             TEXT,
			credit_cap_per_session INTEGER,
			daily_credit_cap       INTEGER,
			version                INTEGER NOT NULL DEFAULT 1,
			is_active              INTEGER NOT NULL DEFAULT 1,
			source_template_id     INTEGER,
			created_by             INTEGER NOT NULL,
			created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, ddl := range ddls {
		require.NoError(t, db.Exec(ddl).Error, "create table")
	}
	return db
}

// seedAgents 插入 3 行 agent_definition：
//   - id=1: advanced_mode=0 → source_type 应为 generated
//   - id=2: advanced_mode=1 → source_type 应为 custom
//   - id=3: advanced_mode=0 → source_type 应为 generated（验证 batch 多行）
//
// 所有 agent 均 is_active=1。
func seedAgents(t *testing.T, db *gorm.DB) {
	t.Helper()
	rows := []struct {
		id           int
		parentUserID int
		name         string
		advanced     int
		genBody      string
		customBody   string
		toolFlags    string
		createdBy    int
	}{
		{1, 100, "agent-A", 0, "# generated A body", "", `["bash"]`, 100},
		{2, 100, "agent-B", 1, "", "# custom B body", `["read","write"]`, 100},
		{3, 200, "agent-C", 0, "# generated C body", "", "", 200},
	}
	for _, r := range rows {
		require.NoError(t, db.Exec(`
			INSERT INTO agent_definition
			(id, parent_user_id, name, advanced_mode, generated_skill_body, custom_skill_body, tool_flags, is_active, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			r.id, r.parentUserID, r.name, r.advanced, r.genBody, r.customBody, r.toolFlags, r.createdBy).Error)
	}
}

// TestRunMigration_DryRun 验证 dry-run 不写入但报正确预计数。
func TestRunMigration_DryRun(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)

	stats, err := RunMigration(context.Background(), db, true, 100)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.WouldMigrate)
	assert.Equal(t, 0, stats.Migrated)

	// 验证未写入
	var skillCount, bindingCount, historyCount int64
	db.Model(&model.Skill{}).Count(&skillCount)
	db.Model(&model.AgentSkillBinding{}).Count(&bindingCount)
	db.Model(&model.SkillHistory{}).Count(&historyCount)
	assert.Equal(t, int64(0), skillCount)
	assert.Equal(t, int64(0), bindingCount)
	assert.Equal(t, int64(0), historyCount)
}

// TestRunMigration_Success 验证实际迁移：3 skill + 3 binding + 3 history + 正确 source_type。
func TestRunMigration_Success(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)

	stats, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Migrated)
	assert.Equal(t, 0, stats.WouldMigrate)

	// 验证 3 skill
	var skills []model.Skill
	require.NoError(t, db.Order("parent_user_id, name").Find(&skills).Error)
	require.Len(t, skills, 3)

	// agent-A (advanced=0) → generated + GeneratedSkillBody
	assert.Equal(t, "agent-A 的默认技能", skills[0].Name)
	assert.Equal(t, "generated", skills[0].SourceType)
	assert.Equal(t, "# generated A body", skills[0].BodyMd)
	assert.Equal(t, uint(100), skills[0].ParentUserID)
	assert.True(t, skills[0].IsActive, "is_active 必须为 true（default:true bool 陷阱）")
	assert.Equal(t, uint(migrationSkillVersion), skills[0].Version)
	assert.Equal(t, migrationSkillWhenToUse, skills[0].WhenToUse)

	// agent-B (advanced=1) → custom + CustomSkillBody
	assert.Equal(t, "agent-B 的默认技能", skills[1].Name)
	assert.Equal(t, "custom", skills[1].SourceType)
	assert.Equal(t, "# custom B body", skills[1].BodyMd)

	// agent-C (parent=200, advanced=0) → generated
	assert.Equal(t, "agent-C 的默认技能", skills[2].Name)
	assert.Equal(t, "generated", skills[2].SourceType)
	assert.Equal(t, uint(200), skills[2].ParentUserID)

	// 验证 3 binding（is_active=1）
	var bindings []model.AgentSkillBinding
	require.NoError(t, db.Order("agent_id").Find(&bindings).Error)
	require.Len(t, bindings, 3)
	for i, b := range bindings {
		assert.Equal(t, uint(i+1), b.AgentID, "binding.agent_id 必须等于源 ad.id")
		assert.True(t, b.IsActive, "binding.is_active 必须为 true")
		assert.Equal(t, int16(0), b.SortOrder)
	}

	// 验证 3 history v1
	var histories []model.SkillHistory
	require.NoError(t, db.Order("skill_id").Find(&histories).Error)
	require.Len(t, histories, 3)
	for _, h := range histories {
		assert.Equal(t, uint(migrationSkillVersion), h.Version)
		assert.NotEmpty(t, h.Snapshot, "snapshot 不能为空")
		// snapshot 是 valid JSON
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal(h.Snapshot, &decoded))
		assert.NotEmpty(t, decoded["name"], "snapshot 必须包含 name")
	}
}

// TestRunMigration_Idempotent 重复跑同一迁移不应重复创建 skill / binding（LEFT JOIN 过滤生效）。
func TestRunMigration_Idempotent(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)

	stats1, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)
	require.Equal(t, 3, stats1.Migrated)

	// 第二次跑
	stats2, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)
	assert.Equal(t, 0, stats2.Migrated, "重复执行不应再次迁移")

	var skillCount, bindingCount int64
	db.Model(&model.Skill{}).Count(&skillCount)
	db.Model(&model.AgentSkillBinding{}).Count(&bindingCount)
	assert.Equal(t, int64(3), skillCount)
	assert.Equal(t, int64(3), bindingCount)
}

// TestRunMigration_BatchSize 验证 batch_size 较小时多批次仍正确处理。
func TestRunMigration_BatchSize(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)

	stats, err := RunMigration(context.Background(), db, false, 2) // 2 行/批 → 2 批
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Migrated)

	var bindingCount int64
	db.Model(&model.AgentSkillBinding{}).Count(&bindingCount)
	assert.Equal(t, int64(3), bindingCount)
}

// TestRunRollback_DeletesMigratedBindings 验证 rollback 删 3 binding 保 3 skill。
func TestRunRollback_DeletesMigratedBindings(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)

	_, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)

	stats, err := RunRollback(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Rolled)

	var skillCount, bindingCount int64
	db.Model(&model.Skill{}).Count(&skillCount)
	db.Model(&model.AgentSkillBinding{}).Count(&bindingCount)
	assert.Equal(t, int64(3), skillCount, "skill 行应保留作审计")
	assert.Equal(t, int64(0), bindingCount, "binding 应全部硬删")
}

// TestRunRollback_SkipsUserCreatedSkill 验证 rollback 不会误删用户自建的同名 skill
// （version > 1，即被编辑过的 skill）。
func TestRunRollback_SkipsUserCreatedSkill(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)
	_, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)

	// 模拟用户编辑过 skill 1（升级到 v2）
	require.NoError(t, db.Exec(`UPDATE skill SET version = 2 WHERE id = 1`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO skill_history (skill_id, version, snapshot, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		1, 2, `{"name":"agent-A 的默认技能"}`, 100, time.Now()).Error)

	stats, err := RunRollback(context.Background(), db)
	require.NoError(t, err)
	// skill 1 被编辑过 → 不在 rollback 名单（只匹配 NOT EXISTS history.version > 1）
	// 因此只删 skill 2 + 3 的 binding
	assert.Equal(t, 2, stats.Rolled)

	var bindingCount int64
	db.Model(&model.AgentSkillBinding{}).Where("agent_id = ?", 1).Count(&bindingCount)
	assert.Equal(t, int64(1), bindingCount, "已被编辑的 skill 1 的 binding 应被保留")
}

// TestRunMigration_InactiveAgentsSkipped 验证 is_active=0 的 agent 不迁移。
func TestRunMigration_InactiveAgentsSkipped(t *testing.T) {
	db := newMigrationTestDB(t)
	seedAgents(t, db)
	// 把 agent 1 设置为 inactive
	require.NoError(t, db.Exec("UPDATE agent_definition SET is_active = 0 WHERE id = 1").Error)

	stats, err := RunMigration(context.Background(), db, false, 100)
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Migrated, "inactive agent 应被跳过")

	var bindings []model.AgentSkillBinding
	require.NoError(t, db.Order("agent_id").Find(&bindings).Error)
	require.Len(t, bindings, 2)
	assert.Equal(t, uint(2), bindings[0].AgentID)
	assert.Equal(t, uint(3), bindings[1].AgentID)
}
