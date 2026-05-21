package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"numind-server/internal/pkg/errno"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newTestWebFetchTool builds a webFetchTool wired to the given httptest.Server.
// skipSSRFCheck bypasses the pre-flight DNS check so localhost servers are reachable.
func newTestWebFetchTool(srv *httptest.Server) *webFetchTool {
	return &webFetchTool{
		httpClient:    srv.Client(),
		skipSSRFCheck: true,
	}
}

// execWebFetch is a convenience wrapper that encodes the input and calls Execute.
func execWebFetch(t *testing.T, tool *webFetchTool, rawURL, prompt string) (*webFetchOutput, error) {
	t.Helper()
	in, _ := json.Marshal(webFetchInput{URL: rawURL, Prompt: prompt})
	raw, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		return nil, err
	}
	var out webFetchOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return &out, nil
}

// ── TestWebFetch_Metadata ─────────────────────────────────────────────────────

func TestWebFetch_Metadata(t *testing.T) {
	tool := &webFetchTool{}
	if tool.Name() != "web_fetch" {
		t.Errorf("Name() = %q; want web_fetch", tool.Name())
	}
	if !tool.IsReadOnly() {
		t.Error("IsReadOnly() should be true")
	}
	if !tool.IsSearchOrReadCommand() {
		t.Error("IsSearchOrReadCommand() should be true")
	}
	if !tool.AlwaysLoad() {
		t.Error("AlwaysLoad() should be true")
	}
	if tool.UserFacingName() == "" {
		t.Error("UserFacingName() should not be empty")
	}
	if tool.NarrationVerb() == "" {
		t.Error("NarrationVerb() should not be empty")
	}
}

// ── TestWebFetch_ImplementsFullTool ───────────────────────────────────────────

func TestWebFetch_ImplementsFullTool(t *testing.T) {
	var _ FullTool = (*webFetchTool)(nil) // compile-time assertion
	// runtime check
	var tool FullTool = &webFetchTool{}
	if tool == nil {
		t.Fatal("webFetchTool should implement FullTool")
	}
}

// ── TestWebFetch_HappyPath ────────────────────────────────────────────────────

