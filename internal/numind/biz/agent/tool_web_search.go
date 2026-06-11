package agent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
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
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// UnmarshalJSON tolerates loosely-typed tool arguments from the model. LLMs
// frequently emit a numeric parameter as a JSON string ("5") or a float (5.0)
// instead of an integer (5). With a strict int field that mismatch is a hard
// unmarshal error, and a hard error from a tool kills the entire agent run
// (dev run 132, 2026-06-11: the model sent max_results="5" on its 6th search and
// the whole task died after 5 healthy searches). Coerce max_results here so a
// model's type wobble never kills a working run. Genuinely out-of-range or
// non-numeric values still fail — surfaced as a soft, retryable error in Execute.
func (in *webSearchInput) UnmarshalJSON(data []byte) error {
	// Alias prevents infinite recursion into this method; max_results stays raw so
	// it can be accepted as either a JSON number or a JSON string.
	var raw struct {
		Query          string          `json:"query"`
		MaxResults     json.RawMessage `json:"max_results"`
		AllowedDomains []string        `json:"allowed_domains,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	in.Query = raw.Query
	in.AllowedDomains = raw.AllowedDomains
	n, err := coerceJSONInt(raw.MaxResults)
	if err != nil {
		return fmt.Errorf("max_results: %w", err)
	}
	in.MaxResults = n
	return nil
}

// coerceJSONInt parses a raw JSON value that should be an integer but may arrive
// as a JSON number (5), a quoted string ("5"), or a float (5.0 / "5.0"). Empty or
// JSON null yields 0, which the caller's range check treats as "unset". A
// non-numeric value returns an error so the caller can surface a soft message.
func coerceJSONInt(raw json.RawMessage) (int, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, nil
	}
	// Unwrap a JSON string ("5" / "5.0") to its inner text.
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, err
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return 0, nil
		}
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	// Accept floats like 5.0 — some models round-trip ints through float. Round to
	// the nearest integer (5.9 → 6) rather than truncating; the downstream 1-10
	// range check still rejects anything that lands out of range (e.g. 0.3 → 0).
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(math.Round(f)), nil
	}
	return 0, fmt.Errorf("not an integer: %q", s)
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

	// All input-validation failures below return a SOFT error (a tool result
	// carrying the message, nil Go error) instead of a hard error. A hard error
	// from a tool propagates as a NodeRunError and kills the entire agent run; a
	// soft error feeds the message back into the ReAct loop so the model retries
	// with corrected arguments and the run survives. This mirrors the provider-error
	// path below (dev run 132 hardening, 2026-06-11).
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return t.returnSoftError("web_search: invalid input: %v", err)
	}
	if in.Query == "" {
		return t.returnSoftError("web_search: query is empty")
	}
	if in.MaxResults < 1 || in.MaxResults > 10 {
		return t.returnSoftError("web_search: max_results 必须在 1-10 之间（收到 %d）", in.MaxResults)
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
		// 软拒绝机制：把 provider 错误喂回模型，避免直接抛 Go fatal 错误引发 Eino 智能体 run 崩溃。
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
