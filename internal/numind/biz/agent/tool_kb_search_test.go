package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/retrieve"
)

// mockKbRetriever implements the narrow kbRetriever interface (T2.2: retrieval base).
type mockKbRetriever struct {
	calledQuery string
	calledScope retrieve.Scope
	calledOpts  retrieve.Options
	result      *retrieve.RetrievalResult
	err         error
}

func (m *mockKbRetriever) Retrieve(_ context.Context, query string, scope retrieve.Scope, opts retrieve.Options) (*retrieve.RetrievalResult, error) {
	m.calledQuery = query
	m.calledScope = scope
	m.calledOpts = opts
	return m.result, m.err
}

func TestKbSearchTool_Execute_ReturnsRawChunks(t *testing.T) {
	mock := &mockKbRetriever{
		result: &retrieve.RetrievalResult{
			Chunks: []domain.KnowledgeChunk{
				{ID: "c1", DocumentID: 7, DocumentName: "doc-a", Content: "hello world", Score: 0.91},
				{ID: "c2", DocumentID: 8, Content: "second snippet", Score: 0.42},
			},
		},
	}
	tool := &kbSearchTool{retriever: mock}

	ctx := middleware.NewContextWithUserID(context.Background(), 42)
	input, _ := json.Marshal(kbSearchInput{
		Query:  "test query",
		DocIDs: []uint{1, 2, 3},
	})

	result, err := tool.Execute(ctx, ToolInput(input))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Query + scope propagation.
	if mock.calledQuery != "test query" {
		t.Errorf("expected query 'test query', got %q", mock.calledQuery)
	}
	if mock.calledScope.UserID != 42 {
		t.Errorf("expected scope userID 42, got %d", mock.calledScope.UserID)
	}
	if mock.calledScope.AllEnabled {
		t.Error("non-empty doc_ids must NOT set AllEnabled")
	}
	if len(mock.calledScope.DocumentIDs) != 3 {
		t.Errorf("expected 3 docIDs in scope, got %d", len(mock.calledScope.DocumentIDs))
	}

	// Options: rewrite off, billing label set, sensible topN.
	if mock.calledOpts.RewriteQuery {
		t.Error("agent kb_search should not rewrite the query")
	}
	if mock.calledOpts.BillingLabel != "agent_kb_search" {
		t.Errorf("unexpected billing label: %q", mock.calledOpts.BillingLabel)
	}
	if mock.calledOpts.RerankTopN <= 0 || mock.calledOpts.TopK <= 0 {
		t.Errorf("expected positive TopK/RerankTopN, got TopK=%d RerankTopN=%d", mock.calledOpts.TopK, mock.calledOpts.RerankTopN)
	}

	// Raw chunk list returned — NOT a salesrag verdict (no Answer field).
	var out kbSearchOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(out.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(out.Chunks))
	}
	if out.Chunks[0].ChunkID != "c1" || out.Chunks[0].Content != "hello world" ||
		out.Chunks[0].DocumentID != 7 || out.Chunks[0].DocumentName != "doc-a" || out.Chunks[0].Score != 0.91 {
		t.Errorf("chunk[0] not faithfully mapped: %+v", out.Chunks[0])
	}
	if out.Chunks[1].ChunkID != "c2" || out.Chunks[1].DocumentID != 8 {
		t.Errorf("chunk[1] not faithfully mapped: %+v", out.Chunks[1])
	}

	// Guard against the old double-LLM verdict shape leaking back: the result must
	// NOT contain an "answer" key.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(result, &generic); err != nil {
		t.Fatalf("result is not a JSON object: %v", err)
	}
	if _, ok := generic["answer"]; ok {
		t.Error("result must not contain an 'answer' field (double-LLM removed)")
	}
}

func TestKbSearchTool_Execute_EmptyDocIDsUsesAllEnabled(t *testing.T) {
	mock := &mockKbRetriever{result: &retrieve.RetrievalResult{}}
	tool := &kbSearchTool{retriever: mock}

	ctx := middleware.NewContextWithUserID(context.Background(), 99)
	input, _ := json.Marshal(kbSearchInput{Query: "q"}) // no doc_ids

	if _, err := tool.Execute(ctx, ToolInput(input)); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !mock.calledScope.AllEnabled {
		t.Error("empty doc_ids must set Scope.AllEnabled = true")
	}
	if len(mock.calledScope.DocumentIDs) != 0 {
		t.Errorf("AllEnabled scope must not carry DocumentIDs, got %d", len(mock.calledScope.DocumentIDs))
	}
	if mock.calledScope.UserID != 99 {
		t.Errorf("expected scope userID 99, got %d", mock.calledScope.UserID)
	}
}

func TestKbSearchTool_Execute_PropagatesError(t *testing.T) {
	mock := &mockKbRetriever{err: errors.New("retrieve error")}
	tool := &kbSearchTool{retriever: mock}

	// tool-soft-error-sweep: retrieval outages surface as SOFT errors so the
	// run survives; the payload still carries the failure for the LLM.
	input, _ := json.Marshal(kbSearchInput{Query: "q"})
	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected soft error, got hard error: %v", err)
	}
	if !strings.Contains(string(result), "retrieval failed") {
		t.Errorf("soft error should mention retrieval failure, got: %s", result)
	}
}

func TestKbSearchTool_Execute_BadJSON(t *testing.T) {
	tool := &kbSearchTool{retriever: &mockKbRetriever{}}
	// tool-soft-error-sweep: malformed input is a SOFT error.
	result, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err != nil {
		t.Fatalf("expected soft error for bad JSON, got hard error: %v", err)
	}
	if !strings.Contains(string(result), "invalid input") {
		t.Errorf("soft error should mention invalid input, got: %s", result)
	}
}

// TestKbSearchTool_Execute_MarshalFailureIsSoft reproduces the run-killer: a chunk
// whose Score is NaN/Inf (degenerate similarity/rerank math) makes json.Marshal of
// the success output fail. The old code returned that as a hard Go error, which Eino
// turns into a NodeRunError that terminates the ENTIRE agent run. A marshal failure
// of one result must stay SOFT so the run survives (tool-soft-error-sweep invariant).
func TestKbSearchTool_Execute_MarshalFailureIsSoft(t *testing.T) {
	mock := &mockKbRetriever{
		result: &retrieve.RetrievalResult{
			Chunks: []domain.KnowledgeChunk{
				// NaN float cannot be JSON-encoded → forces json.Marshal to error.
				{ID: "c1", DocumentID: 7, Content: "snippet", Score: float32(math.NaN())},
			},
		},
	}
	tool := &kbSearchTool{retriever: mock}

	input, _ := json.Marshal(kbSearchInput{Query: "q"})
	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected soft error on marshal failure, got hard error (kills run): %v", err)
	}
	if !strings.Contains(string(result), "ERROR") {
		t.Errorf("soft error payload should carry an ERROR marker, got: %s", result)
	}
}

func TestKbSearchTool_Metadata(t *testing.T) {
	tool := &kbSearchTool{}
	if !tool.IsReadOnly() {
		t.Error("kb_search should be read-only")
	}
	if !tool.IsSearchOrReadCommand() {
		t.Error("kb_search should be a search command")
	}
	if !tool.AlwaysLoad() {
		t.Error("kb_search should always load")
	}
}
