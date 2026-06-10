package port

import (
	"context"
	"numind-server/internal/pkg/retrieval/domain"
)

// SearchFilter 定义检索标准
type SearchFilter struct {
	UserID      uint     // 所属用户ID，用于强制隔离
	Tags        []string // 标签过滤
	DocumentIDs []uint   // 根据文档ID/知识库ID过滤 (Scope Filtering)
}

// VectorStore 抽象底层向量数据库 (DashVector/Qdrant/Milvus)
type VectorStore interface {
	// Upsert 批量插入或更新知识切片
	Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error

	// DeleteByDocumentID 删除指定文档的所有切片
	DeleteByDocumentID(ctx context.Context, documentID uint) error

	// Search 根据语义检索相关切片
	Search(ctx context.Context, query string, filter SearchFilter, limit int) ([]domain.KnowledgeChunk, error)

	// FetchByDocumentID 直接获取指定文档的所有切片（不使用向量搜索）
	// 用于列表展示等场景，更高效且结果完整
	FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error)
}
