package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"

	"numind-server/internal/pkg/crawl4ai"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// crawl4aiRenderer is the subset of *crawl4ai.Client that web_fetch needs.
// Declaring it as an interface lets tests inject a fake renderer without a
// live crawl4ai service. *crawl4ai.Client satisfies it.
type crawl4aiRenderer interface {
	Configured() bool
	RenderMarkdown(ctx context.Context, targetURL string) (*crawl4ai.RenderResult, error)
}

type webFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

type webFetchOutput struct {
	Title     string `json:"title"`
	ContentMD string `json:"content_md"`
	ByteSize  int    `json:"byte_size"`
	Truncated bool   `json:"truncated"`
	FetchedAt string `json:"fetched_at"`
	// Error is set ONLY on the soft-error path (mirrors webSearchOutput.Error).
	// It lets the narration result template distinguish a real page read from a
	// soft failure — without it, returnSoftError's Chinese error label lands in
	// Title and renders as a misleading "已读取：网址被安全策略拦截".
	Error string `json:"error,omitempty"`
}

// webFetchTool implements FullTool for the "web_fetch" operation.
// It fetches a URL, enforces SSRF protection, converts HTML to Markdown,
// and caps the response at 100 KB.
//
// Fields are unexported; httpClient may be set in tests to inject a custom
// *http.Client (e.g. one pointing at an httptest.Server without SSRF guards).
// skipSSRFCheck may be set true in tests that supply their own httpClient and
// want to bypass the pre-flight DNS check.
type webFetchTool struct {
	BaseTool
	httpClient    *http.Client     // nil → newSafeHTTPClient() on each Execute call
	skipSSRFCheck bool             // test-only: bypass validateFetchURL DNS check
	renderer      crawl4aiRenderer // nil/unconfigured → pure raw-HTTP path (no regression)
}

var _ FullTool = (*webFetchTool)(nil)

// NewWebFetchTool constructs a webFetchTool with production defaults (SSRF check enabled,
// newSafeHTTPClient used on first Execute, crawl4ai renderer from config). Used by
// platformToolFactory. When crawl4ai.base_url is unset, the renderer reports
// Configured()=false and web_fetch behaves exactly as the legacy raw-HTTP tool.
func NewWebFetchTool() FullTool {
	return &webFetchTool{renderer: crawl4ai.NewClientFromConfig()}
}

const (
	webFetchMaxBytes = 100 * 1024 // 100 KB
	webFetchTimeout  = 30 * time.Second
)

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL and return its contents as Markdown. JavaScript-rendered pages are supported (a real browser renders the page when the render service is available; otherwise a fast raw fetch is used). Input: { url: string, prompt?: string }. Returns: { title, content_md, byte_size, truncated, fetched_at }."
}
func (t *webFetchTool) UserFacingName() string      { return "网页读取" }
func (t *webFetchTool) NarrationVerb() string       { return "读取网页" }
func (t *webFetchTool) IsReadOnly() bool            { return true }
func (t *webFetchTool) IsSearchOrReadCommand() bool { return true }
func (t *webFetchTool) AlwaysLoad() bool            { return true }

