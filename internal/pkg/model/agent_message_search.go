package model

import "time"

// AgentMessageSearch 是 Agent Mode V1.5 Task 3.5 的中文 FULLTEXT 搜索索引行。
//
// 一条 agent_run.messages 数组元素对应 search 表的一行。FULLTEXT INDEX
// ft_content (content) WITH PARSER ngram 由 migration 手工建立 — GORM tag 不支
// FULLTEXT，AutoMigrate 不会重建。生产环境必须跑过对应 SQL migration。
//
// D9 决策：MySQL 8 FULLTEXT + ngram parser (n=2)；量大再升 Elasticsearch。
//
// 写入容忍：search 表是衍生数据，BulkInsert 失败仅 log warn 不阻塞 agent run。
// 跨 user 严格隔离：所有查询 SQL 强制 WHERE user_id = ?。
type AgentMessageSearch struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentRunID    uint64    `gorm:"not null;index:idx_run" json:"agent_run_id"`
	UserID        uint      `gorm:"not null;index:idx_user_recency" json:"user_id"`
	SessionID     string    `gorm:"size:64;not null;index:idx_session" json:"session_id"`
	MessageUUID   string    `gorm:"size:64;not null;uniqueIndex:uniq_msg_uuid" json:"message_uuid"`
	Role          string    `gorm:"size:32;not null" json:"role"`
	Content       string    `gorm:"type:text;not null" json:"content"`
	ContentLength int       `gorm:"not null" json:"content_length"`
	MessageIndex  int       `gorm:"not null" json:"message_index"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName overrides GORM's default plural naming.
func (AgentMessageSearch) TableName() string { return "agent_message_search" }
