// Package retrieve 提供领域无关的检索主干 RetrievalService。
//
// 它从 internal/numind/biz/salesrag/service/sales_rag.go 抽出**通用主干**：
//   - 步骤1 query 改写 + HyDE 追加（QueryRewriter）
//   - 步骤2a 多路并行检索 + 按 chunk.ID 去重合并（parallelSearch）
//   - 步骤4 topN 截断 + 阈值过滤 + 保底 1 条的 rerank（rerankWithLimit）
//
// 销售专属分支（strategy / opinion 第二通道 / RetrievalVerdict 销售字段）**不在此包**，
// 留在 salesrag，由其在底座外层包裹。本包**零销售概念**。
//
// 行为与 sales_rag.go 主干**忠实等价**——dedup 顺序、rerank 阈值/topN/保底、
// query 顺序逐位一致，以保证 T1.6 salesrag 改调底座后同 query → 同 top-K chunk_id。
package retrieve

import (
	"context"
	"fmt"
	"sync"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
)

// rerankScoreThreshold Rerank 分数截断阈值。
// 低于此阈值的结果将被丢弃（最少保留 1 条）。
// 数值与 sales_rag.go 的同名常量一致（0.3），保 T1.6 逐位一致。
const rerankScoreThreshold = 0.3

// Service 领域无关检索主干。
type Service struct {
	store    port.VectorStore
	rewriter port.QueryRewriter // 可为 nil → 不改写
	docStore DocStore           // 可为 nil → AllEnabled 不可用
}

// NewService 创建检索服务。rewriter 与 docStore 均可为 nil：
//   - rewriter 为 nil 时，opts.RewriteQuery 不生效（fallback 原 query）；
//   - docStore 为 nil 时，scope.AllEnabled 不可用（返回错误）。
func NewService(store port.VectorStore, rewriter port.QueryRewriter, docStore DocStore) *Service {
	return &Service{
		store:    store,
		rewriter: rewriter,
		docStore: docStore,
	}
}

// Retrieve 执行检索主干。逻辑忠实复制 sales_rag.go 通用主干：
//  1. 解析 scope → docIDs（AllEnabled 走 docStore；两者皆空 → ErrEmptyScope）
//  2. 确定 queries（PrewrittenQueries 优先 → rewriter → fallback 原 query）
//  3. parallelSearch 多路扇出 + 按 chunk.ID 去重
//  4. 可选 rerank（topN 截断 + 阈值 0.3 + 保底 1 条；失败 fallback 原 topN）
func (s *Service) Retrieve(ctx context.Context, query string, scope Scope, opts Options) (*RetrievalResult, error) {
	// 1. 解析 scope → docIDs
	docIDs, err := s.resolveScope(ctx, scope)
	if err != nil {
		return nil, err
	}

	// 2. 确定 queries（含 HyDE 追加，顺序与 sales_rag.go 一致）
	queries, err := s.determineQueries(ctx, query, opts)
	if err != nil {
		return nil, err
	}

	// 3. parallelSearch（filter = UserID + 解析后的 docIDs，limit = opts.TopK）
	filter := port.SearchFilter{
		UserID:      scope.UserID,
		DocumentIDs: docIDs,
	}
	chunks, err := s.parallelSearch(ctx, queries, filter, opts.TopK)
	if err != nil {
		return nil, fmt.Errorf("parallel search failed: %w", err)
	}

	// 4. rerank（RerankTopN>0 才走；失败 fallback 原 topN，与 sales_rag.go 一致）
	if opts.RerankTopN > 0 && len(chunks) > 0 {
		reranked, rerankErr := s.rerankWithLimit(ctx, query, chunks, opts.RerankTopN, "Rerank", opts.BillingLabel)
		if rerankErr != nil {
			// Rerank 失败时 Fallback 到原始 Top N（对齐 sales_rag.go 的降级语义）
			log.C(ctx).Warnw("Rerank failed, using original top chunks", "error", rerankErr)
			if len(chunks) > opts.RerankTopN {
				reranked = chunks[:opts.RerankTopN]
			} else {
				reranked = chunks
			}
		}
		chunks = reranked
	}

	return &RetrievalResult{
		Chunks:         chunks,
		RewriteQueries: queries,
	}, nil
}

// resolveScope 将 Scope 解析为具体的 docIDs。
// AllEnabled 优先（走 docStore），否则用 DocumentIDs；两者皆空 → ErrEmptyScope。
func (s *Service) resolveScope(ctx context.Context, scope Scope) ([]uint, error) {
	if scope.AllEnabled {
		if s.docStore == nil {
			return nil, fmt.Errorf("retrieval: scope.AllEnabled requires a DocStore but none configured")
		}
		ids, err := s.docStore.ListEnabledDocIDs(ctx, scope.UserID)
		if err != nil {
			return nil, fmt.Errorf("list enabled doc ids: %w", err)
		}
		return ids, nil
	}
	if len(scope.DocumentIDs) == 0 {
		return nil, ErrEmptyScope
	}
	return scope.DocumentIDs, nil
}

