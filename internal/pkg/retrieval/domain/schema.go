package domain

import (
	"errors"
	"time"
)

// KnowledgeChunk 知识切片（最小检索单位）
type KnowledgeChunk struct {
	ID         string    `json:"id"`
	DocumentID uint      `json:"document_id"` // 关联的文档ID
	UserID     uint      `json:"user_id"`     // 关联的用户ID，用于数据隔离
	Content    string    `json:"content"`
	Vector     []float32 `json:"vector,omitempty"`

	// EmbedText 是用于生成向量的文本（瞬态，不持久化到任何 store 的元数据列）。
	// 结构感知切块器在此写入"标题面包屑 + 正文"，使向量带上文档/章节上下文，
	// 同时 Content 保持干净（返回给 LLM / 展示时不含面包屑）。
	// 为空时各 store 回退用 Content 生成向量（老调用方零回归）。见 TextForEmbedding。
	EmbedText string `json:"-"`

	Tags      []string `json:"tags"`
	Summary   string   `json:"summary"`
	SourceRef string   `json:"source_ref"` // 例如: "第3页"

	// 检索时填充的字段
	Score        float32 `json:"score,omitempty"`         // 检索匹配度 (0-1)
	DocumentName string  `json:"document_name,omitempty"` // 来源文档名称
}

// TextForEmbedding 返回应被向量化的文本：优先 EmbedText（结构感知切块器注入的
// 面包屑+正文），为空则回退 Content。各 VectorStore 的 embed 路径统一调用本方法，
// 作为"嵌入用什么文本"的唯一真相源，使"向量带面包屑、Content 保持干净"成立。
func (k KnowledgeChunk) TextForEmbedding() string {
	if k.EmbedText != "" {
		return k.EmbedText
	}
	return k.Content
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
	DocStatusSplitting DocStatus = "SPLITTING"
	DocStatusTagging   DocStatus = "TAGGING"
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
