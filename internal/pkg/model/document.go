package model

import "time"

// Document 是文档系统 v1 的可编辑文档记录（document-system feature）。
//
// 由 agent mode 在对话中生成的文本类产物（markdown/docx/纯文本/HTML），在用户
// 首次「打开编辑」时懒建档：拉取 COS 源对象 → 解析成 markdown → 快照存入 ContentMD。
// 之后的编辑只改 ContentMD，不再依赖原 COS 对象（源对象可能 24h 后过期/被 GC）。
//
// 隔离：仅 features.document_system.enabled 开启时本表才参与 AutoMigrate（见 helper.go），
// prod 默认 flag off → 表不出现，零 schema 影响。
type Document struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 文档归属用户。(user_id, source_object_key) 唯一，保证同一源产物每用户只建一份档。
	UserID uint `gorm:"not null;uniqueIndex:uniq_doc_user_source,priority:1;index:idx_doc_user_updated,priority:1" json:"user_id"`
	// ParentUserID B2B2C 上下文快照，v1 不用于共享/下发。
	ParentUserID *uint `gorm:"column:parent_user_id" json:"parent_user_id,omitempty"`
	// SourceObjectKey COS object key（限 agent-outputs/{userID}/ 前缀），跨预签名 URL 稳定，作打开依据。
	SourceObjectKey string `gorm:"size:512;not null;uniqueIndex:uniq_doc_user_source,priority:2" json:"source_object_key"`
	// SourceRunID 弱关联 agent_run（无 FK，避免耦合）。
	SourceRunID *uint64 `gorm:"column:source_run_id" json:"source_run_id,omitempty"`
	// SourceMime 源文件 MIME（可能为空，解析时用 filename 扩展名兜底）。
	SourceMime string `gorm:"size:128" json:"source_mime,omitempty"`
	// Title 文档标题（默认取去扩展名的源文件名）。
	Title string `gorm:"size:255;not null" json:"title"`
	// ContentMD 可编辑 markdown 正文，上限 2MB。
	ContentMD string `gorm:"type:mediumtext;not null" json:"content_md"`
	// ParseMethod 解析方式：direct | html | markitdown | qwen_long。
	// biz 层 Create 前必须显式赋值——GORM v2 struct-Create 会跳过空字符串零值，
	// 留空将走 DB DEFAULT 'direct' 而非调用方意图（见 .claude/rules/database.md §6b 同理）。
	ParseMethod string    `gorm:"size:32;not null;default:'direct'" json:"parse_method"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime;index:idx_doc_user_updated,priority:2" json:"updated_at"`
}

// TableName 指定表名。
func (Document) TableName() string { return "document" }
