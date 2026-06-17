package admin

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/retrieval/retrieve"
)

// RAGEvalController exposes a read-only retrieval-debug endpoint for the RAG
// evaluation harness (admin-gated, /v1/admin/rag-eval/retrieve). It runs the
// SAME retrieval stack the chatbot uses (retrieve.Service: query rewrite →
// multi-route vector search → rerank) and returns the ranked chunks (doc id +
// score) so an external scoring script can compute recall@k / MRR / nDCG against
// a golden set. NOT used by any production user flow — it is dev/eval tooling.
type RAGEvalController struct {
	retr *retrieve.Service
}

// NewRAGEvalController creates a RAGEvalController over the wired retrieval service.
func NewRAGEvalController(retr *retrieve.Service) *RAGEvalController {
	return &RAGEvalController{retr: retr}
}

type ragEvalRetrieveReq struct {
	Query       string `json:"query" binding:"required"`
	UserID      uint   `json:"user_id"`
	DocumentIDs []uint `json:"document_ids"`
	AllEnabled  bool   `json:"all_enabled"`
	// Retrieval knobs — fully caller-controlled so the scoring script can pick
	// the measurement mode. TopK/RerankTopN default to the chatbot candidate
	// pool (10) when 0. RerankMinScore/RerankNoFloor/RewriteQuery pass through
	// verbatim: to mirror the production chatbot pass {rewrite_query:false,
	// rerank_min_score:0.6, rerank_no_floor:true}; for raw ranked recall pass
	// {rerank_min_score:0, rerank_no_floor:false}. NOTE: when RerankMinScore=0
	// the service still applies its 0.3 default floor (not "no floor").
	TopK           int     `json:"top_k"`
	RerankTopN     int     `json:"rerank_top_n"`
	RerankMinScore float32 `json:"rerank_min_score"`
	RerankNoFloor  bool    `json:"rerank_no_floor"`
	RewriteQuery   bool    `json:"rewrite_query"`
}

type ragEvalChunk struct {
	DocumentID   uint    `json:"document_id"`
	DocumentName string  `json:"document_name"`
	Score        float32 `json:"score"`
	Preview      string  `json:"preview"`
}

// Retrieve runs retrieval for one query and returns the ranked chunks.
func (ctl *RAGEvalController) Retrieve(c *gin.Context) {
	var req ragEvalRetrieveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%v", err), nil)
		return
	}
	if ctl.retr == nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("retrieval service not wired"), nil)
		return
	}
	// 数据隔离守卫:all_enabled 必须显式指定 user_id,避免误扫 user_id=0(系统默认用户)的全部文档。
	if req.AllEnabled && req.UserID == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("user_id required when all_enabled=true"), nil)
		return
	}

	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.RerankTopN <= 0 {
		req.RerankTopN = 10
	}

	scope := retrieve.Scope{UserID: req.UserID, DocumentIDs: req.DocumentIDs, AllEnabled: req.AllEnabled}
	opts := retrieve.Options{
		TopK:           req.TopK,
		RerankTopN:     req.RerankTopN,
		RerankMinScore: req.RerankMinScore,
		RerankNoFloor:  req.RerankNoFloor,
		RewriteQuery:   req.RewriteQuery,
		BillingLabel:   "rag_eval",
	}

	res, err := ctl.retr.Retrieve(c.Request.Context(), req.Query, scope, opts)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("retrieve: %v", err), nil)
		return
	}

	out := make([]ragEvalChunk, 0, len(res.Chunks))
	for _, ch := range res.Chunks {
		preview := ch.Content
		if r := []rune(preview); len(r) > 80 {
			preview = string(r[:80])
		}
		out = append(out, ragEvalChunk{
			DocumentID:   ch.DocumentID,
			DocumentName: ch.DocumentName,
			Score:        ch.Score,
			Preview:      preview,
		})
	}
	core.WriteResponse(c, nil, gin.H{"chunks": out, "rewrite_queries": res.RewriteQueries})
}