func TestWebFetch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Hello World</title></head><body><h1>World</h1><p>Hello from test.</p></body></html>`)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Title != "Hello World" {
		t.Errorf("Title = %q; want %q", out.Title, "Hello World")
	}
	if !strings.Contains(out.ContentMD, "World") {
		t.Errorf("ContentMD missing 'World': %q", out.ContentMD)
	}
	if out.ByteSize == 0 {
		t.Error("ByteSize should be > 0")
	}
	if out.Truncated {
		t.Error("Truncated should be false for small response")
	}
	if out.FetchedAt == "" {
		t.Error("FetchedAt should not be empty")
	}
	if _, err := time.Parse(time.RFC3339, out.FetchedAt); err != nil {
		t.Errorf("FetchedAt not RFC3339: %v", err)
	}
}

// ── TestWebFetch_AutoPrependHTTPS ─────────────────────────────────────────────

func TestWebFetch_AutoPrependHTTPS(t *testing.T) {
	// No scheme → https:// prepended, then DNS check fails with DNS error (not scheme error).
	_, err := validateFetchURL("totally.invalid.nonexistent.xyz.example", false)
	if err == nil {
		// Unlikely to resolve, but if it does just skip assertion.
		return
	}
	if strings.Contains(err.Error(), "unsupported scheme") {
		t.Error("auto-prepend should have fired; should not get scheme error")
	}
}

func TestWebFetch_AutoPrependDoesNotDoubleAdd(t *testing.T) {
	// Already has https:// — must NOT prepend again.
	// Use a non-resolving domain to verify the scheme parse worked.
	_, err := validateFetchURL("https://totally.invalid.nonexistent.xyz", false)
	if err != nil && strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("double-prepend produced bad scheme: %v", err)
	}
	// Only DNS error is acceptable.
}

// ── TestWebFetch_SSRF: validateFetchURL rejects internal addresses ─────────────

func TestWebFetch_SSRF_Localhost(t *testing.T) {
	_, err := validateFetchURL("http://localhost/secret", false)
	if err == nil {
		t.Fatal("expected SSRF error for localhost, got nil")
	}
	if !errors.Is(err, errno.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestWebFetch_SSRF_127(t *testing.T) {
	_, err := validateFetchURL("http://127.0.0.1/secret", false)
	if err == nil {
		t.Fatal("expected SSRF error for 127.0.0.1, got nil")
	}
	if !errors.Is(err, errno.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestWebFetch_SSRF_LocalDomain(t *testing.T) {
	_, err := validateFetchURL("http://foo.local/admin", false)
	if err == nil {
		t.Fatal("expected error for .local domain, got nil")
	}
	if !strings.Contains(err.Error(), "internal hostname not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWebFetch_SSRF_FtpScheme(t *testing.T) {
	// ftp:// has "://" so no prepend, then scheme check fires.
	_, err := validateFetchURL("ftp://example.com/file.txt", false)
	if err == nil {
		t.Fatal("expected error for ftp:// scheme, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWebFetch_SSRF_FileScheme(t *testing.T) {
	_, err := validateFetchURL("file:///etc/passwd", false)
	if err == nil {
		t.Fatal("expected error for file:// scheme, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── TestCheckIPSafe: direct IP validation ────────────────────────────────────

func TestCheckIPSafe(t *testing.T) {
	type tc struct {
		ip      string
		wantErr bool
		msgFrag string // optional substring to verify in error
	}
	cases := []tc{
		// Loopback
		{"127.0.0.1", true, "loopback"},
		{"::1", true, "loopback"},
		// Private
		{"10.0.0.1", true, "private"},
		{"172.16.0.1", true, "private"},
		{"192.168.0.1", true, "private"},
		// Link-local (not cloud metadata)
		{"169.254.1.1", true, "link-local"},
		// Cloud metadata — MUST return "cloud metadata" message (checked before link-local)
		{"169.254.169.254", true, "cloud metadata"},
		// Unspecified
		{"0.0.0.0", true, ""},
		// Public (allowed)
		{"8.8.8.8", false, ""},
		{"1.1.1.1", false, ""},
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			ip := net.ParseIP(c.ip)
			if ip == nil {
				t.Fatalf("failed to parse test IP: %q", c.ip)
			}
			err := checkIPSafe(ip, "test.example")
			if c.wantErr && err == nil {
				t.Errorf("checkIPSafe(%s): expected error, got nil", c.ip)
				return
			}
			if !c.wantErr && err != nil {
				t.Errorf("checkIPSafe(%s): unexpected error: %v", c.ip, err)
				return
			}
			if c.wantErr && c.msgFrag != "" && !strings.Contains(err.Error(), c.msgFrag) {
				t.Errorf("checkIPSafe(%s): error %q should contain %q", c.ip, err.Error(), c.msgFrag)
			}
		})
	}
}

// ── TestWebFetch_ValidateURL_Schemes ──────────────────────────────────────────

func TestValidateFetchURL_UnsupportedSchemes(t *testing.T) {
	// All of these have "://" so no prepend; scheme check fires immediately.
	schemes := []string{
		"ftp://example.com",
		"file:///etc/passwd",
		"javascript:alert(1)", // no "://" → prepend fires → https://javascript:alert(1) → host="javascript" → DNS fail
	}
	for _, s := range schemes {
		_, err := validateFetchURL(s, false)
		if err == nil {
			t.Errorf("validateFetchURL(%q): expected error, got nil", s)
		}
	}
}

// ── TestWebFetch_LargeBody ────────────────────────────────────────────────────

func TestWebFetch_LargeBody(t *testing.T) {
	// Serve 200 KB body (2× the 100 KB cap).
	bigBody := strings.Repeat("A", 200*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, bigBody)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !out.Truncated {
		t.Error("Truncated should be true for 200 KB body")
	}
	if out.ByteSize > webFetchMaxBytes {
		t.Errorf("ByteSize = %d; want <= %d", out.ByteSize, webFetchMaxBytes)
	}
	if out.ByteSize == 0 {
		t.Error("ByteSize should be > 0")
	}
}

// ── TestWebFetch_ExactCapBody ─────────────────────────────────────────────────

func TestWebFetch_ExactCapBody(t *testing.T) {
	// Exactly 100 KB — should NOT be truncated.
	exactBody := strings.Repeat("B", webFetchMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, exactBody)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Truncated {
		t.Error("Truncated should be false for exactly 100 KB body")
	}
}

// ── TestWebFetch_HTTP4xx ──────────────────────────────────────────────────────

func TestWebFetch_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	_, err := execWebFetch(t, tool, srv.URL+"/not-found", "")
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	if !errors.Is(err, errno.ErrExternalAPI) {
		t.Errorf("expected ErrExternalAPI, got: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention HTTP status, got: %v", err)
	}
}

func TestWebFetch_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	_, err := execWebFetch(t, tool, srv.URL, "")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !errors.Is(err, errno.ErrExternalAPI) {
		t.Errorf("expected ErrExternalAPI, got: %v", err)
	}
}

// ── TestWebFetch_Timeout ──────────────────────────────────────────────────────

func TestWebFetch_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	// Use a short context so the test doesn't take 30 s.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tool := newTestWebFetchTool(srv)
	in, _ := json.Marshal(webFetchInput{URL: srv.URL})
	_, err := tool.Execute(ctx, ToolInput(in))

	if err == nil {
		t.Fatal("expected timeout/cancellation error, got nil")
	}
	// context.DeadlineExceeded wraps into ErrTimeout or ErrExternalAPI depending on timing.
	if !errors.Is(err, errno.ErrTimeout) && !errors.Is(err, errno.ErrExternalAPI) {
		t.Errorf("expected ErrTimeout or ErrExternalAPI, got: %v (type %T)", err, err)
	}
}

// ── TestWebFetch_InvalidInputJSON ─────────────────────────────────────────────

func TestWebFetch_InvalidInputJSON(t *testing.T) {
	tool := &webFetchTool{}
	_, err := tool.Execute(context.Background(), ToolInput([]byte(`not-json`)))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ── TestWebFetch_EmptyURL ─────────────────────────────────────────────────────

func TestWebFetch_EmptyURL(t *testing.T) {
	// Empty URL after prepend attempt should fail at URL parsing or host check.
	_, err := validateFetchURL("", false)
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

// ── TestWebFetch_HTMLToMarkdown ───────────────────────────────────────────────

func TestWebFetch_HTMLToMarkdown_ExtractsTitle(t *testing.T) {
	html := []byte(`<html><head><title>  My Page  </title></head><body><p>Content here</p></body></html>`)
	title, content := convertHTMLToMarkdown(html)
	if title != "My Page" {
		t.Errorf("title = %q; want %q", title, "My Page")
	}
	if !strings.Contains(content, "Content here") {
		t.Errorf("content missing 'Content here': %q", content)
	}
}

func TestWebFetch_HTMLToMarkdown_StripsScript(t *testing.T) {
	html := []byte(`<html><body><script>alert('xss')</script><p>Real content</p></body></html>`)
	_, content := convertHTMLToMarkdown(html)
	if strings.Contains(content, "alert") {
		t.Errorf("content should not contain script code: %q", content)
	}
	if !strings.Contains(content, "Real content") {
		t.Errorf("content missing 'Real content': %q", content)
	}
}

// ── TestExtractHTMLTitle ──────────────────────────────────────────────────────

func TestExtractHTMLTitle_Basic(t *testing.T) {
	cases := []struct {
		html  string
		title string
	}{
		{`<title>Hello</title>`, "Hello"},
		{`<TITLE>World</TITLE>`, "World"},
		{`<title>  Spaces  </title>`, "Spaces"},
		{`<html><head><title>Page</title></head></html>`, "Page"},
		{`no title here`, ""},
		{`<title></title>`, ""},
	}
	for _, tc := range cases {
		got := extractHTMLTitle(tc.html)
		if got != tc.title {
			t.Errorf("extractHTMLTitle(%q) = %q; want %q", tc.html, got, tc.title)
		}
	}
}

// ── TestCollapseWhitespace ────────────────────────────────────────────────────

func TestCollapseWhitespace(t *testing.T) {
	in := "\n\n  hello  \n\n  world  \n\n"
	out := collapseWhitespace(in)
	if out != "hello\nworld" {
		t.Errorf("collapseWhitespace(%q) = %q; want %q", in, out, "hello\nworld")
	}
}

// ── TestRemoveHTMLBlock ───────────────────────────────────────────────────────

func TestRemoveHTMLBlock(t *testing.T) {
	html := `before<script type="text/javascript">bad code</script>after`
	got := removeHTMLBlock(html, "script")
	if strings.Contains(got, "bad code") {
		t.Errorf("removeHTMLBlock should have removed script block: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("removeHTMLBlock removed too much: %q", got)
	}
}

// ── TestWebFetch_UserAgent ────────────────────────────────────────────────────

func TestWebFetch_UserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		fmt.Fprint(w, `<html><body>ok</body></html>`)
	}))
	t.Cleanup(func() { srv.Close() })

	tool := newTestWebFetchTool(srv)
	_, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotUA, "Numind-Agent") {
		t.Errorf("User-Agent = %q; want to contain 'Numind-Agent'", gotUA)
	}
}
