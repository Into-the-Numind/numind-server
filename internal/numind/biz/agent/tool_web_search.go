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
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
)

// webSearchInput is the LLM-facing input schema for web_search.
type webSearchInput struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// webSearchOutput is the JSON shape returned to the LLM.
type webSearchOutput struct {
	Results  []aiservice.WebSearchResult `json:"results"`
	CacheHit bool                        `json:"cache_hit"`
	Provider string                      `json:"provider"`
	Error    string                      `json:"error,omitempty"`
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
// P2-1: returns FullTool interface (not *webSearchTool) to keep callers decoupled.
func NewWebSearchTool(ttlSeconds int) FullTool {
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return &webSearchTool{
		ttl:   time.Duration(ttlSeconds) * time.Second,
		cache: make(map[string]cacheEntry),
	}
}

// NewWebSearchToolFromConfig reads the TTL from viper and calls NewWebSearchTool.
// P2-1: returns FullTool interface.
func NewWebSearchToolFromConfig() FullTool {
	ttl := viper.GetInt("web_search.tavily.cache_ttl_seconds")
	return NewWebSearchTool(ttl)
}

// ── FullTool identity ──

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web for real-time information. Input: { query: string, max_results: number (required, 1-10), allowed_domains?: string[] }. Returns relevant web search results."
}
func (t *webSearchTool) UserFacingName() string      { return "网络搜索" }
func (t *webSearchTool) NarrationVerb() string       { return "搜索网络" }
func (t *webSearchTool) IsReadOnly() bool            { return true }
func (t *webSearchTool) IsSearchOrReadCommand() bool { return true }
func (t *webSearchTool) AlwaysLoad() bool            { return true }

func (t *webSearchTool) returnSoftError(format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(webSearchOutput{
		Results:  []aiservice.WebSearchResult{},
		CacheHit: false,
		Provider: "tavily",
		Error:    "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query":           {"type": "string", "description": "The web search query."},
			"max_results":     {"type": "integer", "minimum": 1, "maximum": 10, "description": "Number of results to return; must be between 1 and 10."},
			"allowed_domains": {"type": "array", "items": {"type": "string"}, "description": "Optional whitelist of domains to restrict results to."}
		},
		"required": ["query", "max_results"]
	}`)
}

// Execute validates input, checks cache, calls Tavily via aiservice, and caches the result.
func (t *webSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	start := time.Now()

	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		// P1-2: JSON unmarshal failure → errno.ErrBind
		return nil, errno.ErrBind.SetMessage("web_search: invalid input: %v", err)
	}
	if in.Query == "" {
		return nil, fmt.Errorf("web_search: query is empty")
	}
	// P1-1: out-of-range max_results returns ErrInvalidParameter, not clamp.
	if in.MaxResults < 1 || in.MaxResults > 10 {
		return nil, errno.ErrInvalidParameter.SetMessage("web_search: max_results 必须在 1-10 之间")
	}

	key := webSearchCacheKey(in)

	// Langfuse Span — tool layer is responsible for tracing; aiservice layer is not.
	var (
		spanID   string
		traceID  string
		cacheHit bool
		nResults int
	)
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID = tc.TraceID
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(traceID, spanID, "tool.web_search.execute",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(in),
		)
		// P1-3: emit output metadata (provider/query/results_count/cache_hit/latency_ms) on EndSpan.
		defer func() {
			langfuse.EndSpan(traceID, spanID,
				langfuse.WithSpanOutput(map[string]any{
					"provider":      "tavily",
					"query":         in.Query,
					"results_count": nResults,
					"cache_hit":     cacheHit,
					"latency_ms":    time.Since(start).Milliseconds(),
				}),
			)
		}()
	}

	// Cache check.
	if cached, ok := t.cacheGet(key); ok {
		cacheHit = true
		nResults = len(cached.Results)
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
		// P1-2: wrap provider errors with errno.ErrAIProviderError.
		// 我们采用软拒绝机制，彻底避免直接抛 Go fatal 错误引发 Eino 智能体崩溃崩溃。
		return t.returnSoftError("web_search: provider error: %v", err)
	}

	nResults = len(resp.Results)

	// Cache the fresh result.
	// Note: cachePut uses a full-eviction strategy (drop all at cap=1000).
	// Thundering herd on cache miss is accepted: Tavily calls are idempotent.
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
