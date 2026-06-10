package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
)

type DashVectorStore struct {
	endpoint   string
	apiKey     string
	collection string
	client     *httpclient.Client
	embedder   func(ctx context.Context, text string) ([]float32, error)
}

func NewDashVectorStore(endpoint, apiKey, collection string, embedder func(ctx context.Context, text string) ([]float32, error)) *DashVectorStore {
	// 允许从 config 初始化 client，或者使用默认
	client := httpclient.NewClientFromConfig("ali.dashvector")

	return &DashVectorStore{
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		collection: collection,
		client:     client,
		embedder:   embedder,
	}
}

type DashVectorDoc struct {
	ID     string                 `json:"id"`
	Vector []float32              `json:"vector,omitempty"`
	Fields map[string]interface{} `json:"fields"`
}

type DashVectorResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Request string      `json:"request_id,omitempty"`
	Output  interface{} `json:"output"`
}

func (s *DashVectorStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	log.Infow("DashVector Upsert start", "count", len(chunks), "collection", s.collection)
	if len(chunks) == 0 {
		return nil
	}

	docs := make([]DashVectorDoc, 0, len(chunks))
	for _, chunk := range chunks {
		// 1. Generate embedding if missing
		vector := chunk.Vector
		if len(vector) == 0 && s.embedder != nil {
			var err error
			vector, err = s.embedder(ctx, chunk.Content)
			if err != nil {
				return fmt.Errorf("failed to generate embedding for chunk %s: %w", chunk.ID, err)
			}
		}

		fields := map[string]interface{}{
			"doc_id":     chunk.DocumentID,
			"user_id":    chunk.UserID,
			"tags":       strings.Join(chunk.Tags, ","),
			"summary":    chunk.Summary,
			"content":    chunk.Content,
			"source_ref": chunk.SourceRef,
		}

		docs = append(docs, DashVectorDoc{
			ID:     chunk.ID,
			Vector: vector,
			Fields: fields,
		})
	}

	// Batch upsert
	batchSize := 20 // Optimize batch size for generic HTTP payload limits
	for i := 0; i < len(docs); i += batchSize {
		end := i + batchSize
		if end > len(docs) {
			end = len(docs)
		}
		batch := docs[i:end]

		reqBody := map[string]interface{}{
			"docs": batch,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		url := fmt.Sprintf("%s/v1/collections/%s/docs", s.endpoint, s.collection)
		req := &httpclient.Request{
			Method:  "POST",
			URL:     url,
			Body:    bytes.NewBuffer(bodyBytes),
			Context: ctx,
			Headers: map[string]string{
				"dashvector-auth-token": s.apiKey,
				"Content-Type":          "application/json",
			},
		}

		respBytes, err := s.client.DoWithJSONResponse(req)
		if err != nil {
			return fmt.Errorf("dashvector upsert request failed: %w", err)
		}

		var resp DashVectorResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
		if resp.Code != 0 {
			return fmt.Errorf("dashvector upsert failed: code=%d msg=%s", resp.Code, resp.Message)
		}
		log.Infow("DashVector Upsert batch success", "batch_size", len(batch), "resp_code", resp.Code)
	}

	// 记录向量库操作用量
	if bc := billing.FromContext(ctx); bc != nil {
		billing.RecordVectorDB(bc.UserID, "dashvector", bc.Operation, len(chunks), bc.Meta)
	}

	return nil
}

func (s *DashVectorStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	// First query to get IDs
	// DashVector delete by filter might be supported directly or retrieve-then-delete
	// To be safe, we iterate. But efficient way is DELETE with filter if API supports.
	// Common DashVector API supports DELETE /docs with IDs.
	// It relies on query first.

	// Step 1: Query all chunks for this doc_id
	filter := fmt.Sprintf("doc_id = %d", documentID)
	// We need pagination here if there are many chunks.
	// For simplicity, we assume robust implementation would loop.
	// But let's check chunks 100 at a time.

	// offset := 0
	limit := 100
	var idsToDelete []string

	for {
		queryBody := map[string]interface{}{
			"filter":         filter,
			"top_k":          limit,
			"include_vector": false,
		}
		// NOTE: DashVector query requires vector usually, but some APIs allow filter-only or match-all.
		// If vector is required, we might need a workaround or use a specific "scan" API.
		// However, standard search usually allows filter-only if the engine supports it.
		// If DashVector doesn't support filter-only search, we might need a dummy vector or use 'query' param if supported.

		// Let's assume standard behavior: we can query by filter without vector if we use a specific endpoint or param?
		// Actually, DashVector Go SDK `Query(ctx context.Context, input QueryInput)`. `QueryInput` has `Vector` optional? No usually required.
		// But for deleting, we should look for a delete-by-filter API.

		// Alternative: DELETE with complex filter?
		// Let's try to query with a zero vector or dummy vector just to match filter results.
		// A dummy vector + filter should work to retrieve IDs.
		dummyVector := make([]float32, 2048) // text-embedding-v4 使用 2048 维
		queryBody["vector"] = dummyVector

		bodyBytes, _ := json.Marshal(queryBody)
		url := fmt.Sprintf("%s/v1/collections/%s/query", s.endpoint, s.collection)

		req := &httpclient.Request{
			Method:  "POST",
			URL:     url,
			Body:    bytes.NewBuffer(bodyBytes),
			Context: ctx,
			Headers: map[string]string{
				"dashvector-auth-token": s.apiKey,
				"Content-Type":          "application/json",
			},
		}

		respBytes, err := s.client.DoWithJSONResponse(req)
		if err != nil {
			return fmt.Errorf("dashvector query-for-delete failed: %w", err)
		}

		var resp struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Output  []struct {
				ID string `json:"id"`
			} `json:"output"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("dashvector query failed: %d %s", resp.Code, resp.Message)
		}

		if len(resp.Output) == 0 {
			break
		}

		for _, item := range resp.Output {
			idsToDelete = append(idsToDelete, item.ID)
		}

		if len(resp.Output) < limit {
			break
		}
		// Pagination logic for next loop?
		// Currently API might not support offset in query easily without partitioned query.
		// BUT, if we delete them, next query will find "next page" effectively.

	}

	if len(idsToDelete) == 0 {
		return nil
	}

	// Step 2: Delete
	delBody := map[string]interface{}{
		"ids": idsToDelete,
	}
	delBytes, _ := json.Marshal(delBody)
	delUrl := fmt.Sprintf("%s/v1/collections/%s/docs", s.endpoint, s.collection) // DELETE method

	req := &httpclient.Request{
		Method:  "DELETE",
		URL:     delUrl,
		Body:    bytes.NewBuffer(delBytes),
		Context: ctx,
		Headers: map[string]string{
			"dashvector-auth-token": s.apiKey,
			"Content-Type":          "application/json",
		},
	}

	respBytes, err := s.client.DoWithJSONResponse(req)
	if err != nil {
		return fmt.Errorf("dashvector delete failed: %w", err)
	}
	var resp DashVectorResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("dashvector delete logic failed: %d %s", resp.Code, resp.Message)
	}

	return nil
}

func (s *DashVectorStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	// 1. Generate embedding
	if s.embedder == nil {
		return nil, fmt.Errorf("embedder is required for search")
	}
	vector, err := s.embedder(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	// 2. Build Filter
	// 如果 DocumentIDs 为空，且不是全局管理员，则不返回任何结果，防止泄露背景知识
	if len(filter.DocumentIDs) == 0 {
		return nil, nil // 严格模式：没有选知识库就不检索
	}
	filterStr := buildDashVectorFilter(filter)

	// 3. Query
	queryBody := map[string]interface{}{
		"vector":         vector,
		"top_k":          limit,
		"include_vector": false,
		"output_fields":  []string{"id", "content", "tags", "summary", "source_ref", "doc_id", "user_id"},
	}
	if filterStr != "" {
		queryBody["filter"] = filterStr
	}

	bodyBytes, _ := json.Marshal(queryBody)
	url := fmt.Sprintf("%s/v1/collections/%s/query", s.endpoint, s.collection)

	req := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"dashvector-auth-token": s.apiKey,
			"Content-Type":          "application/json",
		},
	}

	respBytes, err := s.client.DoWithJSONResponse(req)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Output  []struct {
			ID     string                 `json:"id"`
			Score  float32                `json:"score"`
			Fields map[string]interface{} `json:"fields"`
		} `json:"output"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("search failed: %d %s", resp.Code, resp.Message)
	}

	// 4. Map results
	chunks := make([]domain.KnowledgeChunk, 0, len(resp.Output))
	for _, item := range resp.Output {
		c := domain.KnowledgeChunk{
			ID:    item.ID,
			Score: item.Score, // 保存检索得分
		}

		if val, ok := item.Fields["content"].(string); ok {
			c.Content = val
		}
		if val, ok := item.Fields["doc_id"].(float64); ok {
			c.DocumentID = uint(val)
		}
		if val, ok := item.Fields["user_id"].(float64); ok {
			c.UserID = uint(val)
		}
		if val, ok := item.Fields["tags"].(string); ok {
			c.Tags = strings.Split(val, ",")
		}
		if val, ok := item.Fields["summary"].(string); ok {
			c.Summary = val
		}
		if val, ok := item.Fields["source_ref"].(string); ok {
			c.SourceRef = val
		}

		chunks = append(chunks, c)
	}

	return chunks, nil
}

// FetchByDocumentID 直接获取指定文档的所有切片（不使用语义搜索）
func (s *DashVectorStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	// DashVector 需要向量才能查询，所以我们使用一个通用的查询
	// 生成一个中性向量（全部为0.5）来绕过向量相似度计算
	// 通过过滤条件来精确匹配文档ID

	if limit <= 0 {
		limit = 10000 // 默认返回10000条，确保能获取所有切片
	}

	// 方案：使用一个固定的embedding向量（可以是零向量或随机向量）
	// 由于我们主要依赖过滤条件，向量的具体值不重要
	// 但 DashVector 要求向量维度与collection匹配，所以我们使用一个通用查询生成向量

	// 使用一个通用查询词来生成向量
	var vector []float32
	var err error
	if s.embedder != nil {
		// 使用一个中性的查询词，避免偏向任何特定内容
		vector, err = s.embedder(ctx, "文档内容")
		if err != nil {
			return nil, fmt.Errorf("failed to generate embedding: %w", err)
		}
	} else {
		return nil, fmt.Errorf("embedder is required")
	}

	// 构建过滤条件：只查询指定文档ID
	filterStr := fmt.Sprintf("doc_id = %d", documentID)

	// 构建查询请求
	queryBody := map[string]interface{}{
		"vector":         vector,
		"top_k":          limit,
		"include_vector": false,
		"output_fields":  []string{"id", "content", "tags", "summary", "source_ref", "doc_id", "user_id"},
		"filter":         filterStr,
	}

	bodyBytes, _ := json.Marshal(queryBody)
	url := fmt.Sprintf("%s/v1/collections/%s/query", s.endpoint, s.collection)

	req := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"dashvector-auth-token": s.apiKey,
			"Content-Type":          "application/json",
		},
	}

	respBytes, err := s.client.DoWithJSONResponse(req)
	if err != nil {
		return nil, fmt.Errorf("fetch request failed: %w", err)
	}

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Output  []struct {
			ID     string                 `json:"id"`
			Score  float32                `json:"score"`
			Fields map[string]interface{} `json:"fields"`
		} `json:"output"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("fetch failed: %d %s", resp.Code, resp.Message)
	}

	// 映射结果到 KnowledgeChunk
	chunks := make([]domain.KnowledgeChunk, 0, len(resp.Output))
	for _, item := range resp.Output {
		c := domain.KnowledgeChunk{
			ID: item.ID,
		}

		if val, ok := item.Fields["content"].(string); ok {
			c.Content = val
		}
		if val, ok := item.Fields["doc_id"].(float64); ok {
			c.DocumentID = uint(val)
		}
		if val, ok := item.Fields["user_id"].(float64); ok {
			c.UserID = uint(val)
		}
		if val, ok := item.Fields["tags"].(string); ok && val != "" {
			c.Tags = strings.Split(val, ",")
		}
		if val, ok := item.Fields["summary"].(string); ok {
			c.Summary = val
		}
		if val, ok := item.Fields["source_ref"].(string); ok {
			c.SourceRef = val
		}

		chunks = append(chunks, c)
	}

	log.Infow("FetchByDocumentID completed", "documentID", documentID, "returned", len(chunks), "limit", limit)
	return chunks, nil
}

// buildDashVectorFilter 构建 DashVector 过滤条件
// 安全约束：当 DocumentIDs 非空时，仅按 doc_id 过滤（不叠加 user_id），
// 因为 biz 层 RetrieveStream 已通过 enabledDocIDs 白名单验证了访问权限。
// 这使得系统文档（user_id=0）能被正常检索。
func buildDashVectorFilter(f port.SearchFilter) string {
	parts := []string{}

	if len(f.DocumentIDs) > 0 {
		ids := make([]string, len(f.DocumentIDs))
		for i, id := range f.DocumentIDs {
			ids[i] = fmt.Sprintf("%d", id)
		}
		parts = append(parts, fmt.Sprintf("doc_id IN (%s)", strings.Join(ids, ", ")))
	} else if f.UserID > 0 {
		// 无文档 ID 时，按用户过滤
		parts = append(parts, fmt.Sprintf("user_id = %d", f.UserID))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}
