package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/viper"
)

// WebSearchRequest is the input to WebSearch (provider-agnostic).
type WebSearchRequest struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// WebSearchResult is a single search result.
type WebSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
}

// WebSearchResponse wraps results + cache_hit + provider name.
// Cache detection happens at the caller layer (tool_web_search) — this function
// always calls the provider and CacheHit is always false from this layer.
type WebSearchResponse struct {
	Results  []WebSearchResult `json:"results"`
	CacheHit bool              `json:"cache_hit"`
	Provider string            `json:"provider"`
}

// WebSearch is the unified entry point for web search. v1 routes to Tavily.
// Business callers (tool_web_search) are responsible for cache logic.
func WebSearch(ctx context.Context, req WebSearchRequest) (*WebSearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("aiservice.WebSearch: query is empty")
	}
	if req.MaxResults <= 0 || req.MaxResults > 10 {
		req.MaxResults = 5
	}
	return callTavily(ctx, req)
}

// tavilyRequest is the JSON body sent to POST https://api.tavily.com/search.
type tavilyRequest struct {
	APIKey         string   `json:"api_key"`
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	IncludeDomains []string `json:"include_domains,omitempty"`
}

// tavilyResultItem mirrors one element of Tavily's "results" array.
type tavilyResultItem struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`        // Tavily calls it "content", we expose as Snippet
	PublishedDate string `json:"published_date"` // ISO 8601 or empty
}

// tavilyResponse is the top-level Tavily API response shape.
type tavilyResponse struct {
	Results []tavilyResultItem `json:"results"`
}

// callTavily makes a single HTTP POST to the Tavily search endpoint.
// API key and base URL are read from viper at call time (config hot-reload friendly).
// A configurable context timeout is applied; the caller's ctx deadline is respected first.
func callTavily(ctx context.Context, req WebSearchRequest) (*WebSearchResponse, error) {
	apiKey := viper.GetString("web_search.tavily.api_key")
	baseURL := viper.GetString("web_search.tavily.base_url")
	timeoutSec := viper.GetInt("web_search.tavily.timeout_seconds")

	if baseURL == "" {
		baseURL = "https://api.tavily.com"
	}
	if timeoutSec <= 0 {
		timeoutSec = 5
	}

	// Apply timeout — the caller's context deadline wins if it is tighter.
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	payload := tavilyRequest{
		APIKey:         apiKey,
		Query:          req.Query,
		MaxResults:     req.MaxResults,
		IncludeDomains: req.AllowedDomains,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("callTavily: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("callTavily: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("callTavily: http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("callTavily: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("callTavily: status %d: %s", resp.StatusCode, string(respBytes))
	}

	var tavilyResp tavilyResponse
	if err := json.Unmarshal(respBytes, &tavilyResp); err != nil {
		return nil, fmt.Errorf("callTavily: decode response: %w", err)
	}

	results := make([]WebSearchResult, 0, len(tavilyResp.Results))
	for _, r := range tavilyResp.Results {
		results = append(results, WebSearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Content,
			PublishedAt: r.PublishedDate,
		})
	}

	return &WebSearchResponse{
		Results:  results,
		CacheHit: false,
		Provider: "tavily",
	}, nil
}
