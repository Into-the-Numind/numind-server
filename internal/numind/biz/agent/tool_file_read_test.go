package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// ─── mock fileParser ────────────────────────────────────────────────────────

type mockFileParser struct {
	content   string
	pageCount int
	truncated bool
	err       error
}

func (m *mockFileParser) Parse(_ context.Context, _, _ string) (string, int, bool, error) {
	return m.content, m.pageCount, m.truncated, m.err
}

// ─── helpers ────────────────────────────────────────────────────────────────

// newHeadServer starts an httptest.Server that replies to HEAD with the given
// status, Content-Type, and Content-Length headers.
func newHeadServer(t *testing.T, status int, contentType string, contentLength int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if contentLength >= 0 {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLength))
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// baseURL builds a well-formed agent-attachments URL for user 123 on the given server.
func baseURL(srvURL string) string {
	return srvURL + "/agent-attachments/123/test-file.pdf"
}

// ctxUser123 returns a context carrying userID 123.
func ctxUser123() context.Context {
	return middleware.NewContextWithUserID(context.Background(), 123)
}

// ─── Execute tests ──────────────────────────────────────────────────────────

func TestFileReadTool_Execute_HappyPDF(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 2048)

	parser := &mockFileParser{content: "## Report\n\nPage 1 content.", pageCount: 3, truncated: false}
	tool := &fileReadTool{pdfParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if out.MimeType != "application/pdf" {
		t.Errorf("expected mime application/pdf, got %q", out.MimeType)
	}
	if out.Content != parser.content {
		t.Errorf("unexpected content: %q", out.Content)
	}
	if out.PageCount != 3 {
		t.Errorf("expected page_count 3, got %d", out.PageCount)
	}
	if out.Truncated {
		t.Error("expected truncated=false")
	}
}

func TestFileReadTool_Execute_HappyImage(t *testing.T) {
	srv := newHeadServer(t, 200, "image/jpeg", 51200)

	parser := &mockFileParser{content: "detected text from image", truncated: false}
	tool := &fileReadTool{imageParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if out.Content != parser.content {
		t.Errorf("unexpected content: %q", out.Content)
	}
}

func TestFileReadTool_Execute_HappyText(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 512)

	parser := &mockFileParser{content: "Hello, World!", truncated: false}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if out.Content != "Hello, World!" {
		t.Errorf("unexpected content: %q", out.Content)
	}
}

func TestFileReadTool_Execute_HappyMarkdown(t *testing.T) {
	srv := newHeadServer(t, 200, "text/markdown", 1024)

	parser := &mockFileParser{content: "# Title\n\nContent.", truncated: false}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if out.MimeType != "text/markdown" {
		t.Errorf("expected mime text/markdown, got %q", out.MimeType)
	}
}

func TestFileReadTool_Execute_UnsupportedMIME(t *testing.T) {
	srv := newHeadServer(t, 200, "application/zip", 10240)

	tool := &fileReadTool{headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error for unsupported MIME type")
	}
	var e *errno.Errno
	if !errors.As(err, &e) {
		t.Fatalf("expected *errno.Errno, got %T: %v", err, err)
	}
	if e.Code != errno.ErrInvalidParameter.Code {
		t.Errorf("expected ErrInvalidParameter code, got %q", e.Code)
	}
}

func TestFileReadTool_Execute_UserIDMismatch(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 1024)

	tool := &fileReadTool{headFn: http.Head}
	// URL has user 123; context has user 999.
	ctx := middleware.NewContextWithUserID(context.Background(), 999)
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctx, ToolInput(input))
	if err == nil {
		t.Fatal("expected permission error for user ID mismatch")
	}
	var e *errno.Errno
	if !errors.As(err, &e) {
		t.Fatalf("expected *errno.Errno, got %T: %v", err, err)
	}
	if e.Code != errno.ErrPermissionDenied.Code {
		t.Errorf("expected ErrPermissionDenied code, got %q", e.Code)
	}
}

func TestFileReadTool_Execute_URLFormatInvalid(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	// URL does not contain /agent-attachments/<id>/ segment.
	input, _ := json.Marshal(fileReadInput{FileURL: "https://cdn.example.com/uploads/file.pdf"})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error for invalid URL format")
	}
	var e *errno.Errno
	if !errors.As(err, &e) {
		t.Fatalf("expected *errno.Errno, got %T: %v", err, err)
	}
	if e.Code != errno.ErrInvalidParameter.Code {
		t.Errorf("expected ErrInvalidParameter code, got %q", e.Code)
	}
}

func TestFileReadTool_Execute_EmptyFileURL(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: ""})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error for empty file_url")
	}
}

func TestFileReadTool_Execute_BadInputJSON(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	_, err := tool.Execute(ctxUser123(), ToolInput([]byte("not-json")))
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestFileReadTool_Execute_Truncated(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", int64(fileReadMaxBytes+1))

	parser := &mockFileParser{content: "some content", truncated: true}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if !out.Truncated {
		t.Error("expected truncated=true when parser reports truncation")
	}
}

func TestFileReadTool_Execute_HeadHTTPError(t *testing.T) {
	srv := newHeadServer(t, 404, "text/plain", 0)

	tool := &fileReadTool{headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error for HEAD 404")
	}
}

func TestFileReadTool_Execute_ParserError(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 512)

	parser := &mockFileParser{err: errors.New("parse failed")}
	tool := &fileReadTool{pdfParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err == nil {
		t.Fatal("expected error when parser returns error")
	}
}

// ─── Metadata tests ─────────────────────────────────────────────────────────

func TestFileReadTool_Metadata(t *testing.T) {
	tool := &fileReadTool{}
	if tool.Name() != "file_read" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
	if !tool.IsReadOnly() {
		t.Error("file_read should be read-only")
	}
	if !tool.IsSearchOrReadCommand() {
		t.Error("file_read should be IsSearchOrReadCommand")
	}
	if !tool.AlwaysLoad() {
		t.Error("file_read should AlwaysLoad")
	}
}

// ─── extractUserIDFromURL tests ──────────────────────────────────────────────

func TestExtractUserIDFromURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantID  uint
		wantErr bool
	}{
		{"valid", "https://cdn.example.com/agent-attachments/42/file.pdf", 42, false},
		{"no-segment", "https://cdn.example.com/uploads/file.pdf", 0, true},
		{"zero-id", "https://cdn.example.com/agent-attachments/0/file.pdf", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := extractUserIDFromURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.wantID {
				t.Errorf("expected ID %d, got %d", tc.wantID, id)
			}
		})
	}
}
