package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// setupWebSearchTestServer creates a mock Tavily server returning the given JSON body
// and sets viper to point at it. Returns cleanup func and server.
func setupWebSearchTestServer(t *testing.T, statusCode int, body string) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	viper.Set("web_search.tavily.api_key", "test-key")
	viper.Set("web_search.tavily.base_url", srv.URL)
	viper.Set("web_search.tavily.timeout_seconds", 5)
	viper.Set("web_search.tavily.cache_ttl_seconds", 300)
	cleanup := func() {
		srv.Close()
		viper.Set("web_search.tavily.api_key", "")
		viper.Set("web_search.tavily.base_url", "")
		viper.Set("web_search.tavily.timeout_seconds", 0)
		viper.Set("web_search.tavily.cache_ttl_seconds", 0)
	}
	return srv, cleanup
}

const tavilyFixtureBody = `{
  "results": [
    {"title":"Title A","url":"https://a.example.com","content":"Snippet A","published_date":"2026-01-01"},
    {"title":"Title B","url":"https://b.example.com","content":"Snippet B"}
  ]
}`

func TestWebSearchTool_HappyPath(t *testing.T) {
	_, cleanup := setupWebSearchTestServer(t, http.StatusOK, tavilyFixtureBody)
	defer cleanup()

	tool := NewWebSearchTool(300)
	input, _ := json.Marshal(webSearchInput{Query: "golang", MaxResults: 2})
	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var out webSearchOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out.Results))
	}
	if out.Results[0].Title != "Title A" {
		t.Errorf("unexpected title: %q", out.Results[0].Title)
	}
	if out.Provider != "tavily" {
		t.Errorf("expected provider 'tavily', got %q", out.Provider)
	}
	if out.CacheHit {
		t.Error("first call should not be a cache hit")
	}
}

func TestWebSearchTool_CacheHit(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tavilyFixtureBody))
	}))
	defer srv.Close()
	viper.Set("web_search.tavily.api_key", "test-key")
	viper.Set("web_search.tavily.base_url", srv.URL)
	viper.Set("web_search.tavily.timeout_seconds", 5)
	defer func() {
		viper.Set("web_search.tavily.api_key", "")
		viper.Set("web_search.tavily.base_url", "")
		viper.Set("web_search.tavily.timeout_seconds", 0)
	}()

	tool := NewWebSearchTool(300)
	input, _ := json.Marshal(webSearchInput{Query: "cache test", MaxResults: 3})
	ctx := context.Background()

	// First call — cache miss.
	res1, err := tool.Execute(ctx, ToolInput(input))
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	var out1 webSearchOutput
	_ = json.Unmarshal(res1, &out1)
	if out1.CacheHit {
		t.Error("first call should not be a cache hit")
	}

	// Second call — should be served from cache.
	res2, err := tool.Execute(ctx, ToolInput(input))
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	var out2 webSearchOutput
	_ = json.Unmarshal(res2, &out2)
	if !out2.CacheHit {
		t.Error("second call should be a cache hit")
	}

	if callCount != 1 {
		t.Errorf("expected exactly 1 HTTP call, got %d", callCount)
	}
}

func TestWebSearchTool_EmptyQuery(t *testing.T) {
	tool := NewWebSearchTool(300)
	input, _ := json.Marshal(webSearchInput{Query: ""})
	_, err := tool.Execute(context.Background(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is empty") {
		t.Errorf("expected 'query is empty' in error, got: %v", err)
	}
}

// TestWebSearchTool_MaxResultsOutOfRange replaces the old MaxResultsClamped test.
// P1-1: out-of-range max_results must return an error, not silently clamp.
func TestWebSearchTool_MaxResultsOutOfRange(t *testing.T) {
	cases := []struct {
		name       string
		maxResults int
	}{
		{"zero", 0},
		{"negative", -1},
		{"above max", 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewWebSearchTool(300)
			input, _ := json.Marshal(webSearchInput{Query: "test", MaxResults: tc.maxResults})
			_, err := tool.Execute(context.Background(), ToolInput(input))
			if err == nil {
				t.Fatalf("max_results=%d: expected error, got nil", tc.maxResults)
			}
			if !strings.Contains(err.Error(), "max_results") {
				t.Errorf("max_results=%d: expected 'max_results' in error, got: %v", tc.maxResults, err)
			}
		})
	}
}

func TestWebSearchTool_InvalidInputJSON(t *testing.T) {
	tool := NewWebSearchTool(300)
	_, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestWebSearchTool_Metadata(t *testing.T) {
	tool := NewWebSearchTool(300)
	if !tool.IsReadOnly() {
		t.Error("web_search should be read-only")
	}
	if !tool.IsSearchOrReadCommand() {
		t.Error("web_search should be a search command")
	}
	if !tool.AlwaysLoad() {
		t.Error("web_search should always load")
	}
	if tool.Name() != "web_search" {
		t.Errorf("unexpected name: %q", tool.Name())
	}
}
