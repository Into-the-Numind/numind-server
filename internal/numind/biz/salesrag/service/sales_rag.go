package service

import (
	"context"
	"fmt"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
)

// RetrievalVerdict 检索判决结果
type RetrievalVerdict struct {
	Query        string                  `json:"query"`         // 原始问题
	RewriteQuery string                  `json:"rewrite_query"` // 改写后的检索词
	IsChitChat   bool                    `json:"is_chit_chat"`  // 是否为闲聊
	Reason       string                  `json:"reason"`        // 逻辑判断依据
	Evidence     []domain.KnowledgeChunk `json:"evidence"`      // 检索到的证据
	Answer       string                  `json:"answer"`        // AI生成的最终回复
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

// Search 基础检索
func (s *SalesRAGService) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	return s.store.Search(ctx, query, filter, limit)
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

	// 3. 业务路径：统一检索
	finalQuery := query
	if rewrite != "" {
		finalQuery = rewrite
	}

	filter := port.SearchFilter{
		SalesStages: []domain.SalesStage{stage},
		DocumentIDs: docIDs,
	}

	// 如果是 DISCOVERY 阶段，也许不需要强制过滤 Stage?
	// 但为了保持原有 Strategy 逻辑，我们加上 Stage 过滤。
	// 不过既然去掉了 DocType，也许我们应该放宽一些？
	// 简单起见，我们先做单次检索，取 Top 8

	evidence, err := s.store.Search(ctx, finalQuery, filter, 8)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}
	verdict.Evidence = evidence

	return verdict, nil
}