// determineQueries 决定实际用于检索的 query 列表。
//
// 优先级（与 sales_rag.go RetrieveForResponseV2 步骤1 一致）：
//  1. opts.PrewrittenQueries 非空 → 直接复用（跳过 rewriter，保 I1）；
//  2. opts.RewriteQuery && rewriter != nil → 调 rewriter，得 Queries + HyDE，
//     HyDE 非空则 append 到末尾（顺序：改写 queries... + HyDE，与 sales_rag.go 一致）；
//  3. 否则 fallback 为 []string{query}。
func (s *Service) determineQueries(ctx context.Context, query string, opts Options) ([]string, error) {
	if len(opts.PrewrittenQueries) > 0 {
		return opts.PrewrittenQueries, nil
	}

	if opts.RewriteQuery && s.rewriter != nil {
		res, err := s.rewriter.Rewrite(ctx, query, opts.History)
		if err != nil {
			return nil, fmt.Errorf("query rewrite failed: %w", err)
		}
		// 复制改写 queries，再追加 HyDE（顺序与 sales_rag.go allSearchQueries 一致）
		queries := make([]string, len(res.Queries))
		copy(queries, res.Queries)
		if res.HyDE != "" {
			queries = append(queries, res.HyDE)
			log.C(ctx).Infow("HyDE query added to search list",
				"hyde_query_len", len(res.HyDE),
				"total_queries", len(queries))
		}
		return queries, nil
	}

	return []string{query}, nil
}

// parallelSearch 并行执行多路检索，按 chunk.ID 去重合并。
//
// 忠实抽自 sales_rag.go parallelSearch（约 260-323）：
//   - 每路 goroutine 调 store.Search(query, filter, limit)；
//   - 结果经 channel 收集，按 chunk.ID 去重（seenIDs），保持首次出现顺序；
//   - 全部失败才返回错误，部分失败仅 warn（避免静默降级 / 保留可用结果）。
//
// 唯一参数化差异：sales_rag.go 写死 limit=10，这里用 limit 入参
// （T1.6 salesrag 改调时传 10，保逐位一致）。
func (s *Service) parallelSearch(
	ctx context.Context,
	queries []string,
	filter port.SearchFilter,
	limit int,
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
			chunks, err := s.store.Search(ctx, query, filter, limit)
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
	var failCount int
	var lastErr error

	for result := range resultChan {
		if result.err != nil {
			failCount++
			lastErr = result.err
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

	// 所有查询都失败时返回错误，避免静默降级
	if failCount == len(queries) && lastErr != nil {
		return nil, fmt.Errorf("all %d search queries failed: %w", failCount, lastErr)
	}

	return allChunks, nil
}

// rerankWithLimit 通用 Rerank：按 topN 截断 + 分数阈值过滤 + 兜底至少 1 条。
//
// 忠实抽自 sales_rag.go rerankWithLimit（约 334-423）。逐位一致点：
//   - len(chunks) <= 1 直接返回；
//   - 注入 userID + billing + skip-legacy-billing 上下文；
//   - Langfuse rerank span（name "rerank"，input/output 同构）；
//   - 调 aiservice.Rerank(ctx, profile.SalesragRerank, ...)；
//   - 第一条始终保留（保底），后续 score < 0.3 丢弃；
//   - 结果全空时取 Results[0] 兜底 1 条。
//
// billingLabel 作参数（不同调用点传不同 label 做成本归因），与 sales_rag.go 设计一致。
func (s *Service) rerankWithLimit(
	ctx context.Context,
	query string,
	chunks []domain.KnowledgeChunk,
	topN int,
	label string,
	billingLabel string,
) ([]domain.KnowledgeChunk, error) {
	if len(chunks) <= 1 {
		return chunks, nil
	}

	documents := make([]string, len(chunks))
	for i, chunk := range chunks {
		documents[i] = chunk.Content
	}

	// 注入 aiservice 上下文：userID + skip-legacy-billing
	if uid, ok := middleware.UserIDFromCtx(ctx); ok && uid > 0 {
		ctx = aismw.WithUserID(ctx, uid)
		ctx = billing.WithBilling(ctx, uid, billingLabel)
	}
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// Langfuse rerank span
	var rerankSpanID, rerankTraceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		rerankSpanID = langfuse.SpanID()
		rerankTraceID = tc.TraceID
		langfuse.CreateSpan(tc.TraceID, rerankSpanID, "rerank",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]interface{}{"query": query, "doc_count": len(documents), "topN": topN}),
		)
		ctx = langfuse.WithTraceAndParent(ctx, tc.TraceID, rerankSpanID)
	}

	// 通过 AI Gateway 调用 Rerank（profile.SalesragRerank）
	rerankResp, err := aiservice.Rerank(ctx, profile.SalesragRerank, aiservice.RerankRequest{
		Query:     query,
		Documents: documents,
		TopN:      topN,
	})
	if rerankSpanID != "" {
		if err != nil {
			langfuse.EndSpan(rerankTraceID, rerankSpanID, langfuse.WithSpanError(err.Error()))
		} else {
			langfuse.EndSpan(rerankTraceID, rerankSpanID, langfuse.WithSpanOutput(map[string]interface{}{"result_count": len(rerankResp.Results)}))
		}
	}
	if err != nil {
		return nil, err
	}

	result := make([]domain.KnowledgeChunk, 0, len(rerankResp.Results))
	for i, rr := range rerankResp.Results {
		if rr.Index < 0 || rr.Index >= len(chunks) {
			continue
		}
		// 第一条始终保留（保底），后续按阈值筛选
		if i > 0 && rr.Score < rerankScoreThreshold {
			continue
		}
		chunk := chunks[rr.Index]
		chunk.Score = float32(rr.Score)
		result = append(result, chunk)
	}

	// 安全兜底：至少返回 1 条
	if len(result) == 0 && len(rerankResp.Results) > 0 {
		bestIdx := rerankResp.Results[0].Index
		if bestIdx >= 0 && bestIdx < len(chunks) {
			chunk := chunks[bestIdx]
			chunk.Score = float32(rerankResp.Results[0].Score)
			result = append(result, chunk)
		}
	}

	log.C(ctx).Infow(label+" completed",
		"input_count", len(chunks),
		"output_count", len(result),
		"threshold", rerankScoreThreshold,
		"top_score", func() float64 {
			if len(rerankResp.Results) > 0 {
				return rerankResp.Results[0].Score
			}
			return 0
		}())

	return result, nil
}
