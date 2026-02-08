package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"

	"github.com/volcengine/volc-sdk-golang/service/vikingdb"
)

type Embedder func(ctx context.Context, text string) ([]float32, error)

// VikingStore 基于火山引擎 VikingDB 的向量库适配器
type VikingStore struct {
	service    *vikingdb.VikingDBService
	collection *vikingdb.Collection
	index      *vikingdb.Index
	colName    string
	idxName    string
	embedder   Embedder
}

// NewVikingStore 创建新的 VikingStore
func NewVikingStore(ak, sk, region, host, collectionName, indexName string, embedder Embedder) (*VikingStore, error) {
	// 1. 验证参数，防止 SDK 内部 panic
	if host == "" || region == "" {
		return nil, fmt.Errorf("vikingdb host and region must be provided")
	}

	// 2. 初始化 Service
	service := vikingdb.NewVikingDBService(host, region, ak, sk, "https")

	col, err := service.GetCollection(collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get collection (please ensure it exists in console): %w", err)
	}

	idx, err := service.GetIndex(collectionName, indexName)
	if err != nil {
		return nil, fmt.Errorf("failed to get index: %w", err)
	}

	return &VikingStore{
		service:    service,
		collection: col,
		index:      idx,
		colName:    collectionName,
		idxName:    indexName,
		embedder:   nil, // 托管模式下不再需要后端 embedder
	}, nil
}

func (s *VikingStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	datas := make([]vikingdb.Data, 0, len(chunks))
	for _, chunk := range chunks {
		fields := map[string]interface{}{
			"id":         chunk.ID,
			"doc_id":     int64(chunk.DocumentID),
			"user_id":    int64(chunk.UserID), // 使用实际用户ID
			"summary":    chunk.Summary,
			"tags":       strings.Join(chunk.Tags, ","),
			"content":    chunk.Content,
			"source_ref": chunk.SourceRef,
		}

		datas = append(datas, vikingdb.Data{
			Fields: fields,
			TTL:    0,
		})
	}

	batchSize := 100
	for i := 0; i < len(datas); i += batchSize {
		end := i + batchSize
		if end > len(datas) {
			end = len(datas)
		}
		batch := datas[i:end]
		if err := s.collection.UpsertData(batch); err != nil {
			return fmt.Errorf("vikingdb upsert failed: %w", err)
		}
	}
	return nil
}

func (s *VikingStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	// 托管模式下，我们仍然需要通过标量过滤来删除
	// 这里使用 SearchByText 配合 filter 来查找并删除
	searchOpts := vikingdb.NewSearchOptions()
	searchOpts.SetLimit(1000)
	filterMap := map[string]interface{}{
		"doc_id": map[string]interface{}{
			"eq": int64(documentID),
		},
	}
	searchOpts.SetFilter(filterMap)
	searchOpts.SetOutputFields([]string{"id"})

	// 在托管向量化模式下，我们可以使用 SearchByText
	results, err := s.index.SearchByText(vikingdb.TextObject{Text: ""}, searchOpts)
	if err != nil {
		return fmt.Errorf("failed to search for deletion: %w", err)
	}

	if len(results) == 0 {
		return nil
	}

	idsToDelete := make([]interface{}, 0, len(results))
	for _, res := range results {
		idsToDelete = append(idsToDelete, res.Id)
	}

	return s.collection.DeleteData(idsToDelete)
}

