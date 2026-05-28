package artifact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB 创建独立的 in-memory SQLite DB 并用 raw DDL 显式创建 4 张表。
//
// 不用 GORM AutoMigrate 的原因：model.Skill.SourceType 用 `type:enum(...)`、
// model.Skill.AllowedTools 用 `default:(JSON_ARRAY())`——SQLite 不支持这两种 MySQL 特性，
// AutoMigrate 会原样写进 CREATE TABLE 失败。参考 internal/pkg/model/credit_reservation_test.go
// 的同类处理（ENUM 退化为 TEXT，JSON 默认空字符串）。
//
// 真实 MySQL DDL 由 migrations/20260526_120000_create_skill_tables.sql 创建，
// 已由 T01 覆盖。
func newTestDB(t *testing.T) *gorm.DB {
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
		// skill — ENUM 退化为 TEXT，JSON_ARRAY() 默认退化为 '[]'
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
			origin_type         TEXT    NOT NULL DEFAULT 'user',
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

		// agent_definition（用于 ListBoundAgents / validateAgentOwnership）—— 退化版
		`CREATE TABLE agent_definition (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id         INTEGER NOT NULL,
			name                   TEXT    NOT NULL,
			description            TEXT,
			icon_url               TEXT,
			welcome_message        TEXT,
			starters               TEXT,
			questionnaire_answers  TEXT,
			generated_skill_body   TEXT,
			advanced_mode          INTEGER NOT NULL DEFAULT 0,
			custom_skill_body      TEXT,
			system_prompt          TEXT,
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
