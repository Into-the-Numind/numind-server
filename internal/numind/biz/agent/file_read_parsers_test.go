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

func TestTextParserImpl_Parse_TruncatesLargeContent(t *testing.T) {
	// Build a body that is exactly fileReadMaxBytes+1 bytes.
	big := strings.Repeat("x", fileReadMaxBytes+1)
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
	if !truncated {
		t.Error("expected truncated=true for oversized content")
	}
	if len(content) != fileReadMaxBytes {
		t.Errorf("expected content len %d, got %d", fileReadMaxBytes, len(content))
	}
}

func TestTextParserImpl_Parse_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	parser := &textParserImpl{}
	// A 500 from the server still results in a successful HTTP round-trip;
	// the body will contain the error text. We validate no network error occurs.
	_, _, _, err := parser.Parse(context.Background(), srv.URL+"/file.txt", "")
	// err should be nil — the 500 body is just text; we don't parse status.
	if err != nil {
		t.Fatalf("unexpected error from textParserImpl on 500: %v", err)
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
	// Content exactly equal to fileReadMaxBytes should NOT be truncated.
	exact := strings.Repeat("a", fileReadMaxBytes)
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
	if len(content) != fileReadMaxBytes {
		t.Errorf("expected content len %d, got %d", fileReadMaxBytes, len(content))
	}
}

// ─── fileParser interface compliance ────────────────────────────────────────

func TestParsersImplementInterface(t *testing.T) {
	// Compile-time assertions that all three parsers satisfy fileParser.
	var _ fileParser = (*pdfParserImpl)(nil)
	var _ fileParser = (*imageParserImpl)(nil)
	var _ fileParser = (*textParserImpl)(nil)
}