func (s *VikingStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	opts := vikingdb.NewSearchOptions()
	opts.SetLimit(int64(limit))
	opts.SetOutputFields([]string{"id", "content", "summary", "tags", "source_ref", "doc_id"})

	// Build Filter map
	// 如果 DocumentIDs 为空，且不是全局管理员，则不返回任何结果，防止泄露背景知识
	if len(filter.DocumentIDs) == 0 {
		return nil, nil // 严格模式
	}

	filterMap := buildVikingFilter(filter)
	if len(filterMap) > 0 {
		opts.SetFilter(filterMap)
	}

	// 托管模式直接调用 SearchByText
	results, err := s.index.SearchByText(vikingdb.TextObject{Text: query}, opts)
	if err != nil {
		return nil, err
	}

	// 4. Map results
	chunks := make([]domain.KnowledgeChunk, 0, len(results))
	for _, res := range results {
		c := domain.KnowledgeChunk{}
		if idStr, ok := res.Id.(string); ok {
			c.ID = idStr
		} else {
			c.ID = fmt.Sprintf("%v", res.Id)
		}

		if val, ok := res.Fields["content"].(string); ok {
			c.Content = val
		}
		if val, ok := res.Fields["content"].(string); ok {
			c.Content = val
		}
		if val, ok := res.Fields["doc_id"].(json.Number); ok {
			idInt, _ := val.Int64()
			c.DocumentID = uint(idInt)
		} else if val, ok := res.Fields["doc_id"].(float64); ok {
			c.DocumentID = uint(val)
		}

		if val, ok := res.Fields["tags"].(string); ok {
			c.Tags = strings.Split(val, ",")
		}
		if val, ok := res.Fields["user_id"].(json.Number); ok {
			idInt, _ := val.Int64()
			c.UserID = uint(idInt)
		} else if val, ok := res.Fields["user_id"].(float64); ok {
			c.UserID = uint(val)
		}
		if val, ok := res.Fields["summary"].(string); ok {
			c.Summary = val
		}
		if val, ok := res.Fields["source_ref"].(string); ok {
			c.SourceRef = val
		}

		chunks = append(chunks, c)
	}

	return chunks, nil
}

// FetchByDocumentID 直接获取指定文档的所有切片（不使用语义搜索）
func (s *VikingStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	if limit <= 0 {
		limit = 10000 // 默认返回10000条，确保能获取所有切片
	}

	opts := vikingdb.NewSearchOptions()
	opts.SetLimit(int64(limit))
	opts.SetOutputFields([]string{"id", "content", "summary", "tags", "source_ref", "doc_id", "user_id"})

	// 构建过滤条件：只查询指定文档ID
	filterMap := map[string]interface{}{
		"doc_id": map[string]interface{}{"=": int64(documentID)},
	}
	opts.SetFilter(filterMap)

	// 使用一个通用查询词
	results, err := s.index.SearchByText(vikingdb.TextObject{Text: "文档内容"}, opts)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	// 映射结果
	chunks := make([]domain.KnowledgeChunk, 0, len(results))
	for _, res := range results {
		c := domain.KnowledgeChunk{}
		if idStr, ok := res.Id.(string); ok {
			c.ID = idStr
		} else {
			c.ID = fmt.Sprintf("%v", res.Id)
		}

		if val, ok := res.Fields["content"].(string); ok {
			c.Content = val
		}
		if val, ok := res.Fields["doc_id"].(json.Number); ok {
			idInt, _ := val.Int64()
			c.DocumentID = uint(idInt)
		} else if val, ok := res.Fields["doc_id"].(float64); ok {
			c.DocumentID = uint(val)
		}
		if val, ok := res.Fields["user_id"].(json.Number); ok {
			idInt, _ := val.Int64()
			c.UserID = uint(idInt)
		} else if val, ok := res.Fields["user_id"].(float64); ok {
			c.UserID = uint(val)
		}
		if val, ok := res.Fields["tags"].(string); ok && val != "" {
			c.Tags = strings.Split(val, ",")
		}
		if val, ok := res.Fields["summary"].(string); ok {
			c.Summary = val
		}
		if val, ok := res.Fields["source_ref"].(string); ok {
			c.SourceRef = val
		}

		chunks = append(chunks, c)
	}

	return chunks, nil
}

func buildVikingFilter(f port.SearchFilter) map[string]interface{} {
	// VikingDB filter DSL: {"field": {"op": val}}
	// "and": [{"field":...}, {...}]
	conditions := make([]map[string]interface{}, 0)

	// 强制注入 UserID 过滤
	if f.UserID > 0 {
		conditions = append(conditions, map[string]interface{}{
			"user_id": map[string]interface{}{"=": int64(f.UserID)},
		})
	}

	if len(f.DocumentIDs) > 0 {
		ids := make([]interface{}, len(f.DocumentIDs))
		for i, id := range f.DocumentIDs {
			ids[i] = int64(id)
		}
		conditions = append(conditions, map[string]interface{}{
			"doc_id": map[string]interface{}{"in": ids},
		})
	}

	if len(conditions) == 0 {
		return nil
	}
	if len(conditions) == 1 {
		return conditions[0]
	}
	return map[string]interface{}{
		"and": conditions,
	}
}
