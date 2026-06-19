package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ragbiz "numind-server/internal/numind/biz/rag"              // 通用改写器 + 可答性门（标准化，flag 控制）
	sdomain "numind-server/internal/numind/biz/salesrag/domain" // MetaStrategy/BasicStrategy
	sport "numind-server/internal/numind/biz/salesrag/port"     // IntentRouter/IntentType（销售意图，未搬迁）
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/domain"   // KnowledgeChunk 等
	"numind-server/internal/pkg/retrieval/port"     // VectorStore/SearchFilter（已搬迁）
	"numind-server/internal/pkg/retrieval/retrieve" // 领域无关检索主干（底座）
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
	Intent        sport.IntentType `json:"intent,omitempty"`         // 识别的意图
	SearchQueries []string         `json:"search_queries,omitempty"` // 多路搜索词
	ChatMode      string           `json:"chat_mode,omitempty"`      // 对话模式 (sales/free)
	History       []string         `json:"history,omitempty"`        // 对话历史

	// V3 策略引擎扩展
	Strategy       *sdomain.BasicStrategy `json:"strategy,omitempty"`         // 选择的销售策略
	StrategyMetaID string                 `json:"strategy_meta_id,omitempty"` // 综合策略ID

	// V4 知识库分类
	DocCategoryMap map[uint]string `json:"doc_category_map,omitempty"` // 文档ID→分类 (product/case/faq)

	// V5 观点库独立通道
	OpinionEvidence []domain.KnowledgeChunk `json:"opinion_evidence,omitempty"` // 观点库独立检索结果
}

// SalesRAGService 销售智能体 RAG 服务
type SalesRAGService struct {
	store       port.VectorStore
	router      sport.IntentRouter
	strategySvc *StrategyService  // 策略引擎
	retrieveSvc *retrieve.Service // 领域无关检索主干（底座；改写+并行检索+rerank）
}

