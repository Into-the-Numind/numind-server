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

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
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
// Business callers (tool_web_search) are responsible for cache logic and
// max_results validation; this layer enforces a belt-and-suspenders guard only.
func WebSearch(ctx context.Context, req WebSearchRequest) (*WebSearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("aiservice.WebSearch: query is empty")
	}
	// P1-1: callers should validate before reaching here; return error rather than clamp.
	if req.MaxResults < 1 || req.MaxResults > 10 {
		return nil, errno.ErrInvalidParameter.SetMessage("web_search: max_results 必须在 1-10 之间")
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

// tavilyHTTPClient is the package-level shared client used by callTavily.
// P2-2: use httpclient.NewClient for connection pooling and configurable timeout,
// instead of http.DefaultClient which has no timeout and uses the default transport.
var tavilyHTTPClient = httpclient.NewClient(&httpclient.Config{
	Timeout:               10 * time.Second,
	ConnectTimeout:        5 * time.Second,
	ResponseHeaderTimeout: 10 * time.Second,
	TLSHandshakeTimeout:   5 * time.Second,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConns:          10,
	MaxIdleConnsPerHost:   5,
	MaxRetries:            0, // Tavily search calls are not retried (idempotent but costly)
	EnableCompression:     true,
	UserAgent:             "numind-server/1.0 (web_search)",
})

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
		// P1-2: marshal failure → errno.ErrBind
		return nil, errno.ErrBind.SetMessage("callTavily: marshal request: %v", err)
	}

	// P2-2: use httpclient.Client.Do instead of http.DefaultClient.
	// No retries: Tavily search calls are idempotent but costly; callers retry at a higher level.
	httpReq := &httpclient.Request{
		Method:      http.MethodPost,
		URL:         baseURL + "/search",
		Headers:     map[string]string{"Content-Type": "application/json"},
		Body:        bytes.NewReader(body),
		Context:     callCtx,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	}
	httpResp, err := tavilyHTTPClient.Do(httpReq)
	if err != nil {
		// P1-2: network/timeout errors → errno.ErrAIProviderError
		return nil, errno.ErrAIProviderError.SetMessage("callTavily: http: %v", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, errno.ErrAIProviderError.SetMessage("callTavily: read body: %v", err)
	}

	if httpResp.StatusCode >= 400 {
		// P1-2: HTTP 4xx/5xx → errno.ErrAIProviderError (message includes status code).
		return nil, errno.ErrAIProviderError.SetMessage("callTavily: status %d: %s", httpResp.StatusCode, string(respBytes))
	}

	var tavilyResp tavilyResponse
	if err := json.Unmarshal(respBytes, &tavilyResp); err != nil {
		// P1-2: JSON decode failure → errno.ErrBind
		return nil, errno.ErrBind.SetMessage("callTavily: decode response: %v", err)
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
