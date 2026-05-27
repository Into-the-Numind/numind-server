package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: unsupported MIME type") {
		t.Errorf("expected soft error in content, got: %s", out.Content)
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
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: HEAD returned HTTP status 404") {
		t.Errorf("expected soft error in content, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_ParserError(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 512)

	parser := &mockFileParser{err: errors.New("parse failed")}
	tool := &fileReadTool{pdfParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: parse error: parse failed") {
		t.Errorf("expected soft error in content, got: %s", out.Content)
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

// ─── extractCOSObjectKey tests ──────────────────────────────────────────────

func TestExtractCOSObjectKey(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		wantOK bool
		want   string
	}{
		{
			"cos-attachment",
			"https://numind-dev-1334169463.cos.ap-chengdu.myqcloud.com/agent-attachments/1/1779-x.pdf",
			true,
			"agent-attachments/1/1779-x.pdf",
		},
		{
			"cos-other-region",
			"https://my-bucket.cos.ap-beijing.myqcloud.com/cards/123/image.webp",
			true,
			"cards/123/image.webp",
		},
		{
			"cos-deep-path",
			"https://b.cos.ap-x.myqcloud.com/agent-attachments/42/2026/05/22/file.txt",
			true,
			"agent-attachments/42/2026/05/22/file.txt",
		},
		{
			"non-cos-public-cdn",
			"https://cdn.example.com/agent-attachments/1/file.pdf",
			false,
			"",
		},
		{
			"non-cos-httptest",
			"http://127.0.0.1:8080/agent-attachments/1/file.pdf",
			false,
			"",
		},
		{
			"empty",
			"",
			false,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractCOSObjectKey(tc.url)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── presign integration tests ──────────────────────────────────────────────
//
// Bug-from-customer 2026-05-22: COS attachment URLs returned by upload are
// private — file_read previously hit them with anonymous HEAD/GET and got
// HTTP 403, surfacing as "model_error" with no useful state_reason. The fix
// translates COS URLs into presigned URLs before HEAD and before any parser
// call. These tests lock the new contract.

// TestFileReadTool_Execute_COSURL_UsesPresignedURL is the primary regression
// guard. Without the presign step, the HEAD against a bare COS URL would 403
// and the test would fail with "HEAD returned HTTP 403". With the fix, the
// presignFn rewrites the URL to a httptest endpoint that accepts the request.
//
// ALSO locks the method-bound signing contract (bug-from-customer 2026-05-22
// take 2): presignFn is called TWICE — once with method=HEAD for the probe
// and once with method=GET for the parser fetch. COS signed URLs are method-
// bound; a GET URL hit with HEAD returns 403.
func TestFileReadTool_Execute_COSURL_UsesPresignedURL(t *testing.T) {
	// httptest server doubles as the "signed-URL endpoint" that returns 200.
	srv := newHeadServer(t, 200, "application/pdf", 2048)

	// Track every presignFn invocation so we can assert HEAD and GET were
	// both signed and that they produced DIFFERENT URLs (real COS signatures
	// differ by method; the test stub mimics that).
	type call struct {
		method string
		key    string
	}
	var calls []call
	presign := func(_ context.Context, method, objectKey string, _ int64) (string, error) {
		calls = append(calls, call{method: method, key: objectKey})
		return fmt.Sprintf("%s/signed-%s/%s", srv.URL, method, objectKey), nil
	}

	// Track that the parser sees the GET-signed URL, not the HEAD-signed one
	// (parser internally issues GET; HEAD-signed URL would 403).
	var parserSawURL string
	parser := &recordingParser{
		content: "PDF body",
		onCall:  func(url string) { parserSawURL = url },
	}
	tool := &fileReadTool{
		pdfParser: parser,
		headFn:    http.Head,
		presignFn: presign,
	}

	cosURL := "https://numind-dev-1.cos.ap-chengdu.myqcloud.com/agent-attachments/123/x.pdf"
	input, _ := json.Marshal(fileReadInput{FileURL: cosURL})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both methods must be signed (HEAD first, then GET).
	if len(calls) != 2 {
		t.Fatalf("presignFn should be called exactly twice (HEAD + GET); got %d calls: %+v", len(calls), calls)
	}
	if calls[0].method != http.MethodHead {
		t.Errorf("first presign call must be for HEAD; got %q", calls[0].method)
	}
	if calls[1].method != http.MethodGet {
		t.Errorf("second presign call must be for GET; got %q", calls[1].method)
	}
	for _, c := range calls {
		if c.key != "agent-attachments/123/x.pdf" {
			t.Errorf("presign called with wrong key: got %q want %q", c.key, "agent-attachments/123/x.pdf")
		}
	}

	// Parser must receive the GET-signed URL specifically.
	if parserSawURL == cosURL {
		t.Error("parser must NOT receive the bare COS URL; it must receive the GET-signed URL")
	}
	wantParserURL := fmt.Sprintf("%s/signed-GET/agent-attachments/123/x.pdf", srv.URL)
	if parserSawURL != wantParserURL {
		t.Errorf("parser URL mismatch: got %q want %q", parserSawURL, wantParserURL)
	}

	// FileName must come from the ORIGINAL URL, not the presigned one.
	var out fileReadOutput
	_ = json.Unmarshal(result, &out)
	if out.FileName != "x.pdf" {
		t.Errorf("FileName should come from canonical URL ('x.pdf'); got %q", out.FileName)
	}
}

// TestFileReadTool_Execute_NonCOSURL_BypassesPresign confirms public URLs
// (admin-pasted shareable links, e.g.) skip the presign path. This guard
// prevents anyone accidentally tightening the presign to "all URLs" and
// breaking public-URL use cases.
func TestFileReadTool_Execute_NonCOSURL_BypassesPresign(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 1024)

	presignCalled := false
	presign := func(_ context.Context, _, _ string, _ int64) (string, error) {
		presignCalled = true
		return "should-not-be-used", nil
	}

	parser := &mockFileParser{content: "ok"}
	tool := &fileReadTool{
		pdfParser: parser,
		headFn:    http.Head,
		presignFn: presign,
	}

	// httptest URL is NOT a COS URL — must skip presign.
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if presignCalled {
		t.Error("presignFn must NOT be called for non-COS URLs")
	}
}

// TestFileReadTool_Execute_COSURL_PresignFails surfaces presign errors as soft reject instead of ErrAIProviderError.
func TestFileReadTool_Execute_COSURL_PresignFails(t *testing.T) {
	presign := func(_ context.Context, _, _ string, _ int64) (string, error) {
		return "", fmt.Errorf("cos creds missing")
	}

	tool := &fileReadTool{
		headFn:    http.Head,
		presignFn: presign,
	}
	cosURL := "https://b.cos.ap-x.myqcloud.com/agent-attachments/123/x.pdf"
	input, _ := json.Marshal(fileReadInput{FileURL: cosURL})
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: presign COS URL (HEAD) failed: cos creds missing") {
		t.Errorf("expected soft error in content, got: %s", out.Content)
	}
}

// TestFileReadTool_Execute_COSURL_GetSignFails covers the second signing call
// failing while HEAD signing succeeded — must surface as soft reject instead of ErrAIProviderError.
func TestFileReadTool_Execute_COSURL_GetSignFails(t *testing.T) {
	callCount := 0
	presign := func(_ context.Context, method, _ string, _ int64) (string, error) {
		callCount++
		if method == http.MethodGet {
			return "", fmt.Errorf("cos GET sign rate-limited")
		}
		return "https://signed.example/head", nil
	}

	tool := &fileReadTool{
		headFn:    http.Head,
		presignFn: presign,
	}
	cosURL := "https://b.cos.ap-x.myqcloud.com/agent-attachments/123/x.pdf"
	input, _ := json.Marshal(fileReadInput{FileURL: cosURL})
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 presign calls (HEAD ok, GET fails); got %d", callCount)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: presign COS URL (GET) failed: cos GET sign rate-limited") {
		t.Errorf("expected soft error in content, got: %s", out.Content)
	}
}

// TestFileReadTool_Execute_COSURL_NilPresignFn falls back to the bare URL —
// this is the production posture when COS is disabled (cos.enabled=false in
// dev/local). The HEAD will hit the bare URL; if it works (public bucket) we
// pass, otherwise it 403s — same as before this fix. The point of this test
// is to lock the nil-safe behavior so a future refactor cannot crash on nil.
func TestFileReadTool_Execute_COSURL_NilPresignFn(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 1024)

	parser := &mockFileParser{content: "fallback ok"}
	tool := &fileReadTool{
		pdfParser: parser,
		headFn:    http.Head,
		presignFn: nil, // explicit nil → bypass presign
	}

	// Even though this URL "looks like" COS by host shape, the test server
	// is httptest, so cosURLPathRE won't match and extractCOSObjectKey
	// returns (_, false). But the assertion is that nil presignFn never
	// panics — covered for completeness.
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	_, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// recordingParser is a fileParser that records the URL it was called with.
type recordingParser struct {
	content   string
	pageCount int
	truncated bool
	err       error
	onCall    func(url string)
}

func (p *recordingParser) Parse(_ context.Context, fileURL, _ string) (string, int, bool, error) {
	if p.onCall != nil {
		p.onCall(fileURL)
	}
	return p.content, p.pageCount, p.truncated, p.err
}