func (t *webFetchTool) returnSoftError(title, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(webFetchOutput{
		Title:     title,
		ContentMD: "ERROR: " + msg,
		ByteSize:  len(msg) + 7,
		Truncated: false,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Error:     "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url":    {"type": "string", "format": "uri", "description": "The public web page URL to fetch. Do NOT use for uploaded attachments — use file_read for those."},
			"prompt": {"type": "string", "description": "Optional instruction describing what to extract from the page."}
		},
		"required": ["url"]
	}`)
}

func (t *webFetchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in webFetchInput
	// Malformed model input must come back soft: a non-nil Go error here is a
	// NodeRunError that kills the whole run (dev run 136: bool prompt).
	if err := json.Unmarshal(input, &in); err != nil {
		return t.returnSoftError("输入格式错误", "web_fetch: invalid input: %v", err)
	}

	// Reject COS agent-attachment URLs — they are private uploads, not web
	// pages. Hitting them with anonymous GET gives 403.
	//
	// IMPORTANT — soft reject via successful ToolResult:
	//
	// Returning a Go error here would propagate to Eino as a NodeRunError,
	// which TERMINATES the agent run (state_reason=model_error) before the
	// LLM ever sees the message. Eino v0.8.13 has no tool-error → tool-
	// message hook; Go errors are fatal. We observed this directly:
	// commit 5c0f64da (hard reject) produced the routing-hint error string
	// correctly, but the LLM never saw it — Eino terminated the run after
	// 4 steps (file_read ✓ ✓, web_fetch ✗ ✗).
	//
	// Workaround: return ToolResult SUCCESS with the routing instruction as
	// the content. Eino feeds this back to the LLM as a normal tool message,
	// and the LLM self-corrects on the next ReAct turn. This is a contract
	// violation (the tool "succeeded" but actually delivered an error), but
	// it is the only path that gets the message in front of the LLM.
	//
	// A more principled fix is a ToolCallMiddleware that converts any Go
	// error into a tool-result message globally — tracked as follow-up.
	if isAgentAttachmentURL(in.URL) {
		msg := fmt.Sprintf(
			"ERROR: %q is an uploaded attachment in private storage, not a "+
				"public web page. Do NOT call web_fetch on uploaded attachments. "+
				"Call the file_read tool instead, passing parameter "+
				"file_url=%q. If you have already called file_read on this "+
				"URL in this conversation, use the existing result and do "+
				"NOT call any tool on this URL again.",
			in.URL, in.URL,
		)
		out, _ := json.Marshal(webFetchOutput{
			Title:     "Wrong tool — use file_read for uploaded attachments",
			ContentMD: msg,
			ByteSize:  len(msg),
			Truncated: false,
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return ToolResult(out), nil
	}

	// URL validation: scheme check always runs; DNS/IP check skipped in tests
	// that provide their own httpClient.
	targetURL, err := validateFetchURL(in.URL, t.skipSSRFCheck)
	if err != nil {
		// agent-security-hardening: an SSRF/scheme block is returned as a SOFT tool
		// result (not a Go error) so the LLM is told why and the run continues — only
		// internal/metadata/non-http targets are blocked; public URLs are unaffected.
		return t.returnSoftError("网址被安全策略拦截", "%s", err.Error())
	}

	fetchStart := time.Now()
	var spanID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.web_fetch.execute",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(in),
		)
	}

	// endSpan closes the span (if any). fetch_path is always supplied by the
	// caller via the fields map so every exit path stays observable.
	endSpan := func(fields map[string]any, errMsg string) {
		if spanID == "" {
			return
		}
		tc := langfuse.FromContext(ctx)
		if tc == nil {
			return
		}
		if errMsg != "" {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(fields), langfuse.WithSpanError(errMsg))
			return
		}
		langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(fields))
	}

	// fetchPath records which path produced the result (observability).
	fetchPath := "raw_direct"
	var crawl4aiErr string

	// --- Render path: crawl4ai renders JS pages → LLM-ready markdown. The
	// target URL was already SSRF pre-flight validated above; on any failure we
	// fall back to the raw HTTP path below (zero regression). crawl4ai failures
	// NEVER return a Go error (that would kill the agent run).
	if t.renderer != nil && t.renderer.Configured() {
		renderStart := time.Now()
		res, rerr := t.renderer.RenderMarkdown(ctx, targetURL)
		if rerr == nil && res != nil && strings.TrimSpace(res.Markdown) != "" {
			content := res.Markdown
			truncated := len(content) > webFetchMaxBytes
			if truncated {
				content = content[:webFetchMaxBytes]
			}
			endSpan(map[string]any{
				"url":                 targetURL,
				"fetch_path":          "render",
				"crawl4ai_status":     res.StatusCode,
				"crawl4ai_latency_ms": time.Since(renderStart).Milliseconds(),
				"content_length":      len(content),
				"truncated":           truncated,
				"latency_ms":          time.Since(fetchStart).Milliseconds(),
			}, "")
			out, _ := json.Marshal(webFetchOutput{
				Title:     res.Title,
				ContentMD: content,
				ByteSize:  len(content),
				Truncated: truncated,
				FetchedAt: time.Now().UTC().Format(time.RFC3339),
			})
			return ToolResult(out), nil
		}
		if rerr != nil {
			crawl4aiErr = rerr.Error()
		} else {
			crawl4aiErr = "crawl4ai: empty markdown"
		}
		fetchPath = "raw_fallback"
		log.C(ctx).Infow("web_fetch: crawl4ai render failed, falling back to raw HTTP",
			"url", targetURL, "error", crawl4aiErr)
	}

	// baseFields seeds every raw-path span output with url + fetch_path (+ the
	// crawl4ai fallback error, when applicable).
	baseFields := func(extra map[string]any) map[string]any {
		m := map[string]any{"url": targetURL, "fetch_path": fetchPath}
		if crawl4aiErr != "" {
			m["crawl4ai_error"] = crawl4aiErr
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	client := t.httpClient
	if client == nil {
		client = newSafeHTTPClient()
	}

	fetchCtx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		endSpan(baseFields(map[string]any{"latency_ms": time.Since(fetchStart).Milliseconds()}), err.Error())
		// Model-controlled URL → input-derived failure must stay soft (spec I1).
		return t.returnSoftError("网页请求失败", "web_fetch: build request: %s", err.Error())
	}
	req.Header.Set("User-Agent", "Numind-Agent/1.0 (+https://youshu.asia)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		endSpan(baseFields(map[string]any{"latency_ms": time.Since(fetchStart).Milliseconds()}), err.Error())
		var finalErr error
		if errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err) {
			finalErr = errno.ErrTimeout.SetMessage("web_fetch: timeout after %s", webFetchTimeout)
		} else {
			finalErr = errno.ErrExternalAPI.SetMessage("web_fetch: %s", err.Error())
		}
		return t.returnSoftError("网页请求失败", "%s", finalErr.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		endSpan(baseFields(map[string]any{
			"status_code": resp.StatusCode,
			"latency_ms":  time.Since(fetchStart).Milliseconds(),
		}), fmt.Sprintf("HTTP %d", resp.StatusCode))
		return t.returnSoftError("网页请求拒绝", "web_fetch: HTTP %d from %s", resp.StatusCode, targetURL)
	}

	// Read body with cap to detect truncation.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(webFetchMaxBytes+1)))
	if err != nil {
		endSpan(baseFields(map[string]any{"latency_ms": time.Since(fetchStart).Milliseconds()}), err.Error())
		return t.returnSoftError("读取网页内容失败", "web_fetch: read body: %s", err.Error())
	}
	truncated := len(body) > webFetchMaxBytes
	if truncated {
		body = body[:webFetchMaxBytes]
	}

	// Close span with full metadata + fetch_path (raw_direct or raw_fallback).
	endSpan(baseFields(map[string]any{
		"status_code":    resp.StatusCode,
		"content_length": len(body),
		"mime_type":      resp.Header.Get("Content-Type"),
		"latency_ms":     time.Since(fetchStart).Milliseconds(),
		"truncated":      truncated,
	}), "")

	title, contentMD := convertHTMLToMarkdown(body)

	out, _ := json.Marshal(webFetchOutput{
		Title:     title,
		ContentMD: contentMD,
		ByteSize:  len(body),
		Truncated: truncated,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return ToolResult(out), nil
}

// isAgentAttachmentURL reports whether the URL points at an uploaded agent
// attachment in private COS storage. Such URLs are signed (or require signing)
// and must NOT be fetched by web_fetch. They must be read through file_read,
// which handles ownership verification and presigning.
//
// Two indicators are checked (BOTH must combine for a "true" positive —
// defence in depth via shape-AND-path):
//   - COS bucket host shape (bucket.cos.region.myqcloud.com)
//   - /agent-attachments/<userID>/ OR /agent-outputs/<userID>/ path segment
//     (attachmentPathRE is shared with tool_file_read.go and matches both
//     prefixes — uploads and tool-generated artifacts are both user-owned
//     private COS objects that web_fetch must route to file_read.)
//
// A public CDN that happens to have a path like /agent-attachments/ is still
// considered web-fetchable (host check rejects it). In practice both signals
// come together for our private uploads and generated artifacts.
func isAgentAttachmentURL(rawURL string) bool {
	_, isCOS := extractCOSObjectKey(rawURL)
	hasAttachmentPath := attachmentPathRE.MatchString(rawURL)
	return isCOS && hasAttachmentPath
}

// isTimeoutError checks if an error is a network timeout.
func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// validateFetchURL implements spec §8.1 SSRF prevention algorithm.
// It auto-prepends https:// if missing, validates scheme, resolves DNS,
// and rejects loopback / private / link-local / cloud metadata IPs.
//
// skipDNSCheck skips the DNS resolution and IP validation steps (test-only).
func validateFetchURL(rawURL string, skipDNSCheck bool) (string, error) {
	// 1. Auto-prepend https:// only if no scheme is present at all.
	//    Detect presence of a scheme by looking for "://".
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errno.ErrInvalidInput.SetMessage("web_fetch: invalid URL: %s", err.Error())
	}

	// 2. Only http and https are allowed.
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errno.ErrInvalidInput.SetMessage("web_fetch: unsupported scheme: %s", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return "", errno.ErrInvalidInput.SetMessage("web_fetch: URL missing host")
	}

	// 3. Reject .local TLD (mDNS / Bonjour hostnames used in private networks).
	if strings.HasSuffix(strings.ToLower(host), ".local") {
		return "", errno.ErrInvalidInput.SetMessage("web_fetch: internal hostname not allowed: %s", host)
	}

	if skipDNSCheck {
		return u.String(), nil
	}

	// 4. DNS resolve — collect all A/AAAA records.
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", errno.ErrInvalidInput.SetMessage("web_fetch: DNS resolve failed for %s: %s", host, err.Error())
	}

	// 5. Validate every resolved IP.
	for _, ip := range ips {
		if err := checkIPSafe(ip, host); err != nil {
			return "", err
		}
	}

	return u.String(), nil
}

// checkIPSafe returns an error if ip is a private, loopback, link-local,
// cloud-metadata, or otherwise disallowed address.
// Cloud metadata check runs before link-local because 169.254.169.254 is both.
func checkIPSafe(ip net.IP, host string) error {
	// Cloud metadata endpoints checked first (they are also link-local, but
	// we want the specific "cloud metadata" message for observability).
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return errno.ErrInvalidInput.SetMessage("web_fetch: cloud metadata endpoint blocked (%s)", ip)
	}
	if ip6 := net.ParseIP("fd00:ec2::254"); ip6 != nil && ip.Equal(ip6) {
		return errno.ErrInvalidInput.SetMessage("web_fetch: cloud metadata endpoint blocked (%s)", ip)
	}

	if ip.IsLoopback() {
		return errno.ErrInvalidInput.SetMessage("web_fetch: %s resolves to loopback address %s", host, ip)
	}
	if ip.IsPrivate() {
		return errno.ErrInvalidInput.SetMessage("web_fetch: %s resolves to private address %s", host, ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errno.ErrInvalidInput.SetMessage("web_fetch: %s resolves to link-local address %s", host, ip)
	}
	if ip.IsUnspecified() {
		return errno.ErrInvalidInput.SetMessage("web_fetch: %s resolves to unspecified address %s", host, ip)
	}
	return nil
}

// newSafeHTTPClient returns an *http.Client that re-validates resolved IPs
// at dial time to defend against DNS rebinding attacks (TOCTOU mitigation).
func newSafeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: webFetchTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("web_fetch dial: split host/port: %w", err)
				}

				// Re-resolve at dial time to detect DNS rebinding.
				ips, err := net.LookupIP(host)
				if err != nil {
					return nil, fmt.Errorf("web_fetch dial: DNS lookup for %s: %w", host, err)
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("web_fetch dial: no IPs for %s", host)
				}

				// Check ALL resolved IPs; pick the first safe one to dial.
				// Checking only ips[0] would allow bypassing SSRF guards when a
				// host resolves to both a safe and a disallowed address.
				var safeIP net.IP
				for _, ip := range ips {
					if err := checkIPSafe(ip, host); err == nil {
						safeIP = ip
						break // first safe IP wins
					}
				}
				if safeIP == nil {
					return nil, fmt.Errorf("web_fetch dial: all resolved IPs disallowed for %s", host)
				}

				d := net.Dialer{Timeout: 10 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(safeIP.String(), port))
			},
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}
}

// convertHTMLToMarkdown converts raw HTML bytes to a (title, markdown) pair.
// It uses github.com/JohannesKaufmann/html-to-markdown for conversion.
func convertHTMLToMarkdown(body []byte) (title, content string) {
	// Extract title from raw HTML before conversion (library doesn't expose it).
	title = extractHTMLTitle(string(body))

	converter := md.NewConverter("", true, nil)
	result, err := converter.ConvertBytes(body)
	if err != nil {
		// Fallback to simple strip on conversion error.
		_, content = simpleHTMLStrip(body)
		return title, content
	}
	content = strings.TrimSpace(string(result))
	return title, content
}

// extractHTMLTitle finds the first <title>…</title> in HTML and returns its text content.
func extractHTMLTitle(html string) string {
	lower := strings.ToLower(html)
	start := strings.Index(lower, "<title")
	if start < 0 {
		return ""
	}
	// Skip to end of opening tag.
	closeIdx := strings.Index(lower[start:], ">")
	if closeIdx < 0 {
		return ""
	}
	contentStart := start + closeIdx + 1
	end := strings.Index(lower[contentStart:], "</title>")
	if end < 0 {
		return ""
	}
	raw := html[contentStart : contentStart+end]
	return strings.TrimSpace(raw)
}

// simpleHTMLStrip is a fallback HTML-to-text converter using only stdlib.
// It removes <script>, <style>, and <head> blocks, then strips all tags.
func simpleHTMLStrip(body []byte) (title, text string) {
	s := string(body)
	title = extractHTMLTitle(s)
	s = removeHTMLBlock(s, "script")
	s = removeHTMLBlock(s, "style")
	s = removeHTMLBlock(s, "head")

	// Strip remaining tags, replacing with newlines.
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
			sb.WriteRune('\n')
		default:
			if !inTag {
				sb.WriteRune(r)
			}
		}
	}
	text = collapseWhitespace(sb.String())
	return title, text
}

// removeHTMLBlock removes all occurrences of <tag ...>…</tag> (case-insensitive).
func removeHTMLBlock(s, tag string) string {
	lower := strings.ToLower(s)
	openTag := "<" + tag
	closeTag := "</" + tag + ">"
	for {
		start := strings.Index(lower, openTag)
		if start < 0 {
			break
		}
		end := strings.Index(lower[start:], closeTag)
		if end < 0 {
			// Unclosed tag — remove from open to EOF.
			s = s[:start]
			break
		}
		absEnd := start + end + len(closeTag)
		s = s[:start] + s[absEnd:]
		lower = lower[:start] + lower[absEnd:]
	}
	return s
}

// collapseWhitespace replaces runs of whitespace with a single space or newline.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return strings.Join(result, "\n")
}
