// Package crawl4ai is a thin HTTP client for a self-hosted crawl4ai render
// service (https://github.com/unclecode/crawl4ai). It renders a URL with a real
// browser (JavaScript executed) and returns LLM-ready Markdown.
//
// It is NOT an LLM/AI-model call (the `fit` content filter is heuristic, not
// `llm`), so it deliberately lives outside the aiservice gateway — it is plain
// web-fetch infrastructure, sibling to internal/pkg/httpclient.
//
// SSRF: callers MUST validate the target URL (DNS + IP safety) BEFORE calling
// RenderMarkdown. This client does not re-validate — the render path's residual
// SSRF risk (redirect chains, DNS rebinding inside crawl4ai) is mitigated at the
// deployment layer by network-isolating the crawl4ai container.
package crawl4ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/httpclient"
)

const defaultTimeoutSeconds = 30

// RenderResult is the outcome of a successful render.
type RenderResult struct {
	Title      string
	Markdown   string
	StatusCode int // the target page's HTTP status as reported by crawl4ai
}

// Client calls a crawl4ai service's POST /crawl endpoint.
type Client struct {
	baseURL       string
	token         string
	contentFilter string // "fit" (default) or "raw" — preference when both markdown forms present
	http          *httpclient.Client
}

// NewClientFromConfig builds a Client from viper crawl4ai.* keys. It always
// returns a non-nil *Client; when base_url is empty, Configured() reports false
// and the caller should skip the render path (pure raw-HTTP fallback). Reading
// config at construction is intentional: base_url is deploy-time infra config,
// not a per-request value.
func NewClientFromConfig() *Client {
	baseURL := strings.TrimRight(viper.GetString("crawl4ai.base_url"), "/")
	token := viper.GetString("crawl4ai.token")
	filter := viper.GetString("crawl4ai.content_filter")
	if filter == "" {
		filter = "fit"
	}
	timeoutSec := viper.GetInt("crawl4ai.timeout_seconds")
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSeconds
	}
	total := time.Duration(timeoutSec) * time.Second
	// Connect timeout must not exceed the total timeout (e.g. timeout_seconds=3).
	connectTimeout := 5 * time.Second
	if total < connectTimeout {
		connectTimeout = total
	}
	tlsTimeout := 10 * time.Second
	if total < tlsTimeout {
		tlsTimeout = total
	}

	hc := httpclient.NewClient(&httpclient.Config{
		Timeout:               total,
		ConnectTimeout:        connectTimeout,
		ResponseHeaderTimeout: total,
		TLSHandshakeTimeout:   tlsTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		MaxRetries:            0, // rendering is expensive: fail → fall back, do not retry
		EnableCompression:     true,
		UserAgent:             "numind-server/1.0 (crawl4ai)",
	})

	return &Client{baseURL: baseURL, token: token, contentFilter: filter, http: hc}
}

// Configured reports whether a crawl4ai base URL is set. When false the caller
// must skip the render path entirely.
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != ""
}

// crawlRequest mirrors the crawl4ai POST /crawl request body. Non-primitive
// config values use crawl4ai's {"type": ClassName, "params": {...}} envelope.
type crawlRequest struct {
	URLs          []string       `json:"urls"`
	BrowserConfig map[string]any `json:"browser_config"`
	CrawlerConfig map[string]any `json:"crawler_config"`
}

// crawlResponse is the top-level POST /crawl response.
type crawlResponse struct {
	Success bool          `json:"success"`
	Results []crawlResult `json:"results"`
}

// crawlResult is one element of crawlResponse.results. Markdown is captured as
// RawMessage because crawl4ai returns it as EITHER a plain string OR an object
// {raw_markdown, fit_markdown} depending on version/config (S3 review P2).
type crawlResult struct {
	URL        string          `json:"url"`
	StatusCode int             `json:"status_code"`
	Markdown   json.RawMessage `json:"markdown"`
	Metadata   struct {
		Title string `json:"title"`
	} `json:"metadata"`
}

// markdownObject is the object form of the markdown field.
type markdownObject struct {
	RawMarkdown string `json:"raw_markdown"`
	FitMarkdown string `json:"fit_markdown"`
}

// RenderMarkdown POSTs targetURL to {base_url}/crawl and returns the rendered
// Markdown + title. targetURL MUST already be SSRF-validated by the caller.
//
// Any failure (network/timeout, non-2xx, body-read error, success=false, no
// results, empty markdown) returns a non-nil error so the caller can fall back
// to raw HTTP. It never panics.
func (c *Client) RenderMarkdown(ctx context.Context, targetURL string) (*RenderResult, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("crawl4ai: not configured")
	}

	payload := crawlRequest{
		URLs:          []string{targetURL},
		BrowserConfig: map[string]any{"type": "BrowserConfig", "params": map[string]any{"headless": true}},
		CrawlerConfig: map[string]any{"type": "CrawlerRunConfig", "params": map[string]any{"cache_mode": "bypass"}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("crawl4ai: marshal request: %w", err)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}

	req := &httpclient.Request{
		Method:  http.MethodPost,
		URL:     c.baseURL + "/crawl",
		Headers: headers,
		Body:    bytes.NewReader(body),
		Context: ctx,
		// Explicit: without this, Do() applies DefaultRetryPolicy (MaxRetries=3),
		// silently overriding the Config-level MaxRetries:0 (S3 review P2).
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("crawl4ai: http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("crawl4ai: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("crawl4ai: status %d: %s", resp.StatusCode, truncateForErr(respBytes))
	}

	var cr crawlResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return nil, fmt.Errorf("crawl4ai: decode response: %w", err)
	}
	if !cr.Success || len(cr.Results) == 0 {
		return nil, fmt.Errorf("crawl4ai: render unsuccessful (success=%v, results=%d)", cr.Success, len(cr.Results))
	}

	r := cr.Results[0]
	mdText := c.extractMarkdown(r.Markdown)
	if strings.TrimSpace(mdText) == "" {
		return nil, fmt.Errorf("crawl4ai: empty markdown for %s", targetURL)
	}

	return &RenderResult{
		Title:      strings.TrimSpace(r.Metadata.Title),
		Markdown:   mdText,
		StatusCode: r.StatusCode,
	}, nil
}

// extractMarkdown handles both markdown forms (string OR {raw,fit}) via a
// two-pass unmarshal. Preference: when both fit and raw are present, contentFilter
// ("fit"/"raw") selects which to favour; a non-empty value of the other is used
// as fallback.
func (c *Client) extractMarkdown(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Pass 1: plain string form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Pass 2: object form.
	var o markdownObject
	if err := json.Unmarshal(raw, &o); err != nil {
		return ""
	}
	if c.contentFilter == "raw" {
		if strings.TrimSpace(o.RawMarkdown) != "" {
			return o.RawMarkdown
		}
		return o.FitMarkdown
	}
	// default: prefer fit, fall back to raw
	if strings.TrimSpace(o.FitMarkdown) != "" {
		return o.FitMarkdown
	}
	return o.RawMarkdown
}

// truncateForErr caps an error-embedded response body so logs/spans stay sane.
// It slices on a byte boundary then drops any trailing partial rune so the
// error string is always valid UTF-8.
func truncateForErr(b []byte) string {
	const max = 256
	if len(b) <= max {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:max]), "") + "…"
}
