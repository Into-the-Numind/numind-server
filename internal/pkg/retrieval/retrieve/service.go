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
	"regexp"
	"sort"
	"strings"
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

// RRF（Reciprocal Rank Fusion）融合参数（WeKnora 默认值）。
//
//	fused_score = rrfVecWeight/(rrfK + rankVec) + rrfKwWeight/(rrfK + rankKw)
//
// rank 为 1-based 位次（每路各自排名）；只出现在一路的 chunk 只得该路那一项。
// k=60 削弱头部位次差异、平滑两路尺度；向量权重 > 关键词权重（语义为主、关键词补术语命中）。
const (
	rrfK         = 60.0
	rrfVecWeight = 0.7
	rrfKwWeight  = 0.3
)

// Service 领域无关检索主干。
type Service struct {
	store    port.VectorStore
	rewriter port.QueryRewriter     // 可为 nil → 不改写
	docStore DocStore               // 可为 nil → AllEnabled 不可用
	gate     port.AnswerabilityGate // 可为 nil → 不做可答性判定（见 WithGate）
}

// WithGate 注入可答性门，返回自身以便链式调用。仅当 opts.AnswerabilityCheck=true 时生效。
func (s *Service) WithGate(g port.AnswerabilityGate) *Service {
	s.gate = g
	return s
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

	// 3b. 混合检索（opts.Hybrid 且 store 实现 KeywordSearcher）：在 dense 结果之外加跑一路
	// BM25 关键词检索，按 RRF 融合。任一条件不满足（flag 关 / store 无关键词能力 / 关键词
	// 无结果）→ 保持 dense 结果原序原分不变（零回归）。融合在 rerank 之前，rerank 步骤不变。
	if opts.Hybrid {
		if ks, ok := s.store.(port.KeywordSearcher); ok {
			// 关键词通道用**原始 query**（非改写词）：改写/HyDE 是为语义召回服务的，会冲淡
			// 用户原话里的精确术语/产品码——而关键词通道的价值正是命中这些字面 token。
			kwChunks, kwErr := ks.SearchKeyword(ctx, query, filter, opts.TopK)
			if kwErr != nil {
				// 接口约定关键词异常应返回 (nil,nil)；真出错也只 warn + 退回 dense，绝不杀检索。
				log.C(ctx).Warnw("keyword search errored, using dense-only", "error", kwErr)
			} else if len(kwChunks) > 0 && len(chunks) > 0 {
				before := len(chunks)
				chunks = fuseRRF(chunks, kwChunks, opts.TopK)
				log.C(ctx).Infow("hybrid RRF fusion applied",
					"dense_count", before, "keyword_count", len(kwChunks), "fused_count", len(chunks))
			} else if len(chunks) == 0 && len(kwChunks) > 0 {
				// 单检索器旁路（dense 空）：用 keyword 一路原样（截到 TopK），不丢弃。
				if opts.TopK > 0 && len(kwChunks) > opts.TopK {
					kwChunks = kwChunks[:opts.TopK]
				}
				chunks = kwChunks
			}
			// 单检索器旁路：dense 或 keyword 任一为空 → 不融合，保非空的一路原样（含 dense 原序原分）。
		}
	}

	// 4. rerank（RerankTopN>0 才走；失败 fallback 原 topN，与 sales_rag.go 一致）
	if opts.RerankTopN > 0 && len(chunks) > 0 {
		reranked, rerankErr := s.rerankWithLimit(ctx, query, chunks, opts.RerankTopN, "Rerank", opts.BillingLabel, opts)
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

	// 5. 可答性门（与阈值解耦的拒答）：opts 开启 + 配置了 gate + 有 chunk 时，
	// 判"资料能否回答 query"；不能 → 清空（拒答）。fail-open：门内部已保证出错时放行。
	if opts.AnswerabilityCheck && s.gate != nil && len(chunks) > 0 {
		canAnswer, reason, gateErr := s.gate.CanAnswer(ctx, query, chunks)
		if gateErr != nil {
			log.C(ctx).Warnw("answerability gate errored, keeping chunks (fail-open)", "error", gateErr)
		} else if !canAnswer {
			log.C(ctx).Infow("answerability gate refused: clearing chunks", "query", query, "reason", reason, "had_chunks", len(chunks))
			chunks = nil
		}
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
//   - 阈值过滤 + 保底交由 applyRerankFilter（纯函数，可单测）。
//
// billingLabel 作参数（不同调用点传不同 label 做成本归因），与 sales_rag.go 设计一致。
// opts 透传阈值/保底配置（RerankMinScore / RerankNoFloor）；默认值 → salesrag 现状不变。
func (s *Service) rerankWithLimit(
	ctx context.Context,
	query string,
	chunks []domain.KnowledgeChunk,
	topN int,
	label string,
	billingLabel string,
	opts Options,
) ([]domain.KnowledgeChunk, error) {
	if len(chunks) <= 1 {
		return chunks, nil
	}

	documents := make([]string, len(chunks))
	for i, chunk := range chunks {
		// 重排硬化：清洗 passage 去除 markdown/表格/链接/公式噪声再喂 reranker
		// （只清洗打分输入，返回的 chunk.Content 不变）。flag 关 → 原文，零回归。
		if opts.RerankHardening {
			documents[i] = cleanPassageForRerank(chunk.Content)
		} else {
			documents[i] = chunk.Content
		}
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

	result := applyRerankFilter(chunks, rerankResp.Results, opts)

	// 重排硬化：MMR-lite 去雷同（trigram Jaccard 相似度 > 阈值的近重复块剔除），
	// 避免返回多条几乎一样的内容（如 3 条雷同产品介绍）。flag 关 → 不去重，零回归。
	if opts.RerankHardening {
		result = dedupDiverse(result, rerankDedupSimThreshold)
	}

	log.C(ctx).Infow(label+" completed",
		"input_count", len(chunks),
		"output_count", len(result),
		"threshold", rerankThreshold(opts),
		"no_floor", opts.RerankNoFloor,
		"top_score", func() float64 {
			if len(rerankResp.Results) > 0 {
				return rerankResp.Results[0].Score
			}
			return 0
		}())

	return result, nil
}

// rerankThreshold 解析生效的 rerank 分阈值：
// opts.RerankMinScore > 0 时用它（chatbot 通用问答传 0.6 丢低相关度），
// 否则用 rerankScoreThreshold 常量 0.3（salesrag 现状，逐位一致）。
func rerankThreshold(opts Options) float64 {
	if opts.RerankMinScore > 0 {
		return float64(opts.RerankMinScore)
	}
	return rerankScoreThreshold
}

// applyRerankFilter 是 rerank 结果的纯过滤逻辑（无 I/O，可单测）。
//
// 行为（默认 Options → 与 sales_rag.go 原内联逻辑逐位一致）：
//   - 按 rerank 结果顺序遍历，越界 Index 跳过；
//   - 第一条（i==0）始终保留（保底），后续 score < threshold 丢弃；
//   - 命中的 chunk 写入 rerank score；
//   - 结果全空时兜底取 Results[0] 1 条。
//
// 新增配置（chatbot 通用问答用）：
//   - threshold 由 rerankThreshold(opts) 解析（RerankMinScore>0 抬高至 0.6）；
//   - opts.RerankNoFloor=true 时**不保底**：第一条同样受阈值约束，全部低于阈值 → 返回空
//     （chatbot 召回全是垃圾时返回空 → 走纯聊天而非 grounding 在低相关度内容上）。
func applyRerankFilter(
	chunks []domain.KnowledgeChunk,
	results []aiservice.RerankResult,
	opts Options,
) []domain.KnowledgeChunk {
	threshold := rerankThreshold(opts)

	// 重排硬化（floor 模式）：纯阈值过滤 → 0 结果则阈值×0.7 重试 → 仍空则 top-1 floor
	// 仅当 ≥0.15（否则返回空，不 ground 在垃圾上）。NoFloor 模式不走此路（空=故意不 grounding）。
	//
	// 注意（live 可达性）：当前生产主通道（chatbot / salesrag main）均用 RerankNoFloor=true
	// （chatbot 0.6 阈值是 iDriveCareer 数据校准过的严格拒答，故意不放松），故本降级链在生产
	// 路径**不触发**——它为 floor-mode 调用方保留（如 rag-eval A/B / 未来需"尽量给点 grounding
	// 但拒绝<0.15 垃圾"的通道）。NoFloor 通道的 live 硬化收益来自 passage 清洗 + dedupDiverse
	// 去雷同（二者在 rerankWithLimit 中对所有 hardening 调用生效，与 NoFloor 无关）。
	if opts.RerankHardening && !opts.RerankNoFloor {
		if sel := filterByThreshold(chunks, results, threshold); len(sel) > 0 {
			return sel
		}
		if sel := filterByThreshold(chunks, results, threshold*rerankRetryFactor); len(sel) > 0 {
			return sel
		}
		if len(results) > 0 {
			bestIdx := results[0].Index
			if bestIdx >= 0 && bestIdx < len(chunks) && results[0].Score >= rerankTop1FloorMin {
				chunk := chunks[bestIdx]
				chunk.Score = float32(results[0].Score)
				return []domain.KnowledgeChunk{chunk}
			}
		}
		return nil
	}

	// --- 现状行为（hardening 关 / NoFloor），逐位一致 ---
	result := make([]domain.KnowledgeChunk, 0, len(results))
	for i, rr := range results {
		if rr.Index < 0 || rr.Index >= len(chunks) {
			continue
		}
		// 保底语义：默认（!NoFloor）下第一条始终保留；NoFloor=true 时第一条也受阈值约束。
		keepFloor := i == 0 && !opts.RerankNoFloor
		if !keepFloor && rr.Score < threshold {
			continue
		}
		chunk := chunks[rr.Index]
		chunk.Score = float32(rr.Score)
		result = append(result, chunk)
	}

	// 安全兜底：至少返回 1 条（仅默认保底模式；NoFloor=true 时允许返回空）。
	if len(result) == 0 && !opts.RerankNoFloor && len(results) > 0 {
		bestIdx := results[0].Index
		if bestIdx >= 0 && bestIdx < len(chunks) {
			chunk := chunks[bestIdx]
			chunk.Score = float32(results[0].Score)
			result = append(result, chunk)
		}
	}

	return result
}

// --- 重排硬化辅助（feature flag features.rerank_hardening.enabled）---

const (
	rerankDedupSimThreshold = 0.82 // trigram Jaccard 相似度 > 此值视为雷同块（去重）
	rerankRetryFactor       = 0.7  // 0 结果时阈值降级系数
	rerankTop1FloorMin      = 0.15 // top-1 兜底的最低分（低于则不强塞）
)

// filterByThreshold 纯阈值过滤（无 keepFloor 自动保留首条），用于硬化降级链。
func filterByThreshold(chunks []domain.KnowledgeChunk, results []aiservice.RerankResult, threshold float64) []domain.KnowledgeChunk {
	out := make([]domain.KnowledgeChunk, 0, len(results))
	for _, rr := range results {
		if rr.Index < 0 || rr.Index >= len(chunks) {
			continue
		}
		if rr.Score < threshold {
			continue
		}
		chunk := chunks[rr.Index]
		chunk.Score = float32(rr.Score)
		out = append(out, chunk)
	}
	return out
}

var (
	reMdImage  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)              // ![alt](url)
	reMdLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)             // [text](url) → text
	reMath     = regexp.MustCompile(`\$\$[^$]*\$\$`)                     // 仅 $$显示公式$$（不碰单 $ 价格如 $100）
	reMdHeader = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]*`)           // # 标题号
	reMdEmph   = regexp.MustCompile("[*_~`]+")                           // 强调/代码反引号
	reTableSep = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t:|-]+\|?[ \t]*$`) // 表格分隔行 ---|---
	reInlineWS = regexp.MustCompile(`[ \t]+`)
	reMultiNL  = regexp.MustCompile(`\n{2,}`)
)

// cleanPassageForRerank 去除 markdown/表格/链接/公式噪声，得到更干净的纯文本喂给
// reranker 模型打分。**只用于 rerank 输入**，不改返回的 chunk.Content。
func cleanPassageForRerank(s string) string {
	s = domain.StripContextJoinMarker(s) // 剥旧切块器遗留的 [上下文衔接] 标记
	s = reMdImage.ReplaceAllString(s, " ")
	s = reMdLink.ReplaceAllString(s, "$1") // 保留链接锚文本
	s = reMath.ReplaceAllString(s, " ")
	s = reTableSep.ReplaceAllString(s, "") // 先去表格分隔行（整行）
	s = strings.ReplaceAll(s, "|", " ")    // 再去表格竖线
	s = reMdHeader.ReplaceAllString(s, "")
	s = reMdEmph.ReplaceAllString(s, "")
	s = reInlineWS.ReplaceAllString(s, " ")
	s = reMultiNL.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

// trigramSet 返回字符串的 rune 三元组集合（用于轻量文本相似度）。
func trigramSet(s string) map[string]struct{} {
	r := []rune(s)
	m := make(map[string]struct{})
	for i := 0; i+3 <= len(r); i++ {
		m[string(r[i:i+3])] = struct{}{}
	}
	return m
}

// trigramJaccard 计算两段文本的 trigram Jaccard 相似度 [0,1]。
func trigramJaccard(a, b string) float64 {
	ta, tb := trigramSet(a), trigramSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	if len(ta) > len(tb) {
		ta, tb = tb, ta
	}
	inter := 0
	for k := range ta {
		if _, ok := tb[k]; ok {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// dedupDiverse 按输入（rerank）顺序贪心保留，剔除与已保留块相似度 > simThreshold 的近重复
// （MMR-lite 多样性）。保持原相对顺序与分数。
func dedupDiverse(chunks []domain.KnowledgeChunk, simThreshold float64) []domain.KnowledgeChunk {
	if len(chunks) <= 1 {
		return chunks
	}
	kept := make([]domain.KnowledgeChunk, 0, len(chunks))
	for _, c := range chunks {
		dup := false
		for _, k := range kept {
			if trigramJaccard(c.Content, k.Content) > simThreshold {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, c)
		}
	}
	return kept
}

// fuseRRF 用 Reciprocal Rank Fusion 融合 dense 与 keyword 两路检索结果。纯函数（无 I/O），
// 便于单测。
//
// 算法：
//   - 每路按入参顺序赋 1-based 位次（rank=1 最相关）；
//   - 每个 chunk 的融合分 = rrfVecWeight/(rrfK+rankVec) + rrfKwWeight/(rrfK+rankKw)；
//     只出现在一路的 chunk 仅累加该路那一项；
//   - 按 chunk.ID 去重（同 ID 视为同一 chunk，dense 的元数据优先保留）；
//   - 按融合分降序排序（稳定排序保证同分时确定性），取前 topK。
//
// 单检索器旁路由调用方负责（dense/keyword 任一为空时根本不调本函数）。这里只处理两路都非空
// 的真融合场景，但对其中一个入参为空仍能正确退化（等价于仅另一路按位次打分）。
//
// 注意：融合后写入 chunk.Score 为 RRF 分（量纲与向量/BM25 不同）。这是有意的——下游 rerank
// 会以 Content 重新打分覆盖 Score，RRF 分只决定进入 rerank 的候选集与其顺序。
func fuseRRF(dense, keyword []domain.KnowledgeChunk, topK int) []domain.KnowledgeChunk {
	type fused struct {
		chunk domain.KnowledgeChunk
		score float64
		// order 记录首次出现的全局顺序，作为同分时的稳定 tiebreaker（dense 优先于 keyword）。
		order int
	}

	merged := make(map[string]*fused)
	orderSeq := 0

	accumulate := func(list []domain.KnowledgeChunk, weight float64) {
		for i, c := range list {
			rank := i + 1 // 1-based
			contrib := weight / (rrfK + float64(rank))
			if f, ok := merged[c.ID]; ok {
				f.score += contrib
			} else {
				merged[c.ID] = &fused{chunk: c, score: contrib, order: orderSeq}
				orderSeq++
			}
		}
	}

	// dense 先累加 → 同 ID 时保留 dense 的元数据（含 dense 算出的 Score 字段，虽随后被覆盖）。
	accumulate(dense, rrfVecWeight)
	accumulate(keyword, rrfKwWeight)

	out := make([]*fused, 0, len(merged))
	for _, f := range merged {
		out = append(out, f)
	}
	// 融合分降序；同分按首次出现顺序升序（稳定、确定性，dense 优先）。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].order < out[j].order
	})

	result := make([]domain.KnowledgeChunk, 0, len(out))
	for _, f := range out {
		c := f.chunk
		c.Score = float32(f.score)
		result = append(result, c)
		if topK > 0 && len(result) >= topK {
			break
		}
	}
	return result
}
