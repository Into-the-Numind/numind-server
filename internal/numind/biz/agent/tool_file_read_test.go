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
	tool := &fileReadTool{documentParser: parser, headFn: http.Head}

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

// TestFileReadTool_Execute_Docx is the file_read-layer regression for the docx
// bug: an office MIME must route to the documentParser, not fall through to the
// "unsupported MIME type" soft error.
func TestFileReadTool_Execute_Docx(t *testing.T) {
	const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	srv := newHeadServer(t, 200, docxMIME, 4096)

	parser := &mockFileParser{content: "会议纪要正文：第一项议程……", truncated: false}
	tool := &fileReadTool{documentParser: parser, headFn: http.Head}

	input, _ := json.Marshal(fileReadInput{FileURL: srv.URL + "/agent-attachments/123/会议纪要.docx"})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if strings.Contains(out.Content, "unsupported MIME type") {
		t.Fatalf("docx must not be rejected as unsupported: %q", out.Content)
	}
	if out.Content != parser.content {
		t.Errorf("expected document parser content, got %q", out.Content)
	}
}

// TestIsDocumentReadable covers MIME and extension-fallback routing.
func TestIsDocumentReadable(t *testing.T) {
	cases := []struct {
		mime, url string
		want      bool
	}{
		{"application/pdf", "x/y.pdf", true},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "x/y.docx", true},
		{"application/zip", "x/agent-attachments/1/report.docx", true},               // ext fallback
		{"application/octet-stream", "x/agent-attachments/1/old.doc?sign=abc", true}, // ext fallback + query
		{"image/png", "x/y.png", false},
		{"text/plain", "x/y.txt", false},
		{"text/plain", "x/y.pdf", false},               // specific MIME must NOT be hijacked by a .pdf name
		{"application/vnd.ms-excel", "x/y.xls", false}, // legacy .xls unsupported by parser
	}
	for _, c := range cases {
		if got := isDocumentReadable(c.mime, c.url); got != c.want {
			t.Errorf("isDocumentReadable(%q,%q)=%v want %v", c.mime, c.url, got, c.want)
		}
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
	// video/* with a non-document extension is genuinely unsupported. (A zip MIME
	// with a .docx/.pdf name is now correctly treated as a document, so it can no
	// longer stand in for "unsupported".)
	srv := newHeadServer(t, 200, "video/mp4", 10240)

	tool := &fileReadTool{headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: srv.URL + "/agent-attachments/123/clip.mp4"})
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

// All early-validation paths return a soft error (ToolResult with ERROR content,
// nil Go error). Returning a Go error would propagate to Eino as a NodeRunError
// and TERMINATE the agent run before the LLM ever sees the message — see
// tool_web_fetch.go:80-95 for the canonical rationale. The reference projects
// confirm: Codex `RespondToModel` vs `Fatal` (function_call_error.rs);
// Claude Code `ValidationResult { result: false, message, errorCode }`
// surfaced as a tool_result block (FileReadTool.ts).

func TestFileReadTool_Execute_UserIDMismatch_ReturnsSoftError(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 1024)

	tool := &fileReadTool{headFn: http.Head}
	// URL has user 123; context has user 999.
	ctx := middleware.NewContextWithUserID(context.Background(), 999)
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	res, err := tool.Execute(ctx, ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: file not owned by current user") {
		t.Errorf("expected soft error in content about ownership, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_URLFormatInvalid_ReturnsSoftError(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	// URL does not contain /agent-(attachments|outputs)/<id>/ segment.
	input, _ := json.Marshal(fileReadInput{FileURL: "https://cdn.example.com/uploads/file.pdf"})
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR:") || !strings.Contains(out.Content, "agent-") {
		t.Errorf("expected soft error mentioning agent- path format, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_EmptyFileURL_ReturnsSoftError(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: ""})
	res, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: file_url is required") {
		t.Errorf("expected soft error about missing file_url, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_BadInputJSON_ReturnsSoftError(t *testing.T) {
	tool := &fileReadTool{headFn: http.Head}
	res, err := tool.Execute(ctxUser123(), ToolInput([]byte("not-json")))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: invalid input JSON") {
		t.Errorf("expected soft error about invalid JSON, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_Unauthenticated_ReturnsSoftError(t *testing.T) {
	srv := newHeadServer(t, 200, "application/pdf", 1024)

	tool := &fileReadTool{headFn: http.Head}
	// context.Background carries no userID → middleware.UserIDFromCtx returns ok=false.
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	res, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out fileReadOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.Content, "ERROR: user not authenticated") {
		t.Errorf("expected soft error about authentication, got: %s", out.Content)
	}
}

// TestFileReadTool_Execute_AgentOutputsURL_ReadsSuccessfully covers the
// bug reported on 2026-05-29 (agent_run_id=83): LLM tried to read back its
// own generated artifact at /agent-outputs/<userID>/..., and file_read rejected
// it because the regex only accepted /agent-attachments/. Generated artifacts
// from create_text/create_csv/create_html/create_json/run_python all live
// under /agent-outputs/<userID>/ (see tool_create_helpers.go:90) and are
// legitimately owned by the same user — file_read MUST accept them.
func TestFileReadTool_Execute_AgentOutputsURL_ReadsSuccessfully(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 256)

	parser := &mockFileParser{content: "previously generated content", truncated: false}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}

	// URL contains /agent-outputs/123/ — same user as ctxUser123().
	input, _ := json.Marshal(fileReadInput{
		FileURL: srv.URL + "/agent-outputs/123/20260529-185416-generated_report.txt",
	})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error reading agent-outputs URL: %v", err)
	}

	var out fileReadOutput
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if strings.HasPrefix(out.Content, "ERROR:") {
		t.Errorf("agent-outputs URL should be accepted, got soft error: %s", out.Content)
	}
	if out.Content != parser.content {
		t.Errorf("expected content %q, got %q", parser.content, out.Content)
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
	tool := &fileReadTool{documentParser: parser, headFn: http.Head}

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
		{"valid-attachments", "https://cdn.example.com/agent-attachments/42/file.pdf", 42, false},
		{"valid-outputs", "https://cdn.example.com/agent-outputs/42/20260529-x.txt", 42, false},
		{"valid-outputs-deep", "https://b.cos.ap-x.myqcloud.com/agent-outputs/7/sub/dir/file.csv", 7, false},
		{"no-segment", "https://cdn.example.com/uploads/file.pdf", 0, true},
		{"zero-id-attachments", "https://cdn.example.com/agent-attachments/0/file.pdf", 0, false},
		{"zero-id-outputs", "https://cdn.example.com/agent-outputs/0/file.pdf", 0, false},
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
		documentParser: parser,
		headFn:         http.Head,
		presignFn:      presign,
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
		documentParser: parser,
		headFn:         http.Head,
		presignFn:      presign,
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
		documentParser: parser,
		headFn:         http.Head,
		presignFn:      nil, // explicit nil → bypass presign
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
