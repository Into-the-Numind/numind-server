package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/retrieval/retrieve"
)

// kbRetriever is the subset of the retrieval base Service used by kbSearchTool.
// Keeping a narrow interface here makes the tool testable without spinning up a
// real *retrieve.Service (vector store + rewriter + docStore).
//
// T2.2: the tool was reworked to call the retrieval base directly and return the
// raw chunk list — it no longer goes through salesrag's RetrieveForResponse +
// generateAnswer (which ran a second LLM pass to synthesize an answer). The agent
// already reasons over the returned snippets, so the in-tool answer generation was
// pure double-LLM waste.
type kbRetriever interface {
	Retrieve(ctx context.Context, query string, scope retrieve.Scope, opts retrieve.Options) (*retrieve.RetrievalResult, error)
}

type kbSearchTool struct {
	BaseTool
	retriever kbRetriever
}

type kbSearchInput struct {
	Query  string `json:"query"`
	DocIDs []uint `json:"doc_ids,omitempty"`
}

// kbSearchChunk is the per-chunk shape returned to the agent. Field names mirror
// domain.KnowledgeChunk's JSON tags (content / document_id / document_name / score)
// so the payload stays familiar; chunk_id exposes the chunk identifier for citation.
type kbSearchChunk struct {
	ChunkID      string  `json:"chunk_id"`
	Content      string  `json:"content"`
	DocumentID   uint    `json:"document_id"`
	DocumentName string  `json:"document_name,omitempty"`
	Score        float32 `json:"score,omitempty"`
}

// kbSearchOutput is the tool's JSON result: a flat list of raw snippets.
type kbSearchOutput struct {
	Chunks []kbSearchChunk `json:"chunks"`
}

var _ FullTool = (*kbSearchTool)(nil)

func (t *kbSearchTool) Name() string { return "kb_search" }
func (t *kbSearchTool) Description() string {
	return "Search the knowledge base. Input: { query: string, doc_ids?: number[] }. " +
		"Omit doc_ids to search all of the user's enabled documents. " +
		"Returns relevant document snippets: { chunks: [{ content, document_id, document_name, score, chunk_id }] }."
}
func (t *kbSearchTool) UserFacingName() string      { return "知识库检索" }
func (t *kbSearchTool) NarrationVerb() string       { return "检索" }
func (t *kbSearchTool) IsReadOnly() bool            { return true }
func (t *kbSearchTool) IsSearchOrReadCommand() bool { return true }
func (t *kbSearchTool) AlwaysLoad() bool            { return true }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *kbSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query":   {"type": "string", "description": "The knowledge-base search query."},
			"doc_ids": {"type": "array", "items": {"type": "integer", "minimum": 0}, "description": "Optional list of document IDs to restrict the search to. Omit to search all enabled documents."}
		},
		"required": ["query"]
	}`)
}

// Execute parses {"query":..., "doc_ids":...} and retrieves matching knowledge-base
// snippets via the retrieval base Service (userID read from context).
//
// Scope resolution (T2.2 decision): a non-empty doc_ids → Scope{DocumentIDs}; an
// empty doc_ids → Scope{AllEnabled:true} (search all of the user's enabled docs).
// The base resolves AllEnabled via its DocStore. Returns the raw chunk list — NOT
// salesrag's Answer/Strategy/Opinion verdict — to avoid the double-LLM pass.
func (t *kbSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in kbSearchInput
	// Model-input and recoverable failures stay soft: a non-nil Go error is a
	// NodeRunError that kills the whole agent run (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("kb_search", "invalid input: %v", err)
	}

	// userID from context (runner injects via middleware.NewContextWithUserID).
	// 0 when absent — the base filters by user_id, so a missing user simply yields
	// no chunks rather than leaking another user's data.
	userID, _ := middleware.UserIDFromCtx(ctx)

	scope := retrieve.Scope{UserID: userID}
	if len(in.DocIDs) > 0 {
		scope.DocumentIDs = in.DocIDs
	} else {
		scope.AllEnabled = true
	}

	opts := retrieve.Options{
		TopK:         10,
		RerankTopN:   10,
		RewriteQuery: false, // agent supplies precise queries; skip rewrite to save an LLM call
		BillingLabel: "agent_kb_search",
	}

	res, err := t.retriever.Retrieve(ctx, in.Query, scope, opts)
	if err != nil {
		// Transient retrieval outage (vector store / ES down) must not kill the
		// run; the LLM can retry or continue without KB grounding. Warn so a
		// sustained outage is still visible to ops (T3 review P1).
		log.Warnw("kb_search: retrieval failed", "error", err)
		return softToolError("kb_search", "retrieval failed: %v", err)
	}

	out := kbSearchOutput{Chunks: make([]kbSearchChunk, 0, len(res.Chunks))}
	for _, c := range res.Chunks {
		out.Chunks = append(out.Chunks, kbSearchChunk{
			ChunkID:      c.ID,
			Content:      c.Content,
			DocumentID:   c.DocumentID,
			DocumentName: c.DocumentName,
			Score:        c.Score,
		})
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return ToolResult(b), nil
}
