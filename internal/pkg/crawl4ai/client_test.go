package crawl4ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"numind-server/internal/pkg/httpclient"
)

// testClient builds a Client pointed at baseURL with a short-timeout http client.
func testClient(baseURL, filter string) *Client {
	if filter == "" {
		filter = "fit"
	}
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		contentFilter: filter,
		http: httpclient.NewClient(&httpclient.Config{
			Timeout:             5 * time.Second,
			ConnectTimeout:      2 * time.Second,
			TLSHandshakeTimeout: 2 * time.Second,
			MaxRetries:          0,
			UserAgent:           "test",
		}),
	}
}

// crawlServer returns an httptest.Server that responds to POST /crawl with the
// given HTTP status and raw JSON body.
func crawlServer(t *testing.T, status int, jsonBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawl" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, jsonBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRenderMarkdown_StringForm(t *testing.T) {
	body := `{"success":true,"results":[{"url":"https://x.com","status_code":200,"markdown":"# Hello\n\nworld","metadata":{"title":"Hello Page"}}]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "fit")

	res, err := c.RenderMarkdown(context.Background(), "https://x.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Title != "Hello Page" {
		t.Errorf("Title = %q; want %q", res.Title, "Hello Page")
	}
	if !strings.Contains(res.Markdown, "Hello") {
		t.Errorf("Markdown = %q; want it to contain Hello", res.Markdown)
	}
	if res.StatusCode != 200 {
		t.Errorf("StatusCode = %d; want 200", res.StatusCode)
	}
}

func TestRenderMarkdown_ObjectForm_PrefersFit(t *testing.T) {
	body := `{"success":true,"results":[{"markdown":{"raw_markdown":"RAW body","fit_markdown":"FIT body"},"metadata":{"title":"T"}}]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "fit")

	res, err := c.RenderMarkdown(context.Background(), "https://x.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Markdown != "FIT body" {
		t.Errorf("Markdown = %q; want FIT body (fit preferred)", res.Markdown)
	}
}

func TestRenderMarkdown_ObjectForm_FallsBackToRawWhenFitEmpty(t *testing.T) {
	body := `{"success":true,"results":[{"markdown":{"raw_markdown":"RAW only","fit_markdown":""},"metadata":{"title":"T"}}]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "fit")

	res, err := c.RenderMarkdown(context.Background(), "https://x.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Markdown != "RAW only" {
		t.Errorf("Markdown = %q; want RAW only (fit empty → raw)", res.Markdown)
	}
}

func TestRenderMarkdown_ContentFilterRaw_PrefersRaw(t *testing.T) {
	body := `{"success":true,"results":[{"markdown":{"raw_markdown":"RAW body","fit_markdown":"FIT body"},"metadata":{"title":"T"}}]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "raw")

	res, err := c.RenderMarkdown(context.Background(), "https://x.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Markdown != "RAW body" {
		t.Errorf("Markdown = %q; want RAW body (content_filter=raw)", res.Markdown)
	}
}

func TestRenderMarkdown_SuccessFalse_Errors(t *testing.T) {
	body := `{"success":false,"results":[]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "fit")

	if _, err := c.RenderMarkdown(context.Background(), "https://x.com"); err == nil {
		t.Fatal("expected error for success=false, got nil")
	}
}

func TestRenderMarkdown_EmptyMarkdown_Errors(t *testing.T) {
	body := `{"success":true,"results":[{"markdown":"   ","metadata":{"title":"T"}}]}`
	srv := crawlServer(t, 200, body)
	c := testClient(srv.URL, "fit")

	if _, err := c.RenderMarkdown(context.Background(), "https://x.com"); err == nil {
		t.Fatal("expected error for empty markdown, got nil")
	}
}

func TestRenderMarkdown_Non200_Errors(t *testing.T) {
	srv := crawlServer(t, 500, `{"error":"boom"}`)
	c := testClient(srv.URL, "fit")

	_, err := c.RenderMarkdown(context.Background(), "https://x.com")
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500, got: %v", err)
	}
}

func TestRenderMarkdown_Timeout_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1000 * time.Millisecond)
		_, _ = io.WriteString(w, `{"success":true,"results":[{"markdown":"x","metadata":{}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		baseURL:       srv.URL,
		contentFilter: "fit",
		http: httpclient.NewClient(&httpclient.Config{
			Timeout:    100 * time.Millisecond,
			MaxRetries: 0,
			UserAgent:  "test",
		}),
	}
	if _, err := c.RenderMarkdown(context.Background(), "https://x.com"); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestRenderMarkdown_BodyReadInterrupted simulates a connection closed mid-body
// (Content-Length promises more than is delivered) → io.ReadAll returns an
// unexpected-EOF error, which must surface as a render error (not a panic).
func TestRenderMarkdown_BodyReadInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			// t.Fatal would only stop this goroutine, not the test → use Errorf.
			t.Errorf("server does not support hijacking")
			return
		}
		conn, bw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// Promise 1000 bytes, deliver 5, then close → unexpected EOF on the client.
		_, _ = bw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nContent-Type: application/json\r\n\r\n")
		_, _ = bw.WriteString("{\"su")
		_ = bw.Flush()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL, "fit")
	if _, err := c.RenderMarkdown(context.Background(), "https://x.com"); err == nil {
		t.Fatal("expected body-read error, got nil")
	}
}

func TestConfigured(t *testing.T) {
	if testClient("https://host", "fit").Configured() != true {
		t.Error("Configured() should be true with non-empty base_url")
	}
	empty := &Client{baseURL: "", http: nil}
	if empty.Configured() != false {
		t.Error("Configured() should be false with empty base_url")
	}
	var nilClient *Client
	if nilClient.Configured() != false {
		t.Error("Configured() on nil client should be false")
	}
}

func TestRenderMarkdown_SendsAuthHeaderAndURL(t *testing.T) {
	// Pass captured request data back over a channel: the send/receive provides
	// the happens-before edge the race detector needs (go test -race clean).
	type capture struct {
		auth string
		body []byte
	}
	ch := make(chan capture, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capture{auth: r.Header.Get("Authorization"), body: b}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"results":[{"markdown":"ok","metadata":{"title":"t"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL, "fit")
	c.token = "secret-token"

	if _, err := c.RenderMarkdown(context.Background(), "https://target.example/page"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := <-ch
	if got.auth != "Bearer secret-token" {
		t.Errorf("Authorization = %q; want Bearer secret-token", got.auth)
	}
	var req crawlRequest
	if err := json.Unmarshal(got.body, &req); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if len(req.URLs) != 1 || req.URLs[0] != "https://target.example/page" {
		t.Errorf("sent urls = %v; want [https://target.example/page]", req.URLs)
	}
}
