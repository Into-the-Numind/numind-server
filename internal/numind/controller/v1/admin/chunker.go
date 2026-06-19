package admin

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
)

// ChunkerController exposes admin/dev endpoints for the structure-aware chunker
// (RAG upgrade item 1). It allows previewing how a text or document WOULD be
// chunked (no DB writes), and re-indexing an existing document through the
// current ingestion pipeline.
//
// Architecture note: both endpoints are registered on the USER service router
// (internal/numind/router.go) because the retrieval/ingest stack (AI gateway +
// sqlite-vec volume) lives only in the user-service process. They are gated by
// feature flag "features.rag_eval.enabled" + AdminAuthMiddleware, exactly like
// the existing rag-eval block. Admin callers bypass user ownership checks
// (userID=0).
type ChunkerController struct {
	rag salesrag.SalesRAGBiz
}

// NewChunkerController creates a ChunkerController.
func NewChunkerController(rag salesrag.SalesRAGBiz) *ChunkerController {
	return &ChunkerController{rag: rag}
}

type chunkerPreviewReq struct {
	Text       string `json:"text"`
	DocumentID uint   `json:"document_id"`
}

type chunkerReindexReq struct {
	DocumentID  uint   `json:"document_id"`
	DocumentIDs []uint `json:"document_ids"`
	// UserID is the owner of the documents (used for permission check within
	// biz when non-zero). Pass 0 to bypass ownership checks (admin mode).
	UserID uint `json:"user_id"`
}

// Preview handles POST /v1/admin/chunker/preview.
// It previews how a text or a document would be chunked by the structure-aware
// chunker. No DB writes are performed.
func (ctl *ChunkerController) Preview(c *gin.Context) {
	var req chunkerPreviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%v", err), nil)
		return
	}
	if req.Text == "" && req.DocumentID == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("text or document_id required"), nil)
		return
	}

	// userID=0 → admin bypass (no ownership check)
	result, err := ctl.rag.PreviewChunking(c.Request.Context(), 0, salesrag.ChunkPreviewRequest{
		Text:       req.Text,
		DocumentID: req.DocumentID,
	})
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("preview: %v", err), nil)
		return
	}

	core.WriteResponse(c, nil, result)
}

// Reindex handles POST /v1/admin/chunker/reindex.
// It re-chunks and re-embeds one or more documents through the current ingestion
// pipeline (deletes old chunks first, then submits asynchronously).
func (ctl *ChunkerController) Reindex(c *gin.Context) {
	var req chunkerReindexReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%v", err), nil)
		return
	}

	// Collect all document IDs from both fields.
	seen := make(map[uint]struct{})
	var ids []uint
	if req.DocumentID != 0 {
		if _, ok := seen[req.DocumentID]; !ok {
			seen[req.DocumentID] = struct{}{}
			ids = append(ids, req.DocumentID)
		}
	}
	for _, id := range req.DocumentIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("document_id or document_ids required"), nil)
		return
	}

	ctx := c.Request.Context()
	var submitted []uint
	errs := make(map[string]string)

	for _, id := range ids {
		if err := ctl.rag.ReindexDocument(ctx, req.UserID, id); err != nil {
			errs[fmt.Sprint(id)] = err.Error()
		} else {
			submitted = append(submitted, id)
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"submitted": submitted,
		"errors":    errs,
	})
}
