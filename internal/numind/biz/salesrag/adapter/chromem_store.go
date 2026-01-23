package adapter

import (
	"context"
	"fmt"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"strconv"
	"sync"

	"github.com/philippgille/chromem-go"
)

// ChromemStore 基于 chromem-go 的本地持久化向量库适配器
type ChromemStore struct {
	db         *chromem.DB
	collection *chromem.Collection
	mu         sync.RWMutex

	// embeddingFunc 用于生成向量 (例如调用阿里百炼或火山方舟的 Embedding API)
	embeddingFunc func(ctx context.Context, text string) ([]float32, error)
}

// NewChromemStore 创建新的 ChromemStore
func NewChromemStore(dbPath string, collectionName string, embedFn func(ctx context.Context, text string) ([]float32, error)) (*ChromemStore, error) {
	db, err := chromem.NewPersistentDB(dbPath, false)
	if err != nil {
		return nil, fmt.Errorf("failed to init chromem db: %w", err)
	}

	col, err := db.GetOrCreateCollection(collectionName, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get collection: %w", err)
	}

	return &ChromemStore{
		db:            db,
		collection:    col,
		embeddingFunc: embedFn,
	}, nil
}

func (s *ChromemStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	docs := make([]chromem.Document, 0, len(chunks))
	for _, chunk := range chunks {
		// 生成向量
		embedding, err := s.embeddingFunc(ctx, chunk.Content)
		if err != nil {
			return fmt.Errorf("failed to embed content for chunk %s: %w", chunk.ID, err)
		}

		// 序列化 SalesStage 和 Tags 为逗号分隔字符串，因为 chromem 只支持 map[string]string 作为 metadata
		stagesStr := ""
		for i, stage := range chunk.SalesStage {
			if i > 0 {
				stagesStr += ","
			}
			stagesStr += string(stage)
		}

		tagsStr := ""
		for i, tag := range chunk.Tags {
			if i > 0 {
				tagsStr += ","
			}
			tagsStr += tag
		}

		doc := chromem.Document{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Embedding: embedding,
			Metadata: map[string]string{
				"document_id": strconv.FormatUint(uint64(chunk.DocumentID), 10),
				"doc_type":    string(chunk.DocType),
				"sales_stage": stagesStr,
				"tags":        tagsStr,
				"source_ref":  chunk.SourceRef,
			},
		}
		docs = append(docs, doc)
	}

	// 并行度设为 1，因为我们已经手动管理了 mu
	return s.collection.AddDocuments(ctx, docs, 1)
}

func (s *ChromemStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// chromem 不支持直接按 metadata 过滤删除，需要先查出 ID 再删
	// 或者在这里使用 chromem 的过滤功能
	docIDStr := strconv.FormatUint(uint64(documentID), 10)
	where := map[string]string{
		"document_id": docIDStr,
	}

	// 这里的实现性能一般，但在本地库中可以接受
	return s.collection.Delete(ctx, nil, where, "")
}

func (s *ChromemStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 1. 生成查询向量
	queryEmbedding, err := s.embeddingFunc(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// 2. 构造过滤条件
	where := make(map[string]string)

	// Chromem-go 的 WHERE 过滤目前相对简单，只支持完全匹配
	// 复杂的过滤逻辑可能需要在内存中二次筛选

	// 3. 执行查询
	// 注意: chromem 的 QueryEmbedding 会返回相似度结果
	results, err := s.collection.QueryEmbedding(ctx, queryEmbedding, limit*2, where, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query chromem: %w", err)
	}

	// 4. 解析结果并进行业务级过滤 (DocType, SalesStage, DocumentIDs)
	chunks := make([]domain.KnowledgeChunk, 0, len(results))
	for _, res := range results {
		chunk := domain.KnowledgeChunk{
			ID:      res.ID,
			Content: res.Content,
		}

		// 从 Metadata 还原字段
		if dID, ok := res.Metadata["document_id"]; ok {
			val, _ := strconv.ParseUint(dID, 10, 64)
			chunk.DocumentID = uint(val)
		}
		if dType, ok := res.Metadata["doc_type"]; ok {
			chunk.DocType = domain.DocType(dType)
		}
		chunk.SourceRef = res.Metadata["source_ref"]

		// TODO: 还原 SalesStage 和 Tags

		// 业务级过滤
		if !s.matchFilter(chunk, filter) {
			continue
		}

		chunks = append(chunks, chunk)
		if len(chunks) >= limit {
			break
		}
	}

	return chunks, nil
}

func (s *ChromemStore) matchFilter(chunk domain.KnowledgeChunk, filter port.SearchFilter) bool {
	// 如果过滤条件为空，默认通过

	// 1. DocTypes 过滤
	if len(filter.DocTypes) > 0 {
		matched := false
		for _, dt := range filter.DocTypes {
			if chunk.DocType == dt {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 2. DocumentIDs 过滤 (Scope Control)
	if len(filter.DocumentIDs) > 0 {
		matched := false
		for _, id := range filter.DocumentIDs {
			if chunk.DocumentID == id {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 3. SalesStage 过滤 (省略复杂逻辑，暂不处理多对多匹配)

	return true
}
