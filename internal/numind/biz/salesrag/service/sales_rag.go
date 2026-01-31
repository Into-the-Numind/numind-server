package service

import (
	"context"
	"fmt"
	"sync"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/pkg/log"
)

// RetrievalVerdict 检索判决结果 (V1 兼容)
type RetrievalVerdict struct {
	Query        string                  `json:"query"`         // 原始问题
	RewriteQuery string                  `json:"rewrite_query"` // 改写后的检索词
	IsChitChat   bool                    `json:"is_chit_chat"`  // 是否为闲聊
	Reason       string                  `json:"reason"`        // 逻辑判断依据
	Evidence     []domain.KnowledgeChunk `json:"evidence"`      // 检索到的证据
	Answer       string                  `json:"answer"`        // AI生成的最终回复

	// V2 扩展字段
	Intent        port.IntentType `json:"intent,omitempty"`         // 识别的意图
	SearchQueries []string        `json:"search_queries,omitempty"` // 多路搜索词
}

// SalesRAGService 销售智能体 RAG 服务
type SalesRAGService struct {
	store     port.VectorStore
	router    port.IntentRouter
	dmxClient *adapter.DMXAPIClient
}

// NewSalesRAGService 创建新的 SalesRAGService
func NewSalesRAGService(store port.VectorStore, router port.IntentRouter) *SalesRAGService {
	return &SalesRAGService{
		store:     store,
		router:    router,
		dmxClient: adapter.NewDMXAPIClient(),
	}
}

// DeleteByDocumentID 删除指定文档的所有切片
func (s *SalesRAGService) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	return s.store.DeleteByDocumentID(ctx, documentID)
}

// Search 基础检索
func (s *SalesRAGService) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	return s.store.Search(ctx, query, filter, limit)
}

// FetchByDocumentID 直接获取指定文档的所有切片（不使用向量搜索）
func (s *SalesRAGService) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	return s.store.FetchByDocumentID(ctx, documentID, limit)
}

// RetrieveForResponse 为生成回复进行检索 (V1 兼容接口)
// Deprecated: 请使用 RetrieveForResponseV2
func (s *SalesRAGService) RetrieveForResponse(
	ctx context.Context,
	query string,
	docIDs []uint,
) (*RetrievalVerdict, error) {
	// 直接调用 V2 接口
	return s.RetrieveForResponseV2(ctx, query, docIDs, nil)
}

// RetrieveForResponseV2 V2 版检索流程
// 1. 意图识别 + 多路 Query 生成 (LLM: qwen-turbo-latest)
// 2. 并行全库检索
// 3. 聚合去重
// 4. LLM Rerank (qwen3-rerank) - 返回 Top N 索引
// 5. 组装最终 Evidence
func (s *SalesRAGService) RetrieveForResponseV2(
	ctx context.Context,
	query string,
	docIDs []uint,
	history []string,
) (*RetrievalVerdict, error) {

	// 1. 意图分析 + Query 生成 (LLM: qwen-turbo-latest)
	intentResult, err := s.router.AnalyzeIntentV2(ctx, query, history)
	if err != nil {
		return nil, fmt.Errorf("intent analysis failed: %w", err)
	}

	verdict := &RetrievalVerdict{
		Query:         query,
		Intent:        intentResult.Intent,
		SearchQueries: intentResult.SearchQueries,
		Reason:        intentResult.Reason,
	}

	// 设置 RewriteQuery 为第一个搜索词（兼容 V1）
	if len(intentResult.SearchQueries) > 0 {
		verdict.RewriteQuery = intentResult.SearchQueries[0]
	}

	// 2. 快速路径：闲聊不检索
	if intentResult.Intent == port.IntentChitChat {
		verdict.IsChitChat = true
		return verdict, nil
	}

	// 3. 并行多路检索
	filter := port.SearchFilter{DocumentIDs: docIDs}
	allChunks, err := s.parallelSearch(ctx, intentResult.SearchQueries, filter)
	if err != nil {
		return nil, fmt.Errorf("parallel search failed: %w", err)
	}

	log.C(ctx).Infow("Parallel search completed",
		"query_count", len(intentResult.SearchQueries),
		"total_chunks", len(allChunks))

	// 4. 如果检索结果为空，直接返回
	if len(allChunks) == 0 {
		verdict.Reason = "未检索到相关知识"
		return verdict, nil
	}

	// 5. Rerank (仅返回 Top 5-7 的索引)
	rerankedChunks, err := s.rerankChunks(ctx, query, allChunks)
	if err != nil {
		// Rerank 失败时 Fallback 到原始 Top 5
		log.C(ctx).Warnw("Rerank failed, using original top chunks", "error", err)
		if len(allChunks) > 5 {
			rerankedChunks = allChunks[:5]
		} else {
			rerankedChunks = allChunks
		}
	}

	verdict.Evidence = rerankedChunks
	verdict.Reason = fmt.Sprintf("检索到 %d 条知识，Rerank 后保留 %d 条",
		len(allChunks), len(rerankedChunks))

	return verdict, nil
}

// parallelSearch 并行执行多路检索
func (s *SalesRAGService) parallelSearch(
	ctx context.Context,
	queries []string,
	filter port.SearchFilter,
) ([]domain.KnowledgeChunk, error) {
	if len(queries) == 0 {
		return nil, nil
	}

	type searchResult struct {
		chunks []domain.KnowledgeChunk
		err    error
	}

	resultChan := make(chan searchResult, len(queries))
	var wg sync.WaitGroup

	// 并行检索
	for _, q := range queries {
		wg.Add(1)
		go func(query string) {
			defer wg.Done()
			chunks, err := s.store.Search(ctx, query, filter, 10)
			resultChan <- searchResult{chunks: chunks, err: err}
		}(q)
	}

	// 等待所有 goroutine 完成后关闭 channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// 收集结果
	var allChunks []domain.KnowledgeChunk
	seenIDs := make(map[string]bool)

	for result := range resultChan {
		if result.err != nil {
			log.C(ctx).Warnw("Search query failed", "error", result.err)
			continue
		}
		// 去重合并
		for _, chunk := range result.chunks {
			if !seenIDs[chunk.ID] {
				seenIDs[chunk.ID] = true
				allChunks = append(allChunks, chunk)
			}
		}
	}

	return allChunks, nil
}

// rerankChunks 使用 Rerank 模型进行重排序
func (s *SalesRAGService) rerankChunks(
	ctx context.Context,
	query string,
	chunks []domain.KnowledgeChunk,
) ([]domain.KnowledgeChunk, error) {
	if len(chunks) <= 5 {
		return chunks, nil // 太少无需 rerank
	}

	// 准备 documents 列表 (仅 Summary + Content 前 150 字)
	documents := make([]string, len(chunks))
	for i, chunk := range chunks {
		preview := chunk.Content
		if len(preview) > 150 {
			preview = preview[:150] + "..."
		}
		if chunk.Summary != "" {
			documents[i] = fmt.Sprintf("[%s] %s", chunk.Summary, preview)
		} else {
			documents[i] = preview
		}
	}

	// 调用 Rerank API
	indices, err := s.dmxClient.Rerank(ctx, query, documents, 5)
	if err != nil {
		return nil, err
	}

	// 根据索引提取 Chunk
	result := make([]domain.KnowledgeChunk, 0, len(indices))
	for _, idx := range indices {
		if idx >= 0 && idx < len(chunks) {
			result = append(result, chunks[idx])
		}
	}

	log.C(ctx).Infow("Rerank completed",
		"input_count", len(chunks),
		"output_count", len(result))

	return result, nil
}
