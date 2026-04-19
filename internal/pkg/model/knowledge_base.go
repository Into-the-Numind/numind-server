package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	KBStatusActive   = "active"
	KBStatusArchived = "archived"
)

// KnowledgeBase 知识库（文档分组抽象）
type KnowledgeBase struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"deleted_at"`
	UserID      uint           `gorm:"not null;index:idx_kb_user_id" json:"user_id"`
	Name        string         `gorm:"size:100;not null" json:"name"`
	Description string         `gorm:"size:1024" json:"description"`
	Status      string         `gorm:"size:20;not null;default:'active'" json:"status"`
}

// TableName returns the table name for KnowledgeBase.
func (KnowledgeBase) TableName() string { return "knowledge_base" }

// KnowledgeBaseDocument 知识库-文档关联（硬删除）
type KnowledgeBaseDocument struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	KnowledgeBaseID uint      `gorm:"not null;uniqueIndex:idx_kbd_kb_doc" json:"knowledge_base_id"`
	DocumentID      uint      `gorm:"not null;uniqueIndex:idx_kbd_kb_doc" json:"document_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// TableName returns the table name for KnowledgeBaseDocument.
func (KnowledgeBaseDocument) TableName() string { return "knowledge_base_document" }
