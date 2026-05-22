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

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
)

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
	httpClient    *http.Client // nil → newSafeHTTPClient() on each Execute call
	skipSSRFCheck bool         // test-only: bypass validateFetchURL DNS check
}

var _ FullTool = (*webFetchTool)(nil)

// NewWebFetchTool constructs a webFetchTool with production defaults (SSRF check enabled,
// newSafeHTTPClient used on first Execute). Used by platformToolFactory (T7).
func NewWebFetchTool() FullTool { return &webFetchTool{} }

const (
	webFetchMaxBytes = 100 * 1024 // 100 KB
	webFetchTimeout  = 30 * time.Second
)

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL and return its contents as Markdown. Input: { url: string, prompt?: string }. Returns: { title, content_md, byte_size, truncated, fetched_at }."
}
func (t *webFetchTool) UserFacingName() string      { return "网页读取" }
func (t *webFetchTool) NarrationVerb() string       { return "读取网页" }
func (t *webFetchTool) IsReadOnly() bool            { return true }
func (t *webFetchTool) IsSearchOrReadCommand() bool { return true }
func (t *webFetchTool) AlwaysLoad() bool            { return true }

func (t *webFetchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, errno.ErrBind.SetMessage("web_fetch: %s", err.Error())
	}

	// URL validation: scheme check always runs; DNS/IP check skipped in tests
	// that provide their own httpClient.
	targetURL, err := validateFetchURL(in.URL, t.skipSSRFCheck)
	if err != nil {
		return nil, err
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

	client := t.httpClient
	if client == nil {
		client = newSafeHTTPClient()
	}

	fetchCtx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		if spanID != "" {
			if tc := langfuse.FromContext(ctx); tc != nil {
				langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
			}
		}
		return nil, errno.ErrInvalidInput.SetMessage("web_fetch: build request: %s", err.Error())
	}
	req.Header.Set("User-Agent", "Numind-Agent/1.0 (+https://youshu.asia)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		if spanID != "" {
			if tc := langfuse.FromContext(ctx); tc != nil {
				langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
			}
		}
		if errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err) {
			return nil, errno.ErrTimeout.SetMessage("web_fetch: timeout after %s", webFetchTimeout)
		}
		return nil, errno.ErrExternalAPI.SetMessage("web_fetch: %s", err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		if spanID != "" {
			if tc := langfuse.FromContext(ctx); tc != nil {
				langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(map[string]any{
					"url":         targetURL,
					"status_code": resp.StatusCode,
					"latency_ms":  time.Since(fetchStart).Milliseconds(),
				}), langfuse.WithSpanError(fmt.Sprintf("HTTP %d", resp.StatusCode)))
			}
		}
		return nil, errno.ErrExternalAPI.SetMessage("web_fetch: HTTP %d from %s", resp.StatusCode, targetURL)
	}

	// Read body with cap to detect truncation.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(webFetchMaxBytes+1)))
	if err != nil {
		if spanID != "" {
			if tc := langfuse.FromContext(ctx); tc != nil {
				langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
			}
		}
		return nil, errno.ErrExternalAPI.SetMessage("web_fetch: read body: %s", err.Error())
	}
	truncated := len(body) > webFetchMaxBytes
	if truncated {
		body = body[:webFetchMaxBytes]
	}

	// Close span with full §6.2 metadata: url, status_code, content_length,
	// mime_type, latency_ms, truncated.
	if spanID != "" {
		if tc := langfuse.FromContext(ctx); tc != nil {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(map[string]any{
				"url":            targetURL,
				"status_code":    resp.StatusCode,
				"content_length": len(body),
				"mime_type":      resp.Header.Get("Content-Type"),
				"latency_ms":     time.Since(fetchStart).Milliseconds(),
				"truncated":      truncated,
			}))
		}
	}

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
