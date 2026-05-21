package aiservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// tavilyFixtureResponse returns a minimal Tavily-compatible JSON response.
func tavilyFixtureResponse(t *testing.T) string {
	t.Helper()
	resp := tavilyResponse{
		Results: []tavilyResultItem{
			{Title: "Result One", URL: "https://example.com/1", Content: "Snippet one", PublishedDate: "2026-01-01"},
			{Title: "Result Two", URL: "https://example.com/2", Content: "Snippet two"},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// setupViperForTest overrides viper keys to point at the test server and returns
// a cleanup func that restores empty values.
func setupViperForTest(baseURL string) func() {
	viper.Set("web_search.tavily.api_key", "test-key")
	viper.Set("web_search.tavily.base_url", baseURL)
	viper.Set("web_search.tavily.timeout_seconds", 5)
	return func() {
		viper.Set("web_search.tavily.api_key", "")
		viper.Set("web_search.tavily.base_url", "")
		viper.Set("web_search.tavily.timeout_seconds", 0)
	}
}

func TestWebSearch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/search") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tavilyFixtureResponse(t)))
	}))
	defer srv.Close()

	cleanup := setupViperForTest(srv.URL)
	defer cleanup()

	resp, err := WebSearch(context.Background(), WebSearchRequest{Query: "golang testing", MaxResults: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "tavily" {
		t.Errorf("expected provider 'tavily', got %q", resp.Provider)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Result One" {
		t.Errorf("unexpected title: %q", resp.Results[0].Title)
	}
	if resp.Results[0].Snippet != "Snippet one" {
		t.Errorf("expected Snippet 'Snippet one', got %q", resp.Results[0].Snippet)
	}
	if resp.Results[0].PublishedAt != "2026-01-01" {
		t.Errorf("expected PublishedAt '2026-01-01', got %q", resp.Results[0].PublishedAt)
	}
	if resp.CacheHit {
		t.Error("aiservice layer should never return CacheHit=true")
	}
}

func TestWebSearch_EmptyQuery(t *testing.T) {
	_, err := WebSearch(context.Background(), WebSearchRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is empty") {
		t.Errorf("expected 'query is empty' in error, got: %v", err)
	}
}

func TestWebSearch_MaxResultsClamped(t *testing.T) {
	var capturedMaxResults int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedMaxResults = body.MaxResults
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	cleanup := setupViperForTest(srv.URL)
	defer cleanup()

	// max_results=11 should be clamped to 5 by WebSearch before callTavily
	_, err := WebSearch(context.Background(), WebSearchRequest{Query: "test", MaxResults: 11})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedMaxResults != 5 {
		t.Errorf("expected clamped max_results=5, got %d", capturedMaxResults)
	}
}

func TestWebSearch_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	cleanup := setupViperForTest(srv.URL)
	defer cleanup()

	_, err := WebSearch(context.Background(), WebSearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got: %v", err)
	}
}

func TestWebSearch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the configured timeout to trigger context cancellation.
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	// Set 1-second timeout so the test completes quickly.
	viper.Set("web_search.tavily.api_key", "test-key")
	viper.Set("web_search.tavily.base_url", srv.URL)
	viper.Set("web_search.tavily.timeout_seconds", 1)
	defer func() {
		viper.Set("web_search.tavily.api_key", "")
		viper.Set("web_search.tavily.base_url", "")
		viper.Set("web_search.tavily.timeout_seconds", 0)
	}()

	ctx := context.Background()
	_, err := WebSearch(ctx, WebSearchRequest{Query: "timeout test"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// The error should mention context deadline exceeded or similar.
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "deadline") &&
		!strings.Contains(err.Error(), "timeout") && !strings.Contains(strings.ToLower(err.Error()), "eof") {
		t.Logf("timeout error (acceptable): %v", err)
	}
}
