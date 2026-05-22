package model

import "time"

// UserGlobalMemory 是 L2 长期记忆（跨 agent 全局 Notepad）。
// confidence 带 gorm:"default:1.0"（float zero-value gotcha）；
// Create/Upsert 路径必须用 Select("*").Create(&m) 强制所有列入 INSERT。
type UserGlobalMemory struct {
	ID                      uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                  uint      `gorm:"not null;uniqueIndex:uq_ugm_user_key,priority:1;index:idx_ugm_user_kind,priority:1" json:"user_id"`
	Kind                    string    `gorm:"size:20;not null;index:idx_ugm_user_kind,priority:2" json:"kind"`
	KeyName                 string    `gorm:"size:100;not null;column:key_name;uniqueIndex:uq_ugm_user_key,priority:2" json:"key_name"`
	Value                   string    `gorm:"type:text;not null" json:"value"`
	Confidence              float64   `gorm:"not null;default:1.0" json:"confidence"`
	SourceType              string    `gorm:"size:20;not null;default:'agent_tool'" json:"source_type"`
	SourceAgentDefinitionID *uint64   `gorm:"column:source_agent_definition_id" json:"source_agent_definition_id,omitempty"`
	CreatedAt               time.Time `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"created_at"`
	UpdatedAt               time.Time `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"updated_at"`
}

func (UserGlobalMemory) TableName() string { return "user_global_memory" }

// SQLiteCreateUserGlobalMemoryDDL is a SQLite-compatible CREATE TABLE statement
// for unit tests. The production model uses gorm tag `default:CURRENT_TIMESTAMP(3)`
// for millisecond precision on MySQL, which SQLite parser rejects with
// `near "(": syntax error`. Tests that need an in-memory SQLite UserGlobalMemory
// table should call db.Exec(SQLiteCreateUserGlobalMemoryDDL) instead of AutoMigrate.
const SQLiteCreateUserGlobalMemoryDDL = `
CREATE TABLE IF NOT EXISTS user_global_memory (
	id                         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id                    INTEGER NOT NULL,
	kind                       TEXT    NOT NULL,
	key_name                   TEXT    NOT NULL,
	value                      TEXT    NOT NULL,
	confidence                 REAL    NOT NULL DEFAULT 1.0,
	source_type                TEXT    NOT NULL DEFAULT 'agent_tool',
	source_agent_definition_id INTEGER,
	created_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (user_id, key_name)
)`
