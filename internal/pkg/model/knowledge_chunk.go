package model

import (
	"gorm.io/gorm"
)

// KnowledgeChunk 知识切片MySQL存储模型
// 用于在MySQL中持久化存储切片数据，避免每次查询向量数据库产生费用
type KnowledgeChunk struct {
	gorm.Model
	DocumentID      uint   `gorm:"index;index:idx_user_doc;not null" json:"document_id"`
	UserID          uint   `gorm:"index;index:idx_user_doc;not null" json:"user_id"`
	Sequence        int    `gorm:"not null" json:"sequence"`                          // 切片在文档中的顺序
	Content         string `gorm:"type:longtext;not null" json:"content"`             // 切片内容
	Summary         string `gorm:"type:text" json:"summary"`                          // AI生成的摘要
	SourceRef       string `gorm:"size:255" json:"source_ref"`                        // 来源引用（如页码）
	Tags            string `gorm:"size:512" json:"tags"`                              // 标签（逗号分隔）
	VectorID        string `gorm:"size:255;index" json:"vector_id"`                   // 向量数据库中的ID
	EmbeddingStatus string `gorm:"size:20;default:'PENDING'" json:"embedding_status"` // PENDING, COMPLETED, FAILED
	Metadata        string `gorm:"type:text" json:"metadata"`                         // JSON格式的额外元数据
}

// TableName 指定表名
func (KnowledgeChunk) TableName() string {
	return "knowledge_chunk"
}
