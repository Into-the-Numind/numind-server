package agent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/langfuse"
)

// webSearchInput is the LLM-facing input schema for web_search.
type webSearchInput struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// webSearchOutput is the JSON shape returned to the LLM.
type webSearchOutput struct {
	Results  []aiservice.WebSearchResult `json:"results"`
	CacheHit bool                        `json:"cache_hit"`
	Provider string                      `json:"provider"`
}

// cacheEntry holds a cached WebSearchResponse and its expiry time.
type cacheEntry struct {
	Response *aiservice.WebSearchResponse
	Expiry   time.Time
}

// webSearchTool implements FullTool for the "web_search" agent tool.
// It calls the Tavily provider via aiservice.WebSearch and maintains an
// in-memory TTL cache (default 5 min, configurable via viper) to avoid
// duplicate API calls for identical queries.
type webSearchTool struct {
	BaseTool
	ttl     time.Duration
	cache   map[string]cacheEntry
	cacheMu sync.RWMutex
}

var _ FullTool = (*webSearchTool)(nil)

// NewWebSearchTool constructs a webSearchTool.
// ttlSeconds comes from viper("web_search.tavily.cache_ttl_seconds"); 0 → 300 s default.
func NewWebSearchTool(ttlSeconds int) *webSearchTool {
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return &webSearchTool{
		ttl:   time.Duration(ttlSeconds) * time.Second,
		cache: make(map[string]cacheEntry),
	}
}

// NewWebSearchToolFromConfig reads the TTL from viper and calls NewWebSearchTool.
func NewWebSearchToolFromConfig() *webSearchTool {
	ttl := viper.GetInt("web_search.tavily.cache_ttl_seconds")
	return NewWebSearchTool(ttl)
}

// ── FullTool identity ──

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web for real-time information. Input: { query: string, max_results?: number (1-10, default 5), allowed_domains?: string[] }. Returns relevant web search results."
}
func (t *webSearchTool) UserFacingName() string      { return "网络搜索" }
func (t *webSearchTool) NarrationVerb() string       { return "搜索网络" }
func (t *webSearchTool) IsReadOnly() bool            { return true }
func (t *webSearchTool) IsSearchOrReadCommand() bool { return true }
func (t *webSearchTool) AlwaysLoad() bool            { return true }

// Execute validates input, checks cache, calls Tavily via aiservice, and caches the result.
func (t *webSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("web_search: invalid input: %w", err)
	}
	if in.Query == "" {
		return nil, fmt.Errorf("web_search: query is empty")
	}
	if in.MaxResults <= 0 || in.MaxResults > 10 {
		in.MaxResults = 5
	}

	key := webSearchCacheKey(in)

	// Langfuse Span — tool layer is responsible for tracing; aiservice layer is not.
	var (
		spanID  string
		traceID string
	)
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID = tc.TraceID
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(traceID, spanID, "tool.web_search.execute",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(in),
		)
		defer func() {
			langfuse.EndSpan(traceID, spanID)
		}()
	}

	// Cache check.
	if cached, ok := t.cacheGet(key); ok {
		out, _ := json.Marshal(webSearchOutput{
			Results:  cached.Results,
			CacheHit: true,
			Provider: cached.Provider,
		})
		return ToolResult(out), nil
	}

	// Provider call.
	resp, err := aiservice.WebSearch(ctx, aiservice.WebSearchRequest{
		Query:          in.Query,
		MaxResults:     in.MaxResults,
		AllowedDomains: in.AllowedDomains,
	})
	if err != nil {
		return nil, fmt.Errorf("web_search: provider error: %w", err)
	}

	// Cache the fresh result.
	t.cachePut(key, resp)

	out, _ := json.Marshal(webSearchOutput{
		Results:  resp.Results,
		CacheHit: false,
		Provider: resp.Provider,
	})
	return ToolResult(out), nil
}

// cacheGet returns a cached response if the key exists and has not expired.
func (t *webSearchTool) cacheGet(key string) (*aiservice.WebSearchResponse, bool) {
	t.cacheMu.RLock()
	entry, ok := t.cache[key]
	t.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.Expiry) {
		return nil, false
	}
	return entry.Response, true
}

// cachePut stores a response under key with the configured TTL expiry.
// A crude cap of 1000 entries evicts the entire cache when exceeded to bound memory.
func (t *webSearchTool) cachePut(key string, resp *aiservice.WebSearchResponse) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	// Crude eviction: drop all entries when cap is reached.
	if len(t.cache) >= 1000 {
		t.cache = make(map[string]cacheEntry)
	}
	t.cache[key] = cacheEntry{
		Response: resp,
		Expiry:   time.Now().Add(t.ttl),
	}
}

// webSearchCacheKey derives a deterministic MD5 key from the query + max_results + sorted domains.
func webSearchCacheKey(in webSearchInput) string {
	h := md5.New()
	_, _ = h.Write([]byte(in.Query))
	_, _ = h.Write([]byte(fmt.Sprintf("|%d|", in.MaxResults)))
	sortedDomains := append([]string{}, in.AllowedDomains...)
	sort.Strings(sortedDomains)
	_, _ = h.Write([]byte(strings.Join(sortedDomains, ",")))
	return hex.EncodeToString(h.Sum(nil))
}
