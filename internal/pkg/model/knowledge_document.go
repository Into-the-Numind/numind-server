package model

import (
	"gorm.io/gorm"
)

// KnowledgeDocument 销售 RAG 知识库文档
type KnowledgeDocument struct {
	gorm.Model
	UserID      uint   `gorm:"index;not null" json:"user_id"`
	Name        string `gorm:"size:255;not null" json:"name"`
	FilePath    string `gorm:"size:512" json:"file_path"`
	Status      string `gorm:"size:20;not null;default:'PENDING'" json:"status"` // PENDING, PARSING, EMBEDDING, COMPLETED, FAILED
	ErrorMsg    string `gorm:"type:text" json:"error_msg"`
	Description string `gorm:"size:1024" json:"description"`
	Tags        string `gorm:"size:512" json:"tags"` // JSON array string
	ChunkCount  int    `gorm:"default:0" json:"chunk_count"`
	FileSize    int64  `gorm:"default:0" json:"file_size"`
	FileType    string `gorm:"size:20" json:"file_type"`

	IsEnabled bool `gorm:"default:true;index" json:"is_enabled"`
}

func (KnowledgeDocument) TableName() string {
	return "knowledge_document"
}
