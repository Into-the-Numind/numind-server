package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// nativeRerankJSON is the verified DashScope native text-rerank response shape
// (probed against qwen3-rerank on 2026-06-11).
const nativeRerankJSON = `{"output":{"results":[` +
	`{"index":0,"relevance_score":0.867,"document":{"text":"创业要经历四个阶段"}},` +
	`{"index":1,"relevance_score":0.213,"document":{"text":"今天天气不错"}}` +
	`]},"usage":{"total_tokens":49},"request_id":"req-123"}`

func TestAliAdapter_Rerank(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, nativeRerankJSON)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen3-rerank")

	resp, err := a.Rerank(context.Background(), route, aiservice.RerankRequest{
		Query:     "创业的阶段",
		Documents: []string{"创业要经历四个阶段", "今天天气不错"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("Rerank: unexpected error: %v", err)
	}

	// Hits the DashScope native text-rerank path.
	if !strings.HasSuffix(gotPath, "/services/rerank/text-rerank/text-rerank") {
		t.Errorf("request path = %q; want suffix /services/rerank/text-rerank/text-rerank", gotPath)
	}
	// Request uses the nested input/parameters shape (flat would 400 on DashScope).
	if _, ok := gotBody["input"]; !ok {
		t.Errorf("request body missing nested 'input' object; got %v", gotBody)
	}
	if gotBody["model"] != "qwen3-rerank" {
		t.Errorf("request model = %v; want qwen3-rerank", gotBody["model"])
	}

	if len(resp.Results) != 2 {
		t.Fatalf("Results len = %d; want 2", len(resp.Results))
	}
	if resp.Results[0].Index != 0 || resp.Results[0].Score < 0.86 || resp.Results[0].Score > 0.87 {
		t.Errorf("Results[0] = %+v; want index 0 score ~0.867", resp.Results[0])
	}
	if resp.Results[0].Document != "创业要经历四个阶段" {
		t.Errorf("Results[0].Document = %q; want the doc text", resp.Results[0].Document)
	}
	if resp.Provider != "ali" {
		t.Errorf("Provider = %q; want ali", resp.Provider)
	}
	if resp.Model != "qwen3-rerank" {
		t.Errorf("Model = %q; want qwen3-rerank", resp.Model)
	}
}

// TestAliAdapter_Rerank_EmptyDocuments returns an empty response WITHOUT making
// an HTTP call (mirrors dmxapi behavior).
func TestAliAdapter_Rerank_EmptyDocuments(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen3-rerank")

	resp, err := a.Rerank(context.Background(), route, aiservice.RerankRequest{Query: "q", Documents: nil})
	if err != nil {
		t.Fatalf("Rerank empty: unexpected error: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected empty Results, got %d", len(resp.Results))
	}
	if called {
		t.Error("expected NO HTTP call for empty documents")
	}
}

// TestAliAdapter_Rerank_DocumentFallbackToInput verifies that when the provider
// omits document text, we fall back to the original request document by index.
func TestAliAdapter_Rerank_DocumentFallbackToInput(t *testing.T) {
	const noDocJSON = `{"output":{"results":[{"index":1,"relevance_score":0.5}]},"usage":{"total_tokens":3}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, noDocJSON)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen3-rerank")

	resp, err := a.Rerank(context.Background(), route, aiservice.RerankRequest{
		Query:     "q",
		Documents: []string{"doc-zero", "doc-one"},
	})
	if err != nil {
		t.Fatalf("Rerank: unexpected error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Document != "doc-one" {
		t.Errorf("expected fallback document 'doc-one' for index 1; got %+v", resp.Results)
	}
}

// TestAliAdapter_Rerank_ProviderError surfaces a logical provider error (Code set).
func TestAliAdapter_Rerank_ProviderError(t *testing.T) {
	const errJSON = `{"code":"InvalidParameter","message":"bad request","request_id":"r1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, errJSON) // HTTP 200 but logical error
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen3-rerank")

	_, err := a.Rerank(context.Background(), route, aiservice.RerankRequest{Query: "q", Documents: []string{"a"}})
	if err == nil {
		t.Fatal("expected provider error, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidParameter") {
		t.Errorf("expected error to carry provider code; got %v", err)
	}
}
