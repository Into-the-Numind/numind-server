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
	ChatMode      string          `json:"chat_mode,omitempty"`      // 对话模式 (sales/free)
	History       []string        `json:"history,omitempty"`        // 对话历史

	// V3 策略引擎扩展
	Strategy       *domain.BasicStrategy `json:"strategy,omitempty"`         // 选择的销售策略
	StrategyMetaID string                `json:"strategy_meta_id,omitempty"` // 综合策略ID

	// V4 知识库分类
	DocCategoryMap map[uint]string `json:"doc_category_map,omitempty"` // 文档ID→分类 (product/case/faq)
}

// SalesRAGService 销售智能体 RAG 服务
type SalesRAGService struct {
	store       port.VectorStore
	router      port.IntentRouter
	dmxClient   *adapter.DMXAPIClient
	strategySvc *StrategyService // 策略引擎
}

// NewSalesRAGService 创建新的 SalesRAGService
func NewSalesRAGService(store port.VectorStore, router port.IntentRouter) *SalesRAGService {
	return &SalesRAGService{
		store:       store,
		router:      router,
		dmxClient:   adapter.NewDMXAPIClient(),
		strategySvc: NewStrategyService(),
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
	userID uint,
) (*RetrievalVerdict, error) {
	// 直接调用 V2 接口，默认使用 sales 模式
	return s.RetrieveForResponseV2(ctx, query, docIDs, nil, "sales", userID)
}

// RetrieveForResponseV2 V2 版检索流程
// 1. 意图识别 + 多路 Query 生成 (LLM: qwen-turbo-latest)
// 2. 并行全库检索 + 策略选择
// 3. 聚合去重
// 4. LLM Rerank (qwen3-rerank) - 返回 Top N 索引
// 5. 组装最终 Evidence + Strategy
// chatMode: "sales" (销售话术模式) 或 "free" (自由讨论模式)
func (s *SalesRAGService) RetrieveForResponseV2(
	ctx context.Context,
	query string,
	docIDs []uint,
	history []string,
	chatMode string,
	userID uint,
) (*RetrievalVerdict, error) {

	// 1. 意图分析 + Query 生成 (LLM: qwen-turbo-latest)
	intentResult, err := s.router.AnalyzeIntentV2(ctx, query, history, chatMode)
	if err != nil {
		return nil, fmt.Errorf("intent analysis failed: %w", err)
	}

	verdict := &RetrievalVerdict{
		Query:         query,
		SearchQueries: intentResult.SearchQueries,
		ChatMode:      chatMode,
		History:       history,
	}

	// 设置 RewriteQuery 为第一个搜索词（兼容 V1）
	if len(intentResult.SearchQueries) > 0 {
		verdict.RewriteQuery = intentResult.SearchQueries[0]
	}

	// 将 HyDE Query 追加到搜索列表（如果存在）
	// 最终列表：原始 Query + 3 个普通改写 + 1 个 HyDE = 最多 5 路并行检索
	allSearchQueries := make([]string, len(intentResult.SearchQueries))
	copy(allSearchQueries, intentResult.SearchQueries)
	if intentResult.HyDEQuery != "" {
		allSearchQueries = append(allSearchQueries, intentResult.HyDEQuery)
		log.C(ctx).Infow("HyDE query added to search list",
			"hyde_query_len", len(intentResult.HyDEQuery),
			"total_queries", len(allSearchQueries))
	}

	// 2. 并行执行：RAG 检索 + 策略选择
	var wg sync.WaitGroup
	var allChunks []domain.KnowledgeChunk
	var chunksErr error
	var strategy *domain.BasicStrategy

	// 2a. 并行 - RAG 检索（使用包含 HyDE 的完整搜索列表）
	wg.Add(1)
	go func() {
		defer wg.Done()
		filter := port.SearchFilter{
			UserID:      userID,
			DocumentIDs: docIDs,
		}
		allChunks, chunksErr = s.parallelSearch(ctx, allSearchQueries, filter)
	}()

	// 2b. 并行 - 策略选择（仅在 sales 模式下启用）
	if chatMode == "sales" && s.strategySvc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var strategyErr error
			strategy, strategyErr = s.strategySvc.DetermineStrategy(ctx, query, history)
			if strategyErr != nil {
				log.C(ctx).Warnw("Strategy selection failed", "error", strategyErr)
			}
		}()
	}

	wg.Wait()

	// 处理检索错误
	if chunksErr != nil {
		return nil, fmt.Errorf("parallel search failed: %w", chunksErr)
	}

	// 设置策略结果
	if strategy != nil {
		verdict.Strategy = strategy
		verdict.StrategyMetaID = strategy.MetaID
		log.C(ctx).Infow("Strategy selected",
			"strategy_id", strategy.ID,
			"strategy_name", strategy.Name,
			"meta_id", strategy.MetaID)
	}

	log.C(ctx).Infow("Parallel search completed",
		"query_count", len(intentResult.SearchQueries),
		"total_chunks", len(allChunks),
		"has_strategy", strategy != nil)

	// 3. 如果检索结果为空，直接返回
	if len(allChunks) == 0 {
		verdict.Reason = "未检索到相关知识"
		return verdict, nil
	}

	// 4. Rerank (仅返回 Top 5-7 的索引)
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

// rerankScoreThreshold Rerank 分数截断阈值
// 低于此阈值的结果将被丢弃（最少保留 1 条）
const rerankScoreThreshold = 0.3

// rerankChunks 使用 Rerank 模型进行重排序，并按分数截断
func (s *SalesRAGService) rerankChunks(
	ctx context.Context,
	query string,
	chunks []domain.KnowledgeChunk,
) ([]domain.KnowledgeChunk, error) {
	if len(chunks) <= 1 {
		return chunks, nil // 只有 1 条无需 rerank
	}

	// 准备 documents 列表 (发送全量 Content)
	documents := make([]string, len(chunks))
	for i, chunk := range chunks {
		documents[i] = chunk.Content
	}

	// 调用 Rerank API（返回索引+分数）
	rerankResults, err := s.dmxClient.Rerank(ctx, query, documents, 5)
	if err != nil {
		return nil, err
	}

	// 根据索引提取 Chunk，用 Rerank Score 覆写原始余弦相似度
	// 同时应用分数截断：score >= 0.3 的保留，最少 1 条，最多 5 条
	result := make([]domain.KnowledgeChunk, 0, len(rerankResults))
	for i, rr := range rerankResults {
		if rr.Index < 0 || rr.Index >= len(chunks) {
			continue
		}
		// 第一条始终保留（保底），后续按阈值筛选
		if i > 0 && rr.Score < rerankScoreThreshold {
			continue
		}
		chunk := chunks[rr.Index]
		chunk.Score = float32(rr.Score) // 用 Rerank Score 覆写，前端展示此分数
		result = append(result, chunk)
	}

	// 安全兜底：至少返回 1 条
	if len(result) == 0 && len(rerankResults) > 0 {
		bestIdx := rerankResults[0].Index
		if bestIdx >= 0 && bestIdx < len(chunks) {
			chunk := chunks[bestIdx]
			chunk.Score = float32(rerankResults[0].Score)
			result = append(result, chunk)
		}
	}

	log.C(ctx).Infow("Rerank completed",
		"input_count", len(chunks),
		"output_count", len(result),
		"threshold", rerankScoreThreshold,
		"top_score", func() float64 {
			if len(rerankResults) > 0 {
				return rerankResults[0].Score
			}
			return 0
		}())

	return result, nil
}
