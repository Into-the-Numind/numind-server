package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── textParserImpl tests ────────────────────────────────────────────────────

func TestTextParserImpl_Parse_ReturnsBodyContent(t *testing.T) {
	fixture := "Hello, agent file_read!"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	content, pageCount, truncated, err := parser.Parse(context.Background(), srv.URL+"/file.txt", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != fixture {
		t.Errorf("expected %q, got %q", fixture, content)
	}
	if pageCount != 0 {
		t.Errorf("expected pageCount 0, got %d", pageCount)
	}
	if truncated {
		t.Error("expected truncated=false for small content")
	}
}

func TestTextParserImpl_Parse_ReturnsCompleteLargeContent(t *testing.T) {
	big := strings.Repeat("中🙂abc", 40*1024) // >300 KiB, mixed-width UTF-8.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	content, _, truncated, err := parser.Parse(context.Background(), srv.URL+"/big.txt", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("parser must not truncate; pagination belongs to file_read")
	}
	if content != big {
		t.Fatalf("parser did not return the complete body: got %d bytes want %d", len(content), len(big))
	}
}

func TestTextParserImpl_Parse_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	// textParserImpl returns an error for HTTP status >= 400 (added in T5 reviewer fix).
	_, _, _, err := parser.Parse(context.Background(), srv.URL+"/file.txt", "")
	if err == nil {
		t.Fatal("expected error from textParserImpl on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 in error message, got: %v", err)
	}
}

func TestTextParserImpl_Parse_InvalidURL(t *testing.T) {
	parser := &textParserImpl{}
	_, _, _, err := parser.Parse(context.Background(), "://bad-url", "")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestTextParserImpl_Parse_ExactBoundary(t *testing.T) {
	// Content exactly equal to the 20 MiB download cap is accepted in full.
	exact := strings.Repeat("a", docFetchMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(exact))
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	content, _, truncated, err := parser.Parse(context.Background(), srv.URL+"/exact.txt", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("expected truncated=false for exactly-boundary content")
	}
	if len(content) != docFetchMaxBytes {
		t.Errorf("expected content len %d, got %d", docFetchMaxBytes, len(content))
	}
}

func TestTextParserImpl_Parse_Over20MiBReturnsError(t *testing.T) {
	tooLarge := strings.Repeat("x", docFetchMaxBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tooLarge))
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	_, _, _, err := parser.Parse(context.Background(), srv.URL+"/too-large.txt", "")
	if err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("expected explicit 20 MiB error, got %v", err)
	}
}

func TestDocumentParserImpl_Parse_ReturnsCompleteTextFixture(t *testing.T) {
	fixture := strings.Repeat("文档完整解析🙂\n", 32*1024) // well beyond the old 200 KiB cap.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	parser := &documentParserImpl{}
	content, _, truncated, err := parser.Parse(context.Background(), srv.URL+"/fixture.txt", "")
	if err != nil {
		t.Fatalf("unexpected document parser error: %v", err)
	}
	expected := strings.TrimSuffix(fixture, "\n") // shared DocumentParser normalizes one trailing newline.
	if truncated || content != expected {
		t.Fatalf("document parser must return complete normalized source: got=%d want=%d truncated=%v", len(content), len(expected), truncated)
	}
}

// ─── fileParser interface compliance ────────────────────────────────────────

func TestParsersImplementInterface(t *testing.T) {
	// Compile-time assertions that all three parsers satisfy fileParser.
	var _ fileParser = (*documentParserImpl)(nil)
	var _ fileParser = (*imageParserImpl)(nil)
	var _ fileParser = (*textParserImpl)(nil)
}
