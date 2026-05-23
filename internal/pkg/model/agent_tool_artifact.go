package model

import "time"

// AgentToolArtifact 记录 V2 上下文管理大 tool result 写盘后的元数据。
// Agent Mode V1.5 板块 2 Task 2.1 — V2 专用表（与 V1 包完全隔离）。
//
// 设计要点：
//   - 文件内容写到 <data_dir>/agent_artifacts/<run_id>/<artifact_uuid>，DB 仅存路径 + 前 1KB 预览
//   - file_path 存相对路径 "<run_id>/<artifact_uuid>"，storage 层拼绝对路径（搬机器友好）
//   - storage_backend 预留 "local"/"cos"，phase 1 默认 local
//   - is_expired + expires_at 配合 task 2.2 cleanup cron 使用
//   - idx_ata_run_tool_call (agent_run_id, tool_call_id) 支持 GetByToolCallID 命中
//   - idx_ata_expires (expires_at, is_expired) 支持 cleanup cron 扫表
type AgentToolArtifact struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UUID           string     `gorm:"column:uuid;size:64;uniqueIndex;not null" json:"uuid"`
	AgentRunID     uint64     `gorm:"column:agent_run_id;not null;index:idx_ata_run_tool_call,priority:1" json:"agent_run_id"`
	ToolCallID     string     `gorm:"column:tool_call_id;size:128;not null;index:idx_ata_run_tool_call,priority:2" json:"tool_call_id"`
	ToolName       string     `gorm:"column:tool_name;size:64;not null" json:"tool_name"`
	MimeType       *string    `gorm:"column:mime_type;size:64" json:"mime_type,omitempty"`
	SizeBytes      int64      `gorm:"column:size_bytes;not null" json:"size_bytes"`
	FilePath       string     `gorm:"column:file_path;type:text;not null" json:"file_path"`
	StorageBackend string     `gorm:"column:storage_backend;size:16;not null;default:'local'" json:"storage_backend"`
	Preview        *string    `gorm:"column:preview;type:text" json:"preview,omitempty"`
	IsExpired      bool       `gorm:"column:is_expired;not null;default:false;index:idx_ata_expires,priority:2" json:"is_expired"`
	ExpiresAt      *time.Time `gorm:"column:expires_at;type:datetime(3);index:idx_ata_expires,priority:1" json:"expires_at,omitempty"`
	CreatedAt      time.Time  `gorm:"column:created_at;type:datetime(3);autoCreateTime" json:"created_at"`
}

// TableName 显式指定表名（项目约定）。
func (AgentToolArtifact) TableName() string { return "agent_tool_artifact" }
