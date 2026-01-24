package domain

import (
	"errors"
	"time"
)

// SalesStage 销售阶段
type SalesStage string

const (
	StageDiscovery   SalesStage = "DISCOVERY"   // 探索/线索
	StageNegotiation SalesStage = "NEGOTIATION" // 谈判/报价
	StageClosing     SalesStage = "CLOSING"     // 缔结/签约
)

// KnowledgeChunk 知识切片（最小检索单位）
type KnowledgeChunk struct {
	ID         string    `json:"id"`
	DocumentID uint      `json:"document_id"` // 关联的文档ID
	UserID     uint      `json:"user_id"`     // 关联的用户ID，用于数据隔离
	Content    string    `json:"content"`
	Vector     []float32 `json:"vector,omitempty"`

	Tags       []string     `json:"tags"`
	SalesStage []SalesStage `json:"sales_stage"` // 核心过滤字段
	SourceRef  string       `json:"source_ref"`  // 例如: "第3页"
}

// Validate 验证切片合法性
func (k *KnowledgeChunk) Validate() error {
	if k.Content == "" {
		return errors.New("content is required")
	}
	if k.DocumentID == 0 {
		return errors.New("document_id is required")
	}
	return nil
}

// DocStatus 文档处理状态
type DocStatus string

const (
	DocStatusPending   DocStatus = "PENDING"
	DocStatusParsing   DocStatus = "PARSING"
	DocStatusEmbedding DocStatus = "EMBEDDING"
	DocStatusCompleted DocStatus = "COMPLETED"
	DocStatusFailed    DocStatus = "FAILED"
)

// KnowledgeDocument 知识库文档实体
type KnowledgeDocument struct {
	ID          uint      `json:"id"`
	UserID      uint      `json:"user_id"`
	Name        string    `json:"name"`
	FilePath    string    `json:"file_path"`
	Status      DocStatus `json:"status"`
	ErrorMsg    string    `json:"error_msg,omitempty"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	ChunkCount  int       `json:"chunk_count"`
	FileSize    int64     `json:"file_size"`
	FileType    string    `json:"file_type"`

	IsEnabled bool      `json:"is_enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate 验证文档合法性
func (d *KnowledgeDocument) Validate() error {
	if d.Name == "" {
		return errors.New("document name is required")
	}
	if d.UserID == 0 {
		return errors.New("user_id is required")
	}
	return nil
}
