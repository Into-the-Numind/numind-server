package port

import (
	"context"
	"numind-server/internal/numind/biz/salesrag/domain"
)

// SearchFilter 定义检索标准
type SearchFilter struct {
	SalesStages []domain.SalesStage // 根据销售阶段过滤
	Tags        []string            // 标签过滤
	DocumentIDs []uint              // [New] 根据文档ID/知识库ID过滤 (Scope Filtering)
}

// VectorStore 抽象底层向量数据库 (DashVector/Qdrant/Milvus)
type VectorStore interface {
	// Upsert 批量插入或更新知识切片
	Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error

	// DeleteByDocumentID 删除指定文档的所有切片
	DeleteByDocumentID(ctx context.Context, documentID uint) error

	// Search 根据语义检索相关切片
	Search(ctx context.Context, query string, filter SearchFilter, limit int) ([]domain.KnowledgeChunk, error)
}
