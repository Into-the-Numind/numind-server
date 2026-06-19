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

// KeywordSearcher 是 VectorStore 的**可选**扩展接口：提供 BM25/全文关键词检索通道，
// 供 retrieve.Service 在混合检索（dense + keyword + RRF）开启时调用。
//
// 仅 SQLiteVecStore 实现本接口（基于 FTS5）。其它 store（memory/dashvector/chromem/
// viking）不实现 → Service type-assert 失败 → 自动退回纯向量，零回归。
type KeywordSearcher interface {
	// SearchKeyword 关键词检索。约定：FTS5 不可用 / DocumentIDs 为空 / 查询分词后为空 /
	// MATCH 语法错误等异常一律返回 (nil, nil)，使调用方优雅降级为纯向量，绝不杀检索。
	SearchKeyword(ctx context.Context, query string, filter SearchFilter, limit int) ([]domain.KnowledgeChunk, error)
}
