package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/biz/salesrag/service"
)

// kbRetriever is the subset of salesrag.SalesRAGBiz used by kbSearchTool.
// Keeping a narrow interface here makes the tool testable without a full SalesRAGBiz mock.
type kbRetriever interface {
	Retrieve(ctx context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error)
}

type kbSearchTool struct {
	BaseTool
	rag kbRetriever
}

type kbSearchInput struct {
	Query  string `json:"query"`
	DocIDs []uint `json:"doc_ids,omitempty"`
}

var _ FullTool = (*kbSearchTool)(nil)

func (t *kbSearchTool) Name() string { return "kb_search" }
func (t *kbSearchTool) Description() string {
	return "Search the knowledge base. Input: { query: string, doc_ids?: number[] }. Returns relevant document snippets."
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
			"doc_ids": {"type": "array", "items": {"type": "integer", "minimum": 0}, "description": "Optional list of document IDs to restrict the search to."}
		},
		"required": ["query"]
	}`)
}

func (t *kbSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in kbSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	// Note: SalesRAGBiz.Retrieve internally reads userID from context via
	// middleware.UserIDFromCtx(ctx); ctx injection is handled by the runner (Task 7).
	verdict, err := t.rag.Retrieve(ctx, in.Query, in.DocIDs)
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(verdict)
	return ToolResult(out), nil
}
