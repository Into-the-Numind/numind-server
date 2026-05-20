package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/salesrag/service"
)

// mockKbRetriever implements the narrow kbRetriever interface.
type mockKbRetriever struct {
	calledQuery  string
	calledDocIDs []uint
	result       *service.RetrievalVerdict
	err          error
}

func (m *mockKbRetriever) Retrieve(_ context.Context, query string, docIDs []uint) (*service.RetrievalVerdict, error) {
	m.calledQuery = query
	m.calledDocIDs = docIDs
	return m.result, m.err
}

func TestKbSearchTool_Execute_PassesQueryAndDocIDs(t *testing.T) {
	mock := &mockKbRetriever{
		result: &service.RetrievalVerdict{
			Query:  "test query",
			Answer: "test answer",
		},
	}
	tool := &kbSearchTool{rag: mock}

	input, _ := json.Marshal(kbSearchInput{
		Query:  "test query",
		DocIDs: []uint{1, 2, 3},
	})

	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if mock.calledQuery != "test query" {
		t.Errorf("expected query 'test query', got %q", mock.calledQuery)
	}
	if len(mock.calledDocIDs) != 3 {
		t.Errorf("expected 3 docIDs, got %d", len(mock.calledDocIDs))
	}

	var verdict service.RetrievalVerdict
	if err := json.Unmarshal(result, &verdict); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if verdict.Answer != "test answer" {
		t.Errorf("unexpected answer: %s", verdict.Answer)
	}
}

func TestKbSearchTool_Execute_PropagatesError(t *testing.T) {
	mock := &mockKbRetriever{err: errors.New("rag error")}
	tool := &kbSearchTool{rag: mock}

	input, _ := json.Marshal(kbSearchInput{Query: "q"})
	_, err := tool.Execute(context.Background(), ToolInput(input))
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

func TestKbSearchTool_Execute_BadJSON(t *testing.T) {
	tool := &kbSearchTool{rag: &mockKbRetriever{}}
	_, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err == nil {
		t.Error("expected JSON unmarshal error")
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
