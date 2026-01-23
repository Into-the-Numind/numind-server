package adapter

import (
	"context"
	"strings"
	"sync"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
)

// MemoryStore 内存版向量数据库 (用于测试和开发)
type MemoryStore struct {
	mu     sync.RWMutex
	chunks map[string]domain.KnowledgeChunk
}

// NewMemoryStore 创建一个新的内存存储实例
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		chunks: make(map[string]domain.KnowledgeChunk),
	}
}

// Ensure MemoryStore implements VectorStore interface
var _ port.VectorStore = (*MemoryStore)(nil)

// Upsert 批量插入或更新
func (m *MemoryStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, chunk := range chunks {
		m.chunks[chunk.ID] = chunk
	}
	return nil
}

// DeleteByDocumentID 删除指定文档的所有切片
func (m *MemoryStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, chunk := range m.chunks {
		if chunk.DocumentID == documentID {
			delete(m.chunks, id)
		}
	}
	return nil
}

// Search 模拟语义检索 (实际上是简单的字符串包含匹配)
func (m *MemoryStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []domain.KnowledgeChunk
	queryLower := strings.ToLower(query)

	for _, chunk := range m.chunks {
		// 1. 模拟相似度匹配 (Content 包含 Query)
		if query != "" && !strings.Contains(strings.ToLower(chunk.Content), queryLower) {
			continue
		}

		// 2. DocType 过滤
		if len(filter.DocTypes) > 0 {
			match := false
			for _, dt := range filter.DocTypes {
				if chunk.DocType == dt {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		// 3. DocumentIDs 过滤
		if len(filter.DocumentIDs) > 0 {
			match := false
			for _, docID := range filter.DocumentIDs {
				if chunk.DocumentID == docID {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		// 4. SalesStage 过滤 (如果有交集则匹配)
		if len(filter.SalesStages) > 0 {
			match := false
			// Check intersection between chunk.SalesStage and filter.SalesStages
			for _, stage := range chunk.SalesStage {
				for _, reqStage := range filter.SalesStages {
					if stage == reqStage {
						match = true
						break
					}
				}
				if match {
					break
				}
			}
			// 注意：如果切片没有 Stage 标签，但过滤要求 Stage，则不匹配
			if !match {
				continue
			}
		}

		results = append(results, chunk)
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
