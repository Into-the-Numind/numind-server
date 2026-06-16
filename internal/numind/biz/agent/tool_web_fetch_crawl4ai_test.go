package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"numind-server/internal/pkg/crawl4ai"
)

// fakeRenderer is a test double for crawl4aiRenderer. It records call count so
// tests can assert the renderer is (or is NOT) invoked.
type fakeRenderer struct {
	configured bool
	res        *crawl4ai.RenderResult
	err        error
	called     int
}

func (f *fakeRenderer) Configured() bool { return f.configured }

func (f *fakeRenderer) RenderMarkdown(_ context.Context, _ string) (*crawl4ai.RenderResult, error) {
	f.called++
	return f.res, f.err
}

// Note: fetch_path / crawl4ai_* span fields are observability only and require a
// langfuse trace context (absent in unit tests, as elsewhere in this file), so
// these tests assert branch BEHAVIOUR (which path produced the content) rather
// than span output.

// TestWebFetch_RenderPath: configured renderer returns markdown → that markdown
// is the result, raw HTTP is never used.
func TestWebFetch_RenderPath(t *testing.T) {
	fr := &fakeRenderer{
		configured: true,
		res:        &crawl4ai.RenderResult{Title: "Rendered Title", Markdown: "# Rendered\n\nbody from crawl4ai", StatusCode: 200},
	}
	tool := &webFetchTool{renderer: fr, skipSSRFCheck: true} // httpClient nil — must not be reached

	out, err := execWebFetch(t, tool, "https://example.com", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fr.called != 1 {
		t.Fatalf("renderer called %d times; want 1", fr.called)
	}
	if out.Title != "Rendered Title" {
		t.Errorf("Title = %q; want Rendered Title", out.Title)
	}
	if !strings.Contains(out.ContentMD, "body from crawl4ai") {
		t.Errorf("ContentMD = %q; want it to contain the rendered body", out.ContentMD)
	}
	if out.Error != "" {
		t.Errorf("Error should be empty on success, got %q", out.Error)
	}
}

// TestWebFetch_FallbackToRaw: configured renderer fails → falls back to raw HTTP,
// which succeeds; the raw HTML content is returned.
func TestWebFetch_FallbackToRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Raw Fallback</title></head><body><p>raw body content</p></body></html>`)
	}))
	t.Cleanup(srv.Close)

	fr := &fakeRenderer{configured: true, err: fmt.Errorf("crawl4ai: boom")}
	tool := &webFetchTool{renderer: fr, httpClient: srv.Client(), skipSSRFCheck: true}

	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fr.called != 1 {
		t.Fatalf("renderer called %d times; want 1 (attempted then fell back)", fr.called)
	}
	if out.Title != "Raw Fallback" {
		t.Errorf("Title = %q; want Raw Fallback (from raw HTTP)", out.Title)
	}
	if !strings.Contains(out.ContentMD, "raw body content") {
		t.Errorf("ContentMD = %q; want raw HTTP body", out.ContentMD)
	}
	if out.Error != "" {
		t.Errorf("Error should be empty (raw fallback succeeded), got %q", out.Error)
	}
}

// TestWebFetch_RenderEmptyMarkdown_FallsBack: renderer returns success with empty
// markdown → treated as failure → raw fallback.
func TestWebFetch_RenderEmptyMarkdown_FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>RawT</title></head><body>raw</body></html>`)
	}))
	t.Cleanup(srv.Close)

	fr := &fakeRenderer{configured: true, res: &crawl4ai.RenderResult{Title: "ignored", Markdown: "   "}}
	tool := &webFetchTool{renderer: fr, httpClient: srv.Client(), skipSSRFCheck: true}

	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Title != "RawT" {
		t.Errorf("Title = %q; want RawT (empty render → raw fallback)", out.Title)
	}
}

// TestWebFetch_RawDirect_UnconfiguredRenderer: renderer present but Configured()
// false → render is never attempted, raw HTTP is used directly.
func TestWebFetch_RawDirect_UnconfiguredRenderer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Direct</title></head><body>direct body</body></html>`)
	}))
	t.Cleanup(srv.Close)

	fr := &fakeRenderer{configured: false}
	tool := &webFetchTool{renderer: fr, httpClient: srv.Client(), skipSSRFCheck: true}

	out, err := execWebFetch(t, tool, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fr.called != 0 {
		t.Errorf("renderer should NOT be called when Configured()=false, got %d calls", fr.called)
	}
	if out.Title != "Direct" {
		t.Errorf("Title = %q; want Direct", out.Title)
	}
}

// TestWebFetch_SSRFEnforced_RendererNotCalled: an internal/loopback URL is
// rejected by the pre-flight SSRF check BEFORE the renderer is consulted.
func TestWebFetch_SSRFEnforced_RendererNotCalled(t *testing.T) {
	fr := &fakeRenderer{configured: true, res: &crawl4ai.RenderResult{Markdown: "should not be used"}}
	// skipSSRFCheck:false → validateFetchURL resolves the literal IP and rejects loopback.
	tool := &webFetchTool{renderer: fr, skipSSRFCheck: false}

	out, err := execWebFetch(t, tool, "http://127.0.0.1/internal", "")
	if err != nil {
		t.Fatalf("expected soft error (not Go error), got: %v", err)
	}
	if fr.called != 0 {
		t.Errorf("renderer must NOT be called for an SSRF-blocked URL, got %d calls", fr.called)
	}
	if out.Error == "" || !strings.Contains(out.ContentMD, "ERROR") {
		t.Errorf("expected SSRF soft-error payload, got Error=%q ContentMD=%q", out.Error, out.ContentMD)
	}
}

// TestWebFetch_RenderFailAndRawFail_SoftError: both render and raw HTTP fail →
// a SOFT error result (never a Go error that would kill the agent run).
func TestWebFetch_RenderFailAndRawFail_SoftError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	fr := &fakeRenderer{configured: true, err: fmt.Errorf("crawl4ai down")}
	tool := &webFetchTool{renderer: fr, httpClient: srv.Client(), skipSSRFCheck: true}

	in, _ := json.Marshal(webFetchInput{URL: srv.URL})
	raw, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("must be a soft error, got Go error: %v", err)
	}
	var out webFetchOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == "" {
		t.Errorf("expected soft-error payload when both paths fail, got %q", out.ContentMD)
	}
}
