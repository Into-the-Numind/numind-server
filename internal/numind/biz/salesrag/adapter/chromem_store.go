package adapter

import (
	"context"
	"fmt"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"strconv"
	"strings"
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

		// 序列化 Tags 为逗号分隔字符串，因为 chromem 只支持 map[string]string 作为 metadata
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
				"user_id":     strconv.FormatUint(uint64(chunk.UserID), 10),
				"summary":     chunk.Summary,
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
	// 如果不是全局管理员且没有指定 DocumentIDs，则直接返回空结果，杜绝泄露
	if len(filter.DocumentIDs) == 0 {
		return nil, nil // 严格模式
	}

	where := make(map[string]string)
	// 如果指定了用户，注入 user_id 到 where (如果 chromem 支持精准过滤)
	if filter.UserID > 0 {
		where["user_id"] = strconv.FormatUint(uint64(filter.UserID), 10)
	}

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
		if uID, ok := res.Metadata["user_id"]; ok {
			val, _ := strconv.ParseUint(uID, 10, 64)
			chunk.UserID = uint(val)
		}
		chunk.SourceRef = res.Metadata["source_ref"]
		chunk.Summary = res.Metadata["summary"]

		// 还原 Tags
		if tagsStr, ok := res.Metadata["tags"]; ok && tagsStr != "" {
			chunk.Tags = strings.Split(tagsStr, ",")
		}

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
	// 1. 用户隔离 (强制)
	if filter.UserID > 0 && chunk.UserID != filter.UserID {
		return false
	}

	// 2. DocumentIDs 过滤 (严格模式：空列表不返回结果)
	if len(filter.DocumentIDs) == 0 {
		return false
	}

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

	return true
}

// FetchByDocumentID 直接获取指定文档的所有切片
func (s *ChromemStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10000 // 默认返回10000条，确保能获取所有切片
	}

	// 使用一个通用查询词生成向量
	queryEmbedding, err := s.embeddingFunc(ctx, "文档内容")
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// 使用一个较大的查询限制来确保获取所有切片
	results, err := s.collection.QueryEmbedding(ctx, queryEmbedding, limit, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query chromem: %w", err)
	}

	// 解析结果并过滤指定文档ID
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

		// 只返回指定文档的切片
		if chunk.DocumentID != documentID {
			continue
		}

		if uID, ok := res.Metadata["user_id"]; ok {
			val, _ := strconv.ParseUint(uID, 10, 64)
			chunk.UserID = uint(val)
		}
		chunk.SourceRef = res.Metadata["source_ref"]
		chunk.Summary = res.Metadata["summary"]

		// 还原 Tags
		if tagsStr, ok := res.Metadata["tags"]; ok && tagsStr != "" {
			chunk.Tags = strings.Split(tagsStr, ",")
		}

		chunks = append(chunks, chunk)
		if len(chunks) >= limit {
			break
		}
	}

	return chunks, nil
}
