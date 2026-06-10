package biz

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/retrieval/domain"
)

// kbDocStore 实现 retrieve.DocStore，解析 Scope{AllEnabled:true} 所需的"全部启用文档" docID 列表。
//
// 过滤逻辑对齐 salesrag.go 的检索白名单构建（salesRAGBiz.Retrieve / RetrieveStream）：
// 列出用户全部文档 → 仅保留 IsEnabled && Status==COMPLETED 的文档 → 返回其 docID。
// agent 的 kb_search 在空 doc_ids 时走 AllEnabled，需要该适配器解析"翻全部启用文档"。
type kbDocStore struct {
	docs store.KnowledgeDocumentStore
}

// newKBDocStore 包装 ds.KnowledgeDocuments() 为 retrieve.DocStore。
func newKBDocStore(docs store.KnowledgeDocumentStore) *kbDocStore {
	return &kbDocStore{docs: docs}
}

// ListEnabledDocIDs 返回该用户全部"启用且已完成"的文档 ID。
// 与 salesrag 白名单一致：doc.IsEnabled && doc.Status == COMPLETED。
func (s *kbDocStore) ListEnabledDocIDs(ctx context.Context, userID uint) ([]uint, error) {
	docs, err := s.docs.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("kbDocStore.ListEnabledDocIDs(userID=%d): %w", userID, err)
	}
	ids := make([]uint, 0, len(docs))
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		if doc.IsEnabled && doc.Status == string(domain.DocStatusCompleted) {
			ids = append(ids, doc.ID)
		}
	}
	return ids, nil
}
