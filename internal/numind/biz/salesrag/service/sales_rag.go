package service

import (
	"context"
	"fmt"
	"sync"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
)

// RetrievalVerdict 检索判决结果
type RetrievalVerdict struct {
	Query        string                  `json:"query"`         // 原始问题
	RewriteQuery string                  `json:"rewrite_query"` // 改写后的检索词
	IsChitChat   bool                    `json:"is_chit_chat"`  // 是否为闲聊
	Reason       string                  `json:"reason"`        // 逻辑判断依据
	Facts        []domain.KnowledgeChunk // 事实知识块
	Strategies   []domain.KnowledgeChunk // 策略知识块
	Cases        []domain.KnowledgeChunk // 案例知识块
	Evidence     []domain.KnowledgeChunk `json:"evidence"` // 检索到的证据 (可能是Facts, Strategies, Cases的合并或子集)
}

// SalesRAGService 销售智能体 RAG 服务
type SalesRAGService struct {
	store  port.VectorStore
	router port.IntentRouter
}

func NewSalesRAGService(store port.VectorStore, router port.IntentRouter) *SalesRAGService {
	return &SalesRAGService{
		store:  store,
		router: router,
	}
}

// DeleteByDocumentID 删除指定文档的所有切片
func (s *SalesRAGService) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	return s.store.DeleteByDocumentID(ctx, documentID)
}

// RetrieveForResponse 为生成回复进行检索
func (s *SalesRAGService) RetrieveForResponse(
	ctx context.Context,
	query string,
	stage domain.SalesStage,
	docIDs []uint,
) (*RetrievalVerdict, error) {

	// 1. 意图分析 (历史记录暂空)
	intent, rewrite, err := s.router.AnalyzeIntent(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("analyze intent failed: %w", err)
	}

	verdict := &RetrievalVerdict{
		Query:        query, // Store original query
		RewriteQuery: rewrite,
		Reason:       "Dual-track retrieval executed", // Default reason
	}

	// 2. 快速路径：闲聊
	if intent == port.IntentChitChat {
		verdict.IsChitChat = true
		verdict.Reason = "Chit-chat intent detected"
		return verdict, nil
	}

	// 3. 业务路径：双轨检索
	finalQuery := query
	if rewrite != "" {
		finalQuery = rewrite
	}

	var wg sync.WaitGroup
	var factErr, stratErr error

	// Track 1: 事实检索 (Facts)
	wg.Add(1)
	go func() {
		defer wg.Done()
		filter := port.SearchFilter{
			DocTypes:    []domain.DocType{domain.DocTypeFact, domain.DocTypeCase},
			DocumentIDs: docIDs, // 同时支持指定文档范围
		}
		verdict.Facts, factErr = s.store.Search(ctx, finalQuery, filter, 5)
	}()

	// Track 2: 策略检索 (Strategy)
	wg.Add(1)
	go func() {
		defer wg.Done()
		filter := port.SearchFilter{
			DocTypes:    []domain.DocType{domain.DocTypeStrategy, domain.DocTypeStyle},
			SalesStages: []domain.SalesStage{stage},
			DocumentIDs: docIDs,
		}
		verdict.Strategies, stratErr = s.store.Search(ctx, finalQuery, filter, 3)
	}()

	wg.Wait()

	if factErr != nil {
		return nil, fmt.Errorf("fact retrieval failed: %w", factErr)
	}
	if stratErr != nil {
		return nil, fmt.Errorf("strategy retrieval failed: %w", stratErr)
	}

	return verdict, nil
}