// NewSalesRAGService 创建新的 SalesRAGService。
//
// 内部把通用检索主干委托给 internal/pkg/retrieval/retrieve 底座：用
// routerRewriter 适配现有 IntentRouter（chatMode 经 context 透传），docStore=nil
// （RetrieveForResponseV2 始终用显式 docIDs scope，不需要 AllEnabled 解析）。
// 签名保持不变，golden 测试与 biz.go 构造点均无需改动。
func NewSalesRAGService(store port.VectorStore, router sport.IntentRouter) *SalesRAGService {
	return &SalesRAGService{
		store:       store,
		router:      router,
		strategySvc: NewStrategyService(),
		// 通用改写器（flag 开时）+ 销售改写器（flag 关时 fallback，保现状逐位一致）；
		// 主通道挂可答性门（flag 控制，关时放行）。standardized——chatbot/agent 同源。
		retrieveSvc: retrieve.NewService(
			store,
			ragbiz.NewFlaggedRewriter(ragbiz.NewUniversalRewriter(), routerRewriter{router: router}),
			nil,
		).WithGate(ragbiz.NewGate()),
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
	// 直接调用 V2 接口，默认使用 sales 模式（无独立观点库通道）
	return s.RetrieveForResponseV2(ctx, query, docIDs, nil, nil, "sales", userID, nil)
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
	opinionDocIDs []uint,
	history []string,
	chatMode string,
	userID uint,
	onStatus func(string),
) (*RetrievalVerdict, error) {

	verdict := &RetrievalVerdict{
		Query:    query,
		ChatMode: chatMode,
		History:  history,
	}

	// 把 per-request chatMode 注入 context，供 routerRewriter.Rewrite 透传给
	// IntentRouter.AnalyzeIntentV2（底座 QueryRewriter 接口领域无关，不含 chatMode）。
	retrieveCtx := withChatMode(ctx, chatMode)

	// 2b. 策略选择（仅 sales 模式启用）——销售专属，留 salesrag、不进底座。
	// 保持与原结构一致：与主通道检索并行执行（无共享状态，结果与串行一致）。
	var wg sync.WaitGroup
	var strategy *sdomain.BasicStrategy
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

	// 1+2a+4. 主通道：底座完成 query 改写(+HyDE) → 多路并行检索去重 → rerank(top5)。
	// 行为忠实等价于原 步骤1(意图分析+HyDE 追加) + 步骤2a(parallelSearch) + 步骤4(rerankChunks)。
	if onStatus != nil {
		onStatus("正在分析您的意图...")
		onStatus("正在检索知识库并重排序...")
	}
	mainResult, err := s.retrieveSvc.Retrieve(retrieveCtx, query, retrieve.Scope{
		UserID:      userID,
		DocumentIDs: docIDs,
	}, retrieve.Options{
		TopK:               10, // 每路召回 limit（对齐原 parallelSearch 写死的 10）
		RerankTopN:         5,  // 常规知识库 rerank 保留 top5（对齐原 rerankChunks）
		RewriteQuery:       true,
		History:            history,
		BillingLabel:       "salesrag_rerank",
		AnswerabilityCheck: true, // 主通道挂可答性门（flag 控制，关时放行）
		// RerankNoFloor=true：关掉"保底 top-1"。所有片段都低于阈值(默认0.3)时返回空，
		// 让下游走 len(Chunks)==0 的"未检索到相关知识"分支，而不是拿一个低相关度的 top-1
		// 硬 grounding（防"在域但库外"——如莫小派别条产品线问题——被当成 iDC 内容乱答）。
		// 依据：iDriveCareer 真实数据评估，0.3 阈值 + no_floor 对硬负例拒答率 1.0、in-KB 召回 0.845。
		// 见 scripts/rag_eval/REAL_DATA_RAG_FINDINGS.md。
		RerankNoFloor: true,
		// 混合检索（dense + BM25 + RRF）：flag 开 + store 支持关键词通道才生效（产品码/精确
		// FAQ 措辞命中），否则底座自动退回纯向量（零回归）。仅主通道开，观点库通道不开。
		Hybrid: ragbiz.HybridRetrievalEnabled(),
		// 重排硬化（passage 清洗 + 去雷同 + 降级链）：flag 开才生效，关=现状（零回归）。仅主通道。
		RerankHardening: ragbiz.RerankHardeningEnabled(),
	})

	// 等待策略选择完成（与主通道并行后汇合，对齐原 wg.Wait 语义）。
	wg.Wait()

	// mainEmpty: 主通道 docIDs 为空（用户可能只配了观点库、没配产品/案例/FAQ）。
	// 原行为：空 docIDs 时 store 静默返回空、不报错，且 opinion 仍独立检索。底座对空
	// scope 改为返回 ErrEmptyScope——这里**不能提前 return**，否则会跳过 opinion 通道
	// （收入路径回归：只配观点库的 session 会丢失全部观点证据）。故标记 mainEmpty 后
	// 继续走 opinion；非 ErrEmptyScope 的真错误才中断。
	mainEmpty := false
	if err != nil {
		if errors.Is(err, retrieve.ErrEmptyScope) {
			mainEmpty = true
			verdict.Reason = "未检索到相关知识"
		} else {
			return nil, fmt.Errorf("parallel search failed: %w", err)
		}
	}

	// 策略赋值（无论主通道是否为空，strategy 已在上面 wg.Wait 处汇合完成；对齐原行为：
	// 原代码主空时也不早返回，strategy 照常赋值）。
	if strategy != nil {
		verdict.Strategy = strategy
		verdict.StrategyMetaID = strategy.MetaID
		log.C(ctx).Infow("Strategy selected",
			"strategy_id", strategy.ID,
			"strategy_name", strategy.Name,
			"meta_id", strategy.MetaID)
	}

	// 3. 主通道结果组装（仅主通道非空时）。allSearchQueries = 实际检索用的 query 列表
	// （含 HyDE）；主通道为空时无改写词（ErrEmptyScope 在 rewrite 前触发）。
	var allSearchQueries []string
	if !mainEmpty {
		allSearchQueries = mainResult.RewriteQueries
		verdict.SearchQueries = allSearchQueries
		if len(allSearchQueries) > 0 {
			verdict.RewriteQuery = allSearchQueries[0]
		}
		if len(mainResult.Chunks) == 0 {
			verdict.Reason = "未检索到相关知识"
		} else {
			verdict.Evidence = mainResult.Chunks
			verdict.Reason = fmt.Sprintf("Rerank 后保留 %d 条", len(mainResult.Chunks))
		}
		log.C(ctx).Infow("Parallel search completed",
			"query_count", len(allSearchQueries),
			"evidence_count", len(mainResult.Chunks),
			"has_strategy", strategy != nil)
	}

	// 2c+5. 观点库独立通道（保留）。主通道有改写词则复用（保 I1，不二次 intent）；
	// 主通道为空（mainEmpty）时无改写词，opinion 自行改写一次——等价于原行为（原本就是
	// 单次 intent 分析喂两通道，主空时该次分析此前未发生，故在此由 opinion 补一次）。
	if len(opinionDocIDs) > 0 {
		opOpts := retrieve.Options{
			TopK:       10,
			RerankTopN: 5, // 观点库统一为 top5（与主通道一致）。观点是补充视角,保底留 1 条
			// （RerankNoFloor 不设=false→全低于阈值仍保 top1,与主通道"拒答"语义有意不同）。
			BillingLabel: "salesrag_rerank_opinion",
		}
		if len(allSearchQueries) > 0 {
			opOpts.RewriteQuery = false
			opOpts.PrewrittenQueries = allSearchQueries // 复用主通道改写词，不二次 intent
		} else {
			opOpts.RewriteQuery = true // 主通道为空，opinion 自行改写一次（等价原单次 intent）
			opOpts.History = history
		}
		opinionResult, opErr := s.retrieveSvc.Retrieve(retrieveCtx, query, retrieve.Scope{
			UserID:      userID,
			DocumentIDs: opinionDocIDs,
		}, opOpts)
		if opErr != nil {
			// 原行为：观点库检索失败仅 warn、不中断主流程。
			if errors.Is(opErr, retrieve.ErrEmptyScope) {
				// opinionDocIDs 非空时不会触发，防御性兜底。
				log.C(ctx).Warnw("Opinion search empty scope, skipping", "error", opErr)
			} else {
				log.C(ctx).Warnw("Opinion search failed, continuing without opinion evidence", "error", opErr)
			}
		} else if len(opinionResult.Chunks) > 0 {
			verdict.OpinionEvidence = opinionResult.Chunks
			// 主通道为空时用 opinion 的改写词补 verdict.SearchQueries（trace 完整性）。
			if mainEmpty && len(verdict.SearchQueries) == 0 {
				verdict.SearchQueries = opinionResult.RewriteQueries
			}
			if verdict.Reason == "" {
				verdict.Reason = fmt.Sprintf("观点库 Rerank 后保留 %d 条", len(opinionResult.Chunks))
			} else {
				verdict.Reason += fmt.Sprintf("，观点库 Rerank 后保留 %d 条", len(opinionResult.Chunks))
			}
		}
	}

	return verdict, nil
}
