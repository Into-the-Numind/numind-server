package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// ─── mock fileParser ────────────────────────────────────────────────────────

type mockFileParser struct {
	content   string
	pageCount int
	truncated bool
	err       error
	calls     int
}

func TestFileRead_LangfuseContainsOnlySafePaginationMetadata(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 128)
	secretContent := "customer-secret-file-content"
	secretPrompt := "extract customer-secret-instruction"
	tool := &fileReadTool{textParser: &mockFileParser{content: secretContent}, headFn: http.Head}
	events := capturePipelineLangfuseEvents(t)
	ctx := middleware.NewContextWithUserID(langfuse.WithTrace(context.Background(), "file-safe-trace"), 123)
	input, err := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL), Prompt: secretPrompt})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, ToolInput(input))

	require.NoError(t, err)
	require.Contains(t, string(result), secretContent, "tool result remains functionally unchanged")
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.file_read.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	assert.Equal(t, map[string]any{}, created.Input)
	output, ok := updated.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text/plain", output["mime_type"])
	assert.Equal(t, 0, output["offset"])
	assert.Equal(t, fileReadDefaultLimitBytes, output["limit_bytes"])
	assert.Equal(t, len(secretContent), output["returned_bytes"])
	assert.Equal(t, false, output["has_more"])
	assert.Equal(t, pipelineToolTraceNoError, output["error_class"])
	assert.Contains(t, output, "duration_ms")

	encoded := pipelineEventsJSON(t, *events)
	for _, secret := range []string{srv.URL, "test-file.pdf", secretPrompt, secretContent, "sha256:"} {
		assert.NotContains(t, encoded, secret)
	}
	for _, forbiddenKey := range []string{"file_url", "prompt", "content", "read_token"} {
		assert.NotContains(t, encoded, `"`+forbiddenKey+`"`)
	}
}

func TestFileRead_LangfuseErrorUsesFixedClassWithoutRawParserError(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 128)
	secretError := "provider leaked customer-secret-error-body"
	tool := &fileReadTool{textParser: &mockFileParser{err: errors.New(secretError)}, headFn: http.Head}
	events := capturePipelineLangfuseEvents(t)
	ctx := middleware.NewContextWithUserID(langfuse.WithTrace(context.Background(), "file-error-trace"), 123)
	input, err := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, ToolInput(input))

	require.NoError(t, err, "parser failures remain model-correctable soft errors")
	require.NotContains(t, string(result), secretError)
	require.Contains(t, string(result), `"error":"ERROR: parse_failed"`)
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.file_read.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	assert.Equal(t, "ERROR", updated.Level)
	assert.Equal(t, "parse_error", updated.StatusMessage)
	assert.NotContains(t, pipelineEventsJSON(t, *events), secretError)
}

func TestFileReadTool_Execute_RejectsUnmanagedURLBeforeNetwork(t *testing.T) {
	headCalled := false
	tool := &fileReadTool{
		textParser: &mockFileParser{content: "must not run"},
		headFn: func(string) (*http.Response, error) {
			headCalled = true
			return nil, errors.New("must not reach network")
		},
		presignFn: func(context.Context, string, string, int64) (string, error) {
			return "", errors.New("must not presign")
		},
	}
	input, err := json.Marshal(fileReadInput{
		FileURL: "http://169.254.169.254/latest/meta-data?next=/agent-attachments/123/secret.txt",
	})
	require.NoError(t, err)

	events := capturePipelineLangfuseEvents(t)
	ctx := middleware.NewContextWithUserID(langfuse.WithTrace(context.Background(), "file-preflight-trace"), 123)
	result, err := tool.Execute(ctx, input)

	require.NoError(t, err)
	assert.False(t, headCalled)
	assert.Contains(t, string(result), `"error":"ERROR: unmanaged_file_url"`)
	assert.NotContains(t, string(result), "169.254.169.254")
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.file_read.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	assert.Equal(t, "preflight_error", updated.StatusMessage)
	assert.NotContains(t, pipelineEventsJSON(t, *events), "169.254.169.254")
}

func (m *mockFileParser) Parse(_ context.Context, _, _ string) (string, int, bool, error) {
	m.calls++
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

func TestFileReadTool_AttachmentIDPagesCanonicalCacheWithoutNetworkOrParser(t *testing.T) {
	content := strings.Repeat("中文A", 64)
	sum := sha256.Sum256([]byte(content))
	att := &model.AgentAttachment{
		ID: 901, UserID: 123,
		URL:      "https://bucket.cos.ap-chengdu.myqcloud.com/agent-attachments/123/customer.md",
		Filename: "customer.md", MimeType: "text/markdown", Size: 1234,
		Modality: attachment.ModalityText, FallbackReady: true,
		ParsedContent: &content, ParsedContentSHA256: fmt.Sprintf("sha256:%x", sum),
		ParsedContentByteSize: int64(len(content)),
	}
	store := newStubStore(att)
	parser := &mockFileParser{content: "must not parse"}
	headCalled := false
	tool := &fileReadTool{
		attachmentStore: store,
		textParser:      parser,
		headFn: func(string) (*http.Response, error) {
			headCalled = true
			return nil, errors.New("must not HEAD cached attachment")
		},
	}

	limit := 17
	var rebuilt strings.Builder
	offset := 0
	readToken := ""
	for {
		input, err := json.Marshal(fileReadInput{
			AttachmentID: att.ID, Offset: &offset, LimitBytes: &limit, ReadToken: readToken,
		})
		require.NoError(t, err)
		result, err := tool.Execute(ctxUser123(), input)
		require.NoError(t, err)
		var out fileReadOutput
		require.NoError(t, json.Unmarshal(result, &out))
		require.Empty(t, out.Error)
		rebuilt.WriteString(out.Content)
		if !out.HasMore {
			break
		}
		offset = out.NextOffset
		readToken = out.ReadToken
	}

	assert.Equal(t, content, rebuilt.String())
	assert.Zero(t, parser.calls)
	assert.False(t, headCalled)
}

func TestFileReadTool_LegacyUploadURLResolvesCanonicalCache(t *testing.T) {
	content := "cached customer profile"
	att := &model.AgentAttachment{
		ID: 902, UserID: 123,
		URL:      "https://bucket.cos.ap-chengdu.myqcloud.com/agent-attachments/123/profile.txt",
		Filename: "profile.txt", MimeType: "text/plain", Size: int64(len(content)),
		Modality: attachment.ModalityText, FallbackReady: true, ParsedContent: &content,
	}
	store := newStubStore(att)
	parser := &mockFileParser{content: "must not parse"}
	headCalled := false
	tool := &fileReadTool{
		attachmentStore: store, textParser: parser,
		headFn: func(string) (*http.Response, error) {
			headCalled = true
			return nil, errors.New("must not HEAD cached attachment")
		},
	}

	input, _ := json.Marshal(fileReadInput{FileURL: att.URL})
	result, err := tool.Execute(ctxUser123(), input)
	require.NoError(t, err)
	var out fileReadOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, content, out.Content)
	assert.Zero(t, parser.calls)
	assert.False(t, headCalled)
}

func TestFileReadTool_ManagedCacheStatusAndOwnershipAreSoftErrors(t *testing.T) {
	parseErr := "secret provider detail"
	pending := &model.AgentAttachment{ID: 903, UserID: 123, Filename: "pending.docx"}
	failed := &model.AgentAttachment{ID: 904, UserID: 123, Filename: "failed.docx", FallbackReady: true, FallbackError: &parseErr}
	tool := &fileReadTool{attachmentStore: newStubStore(pending, failed)}

	for _, tc := range []struct {
		id   uint64
		want string
	}{
		{pending.ID, "file_processing"},
		{failed.ID, "parse_failed"},
		{999999, "file_not_owned_or_missing"},
	} {
		input, _ := json.Marshal(fileReadInput{AttachmentID: tc.id})
		result, err := tool.Execute(ctxUser123(), input)
		require.NoError(t, err)
		assert.Contains(t, string(result), tc.want)
		assert.NotContains(t, string(result), parseErr)
	}
}

func TestFileReadTool_WaitsBrieflyForUploadWorkerWithoutParsing(t *testing.T) {
	pending := &model.AgentAttachment{ID: 905, UserID: 123, Filename: "soon.txt", MimeType: "text/plain"}
	store := newStubStore(pending)
	parser := &mockFileParser{content: "must not parse"}
	tool := &fileReadTool{attachmentStore: store, textParser: parser, cacheWait: 500 * time.Millisecond}

	go func() {
		time.Sleep(25 * time.Millisecond)
		content := "worker cached content"
		store.mu.Lock()
		store.rows[pending.ID].ParsedContent = &content
		store.rows[pending.ID].FallbackReady = true
		store.mu.Unlock()
	}()

	input, _ := json.Marshal(fileReadInput{AttachmentID: pending.ID})
	result, err := tool.Execute(ctxUser123(), input)
	require.NoError(t, err)
	var out fileReadOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "worker cached content", out.Content)
	assert.Zero(t, parser.calls)
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
	if out.Content != "ERROR: unsupported_mime" {
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
	if out.Content != "ERROR: file_not_owned" {
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
	if out.Content != "ERROR: unmanaged_file_url" {
		t.Errorf("expected soft error mentioning agent- path format, got: %s", out.Content)
	}
}

func TestFileReadTool_Execute_MissingAttachmentReference_ReturnsSoftError(t *testing.T) {
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
	if out.Content != "ERROR: attachment_reference_required" {
		t.Errorf("expected soft error about missing attachment reference, got: %s", out.Content)
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
	if out.Content != "ERROR: invalid_input_json" {
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
	if out.Content != "ERROR: unauthenticated" {
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
	srv := newHeadServer(t, 200, "text/plain", int64(fileReadDefaultLimitBytes+1))

	parser := &mockFileParser{content: strings.Repeat("x", fileReadDefaultLimitBytes+1)}
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
		t.Error("expected truncated=true when normalized content has another page")
	}
	if !out.HasMore || out.Truncated != out.HasMore {
		t.Error("truncated must equal has_more")
	}
}

func TestFileReadTool_Execute_Exact64KiBIsTerminal(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", int64(fileReadDefaultLimitBytes))
	parser := &mockFileParser{content: strings.Repeat("x", fileReadDefaultLimitBytes)}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var out fileReadOutput
	_ = json.Unmarshal(result, &out)
	if out.ReturnedBytes != fileReadDefaultLimitBytes || out.HasMore || out.Truncated || out.NextOffset != fileReadDefaultLimitBytes {
		t.Fatalf("exact 64 KiB must be one terminal page: %+v", out)
	}
}

func TestFileReadTool_Execute_ResumableReassemblesCompleteMixedUTF8(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 400*1024)
	content := strings.Repeat("中文🙂-agent-file-read\n", 18*1024)
	parser := &mockFileParser{content: content}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}
	fileURL := baseURL(srv.URL)

	offset := 0
	readToken := ""
	var rebuilt strings.Builder
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 20 {
			t.Fatal("pagination did not terminate")
		}
		limit := fileReadDefaultLimitBytes
		input, err := json.Marshal(fileReadInput{FileURL: fileURL, Offset: &offset, LimitBytes: &limit, ReadToken: readToken})
		if err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(ctxUser123(), input)
		if err != nil {
			t.Fatalf("page %d failed: %v", pageNumber, err)
		}
		var out fileReadOutput
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(out.Content) {
			t.Fatalf("page %d contains invalid UTF-8", pageNumber)
		}
		if out.Offset != offset || out.ReturnedBytes != len(out.Content) {
			t.Fatalf("page %d offsets inconsistent: %+v", pageNumber, out)
		}
		if pageNumber == 0 {
			readToken = out.ReadToken
		} else if out.ReadToken != readToken {
			t.Fatalf("read token changed across pages")
		}
		rebuilt.WriteString(out.Content)
		offset = out.NextOffset
		if !out.HasMore {
			if out.Truncated {
				t.Fatal("terminal page must have truncated=false")
			}
			break
		}
	}

	if rebuilt.String() != content {
		t.Fatalf("reassembled content mismatch: got %d bytes want %d", rebuilt.Len(), len(content))
	}
	wantHash := sha256.Sum256([]byte(content))
	wantToken := fmt.Sprintf("sha256:%x", wantHash)
	if readToken != wantToken {
		t.Fatalf("read_token=%q want %q", readToken, wantToken)
	}
}

func TestFileReadTool_Execute_UTF8BoundaryOffsetAndLimitValidation(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 9)
	parser := &mockFileParser{content: "A你🙂B"}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}
	fileURL := baseURL(srv.URL)
	for _, multiByteRune := range []string{"é", "你", "🙂"} {
		parser.content = "A" + multiByteRune + "B"
		boundaryLimit := 2 // lands inside each 2/3/4-byte rune after the ASCII prefix.
		boundaryOffset := 0
		boundaryInput, _ := json.Marshal(fileReadInput{FileURL: fileURL, Offset: &boundaryOffset, LimitBytes: &boundaryLimit})
		boundaryResult, boundaryErr := tool.Execute(ctxUser123(), boundaryInput)
		if boundaryErr != nil {
			t.Fatal(boundaryErr)
		}
		var boundaryPage fileReadOutput
		_ = json.Unmarshal(boundaryResult, &boundaryPage)
		if boundaryPage.Content != "A" || boundaryPage.NextOffset != 1 || !utf8.ValidString(boundaryPage.Content) {
			t.Fatalf("rune %q was split: %+v", multiByteRune, boundaryPage)
		}
	}
	parser.content = "A你🙂B"

	limit := 5 // byte 5 lands inside the four-byte emoji; end must retreat to byte 4.
	zero := 0
	input, _ := json.Marshal(fileReadInput{FileURL: fileURL, Offset: &zero, LimitBytes: &limit})
	result, err := tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var first fileReadOutput
	_ = json.Unmarshal(result, &first)
	if first.Content != "A你" || first.ReturnedBytes != 4 || first.NextOffset != 4 || !first.HasMore {
		t.Fatalf("unexpected rune-safe first page: %+v", first)
	}

	badOffset := 2 // middle of 你.
	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &badOffset, LimitBytes: &limit, ReadToken: first.ReadToken})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var soft fileReadOutput
	_ = json.Unmarshal(result, &soft)
	if soft.Error != "ERROR: invalid_utf8_boundary" {
		t.Fatalf("expected UTF-8 boundary soft error, got %+v", soft)
	}

	for _, invalid := range []int{0, fileReadMaxLimitBytes + 1} {
		input, _ = json.Marshal(fileReadInput{FileURL: fileURL, LimitBytes: &invalid})
		result, err = tool.Execute(ctxUser123(), input)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(result, &soft)
		if soft.Error == "" {
			t.Fatalf("limit_bytes=%d should be a soft error", invalid)
		}
	}

	one := 1
	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &zero, LimitBytes: &one})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(result, &first)
	if first.Content != "A" || first.ReturnedBytes != 1 {
		t.Fatalf("limit_bytes=1 must remain valid for ASCII: %+v", first)
	}
}

func TestFileReadTool_Execute_OffsetEndAndReadTokenChange(t *testing.T) {
	srv := newHeadServer(t, 200, "text/plain", 6)
	parser := &mockFileParser{content: "abcdef"}
	tool := &fileReadTool{textParser: parser, headFn: http.Head}
	fileURL := baseURL(srv.URL)
	limit := 3
	zero := 0
	input, _ := json.Marshal(fileReadInput{FileURL: fileURL, Offset: &zero, LimitBytes: &limit})
	result, err := tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var first fileReadOutput
	_ = json.Unmarshal(result, &first)

	atEnd := len(parser.content)
	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &atEnd, LimitBytes: &limit, ReadToken: first.ReadToken})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var terminal fileReadOutput
	_ = json.Unmarshal(result, &terminal)
	if terminal.Content != "" || terminal.HasMore || terminal.NextOffset != atEnd {
		t.Fatalf("offset==content_bytes must be a clean terminal page: %+v", terminal)
	}

	beyond := len(parser.content) + 1
	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &beyond, LimitBytes: &limit, ReadToken: first.ReadToken})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	var soft fileReadOutput
	_ = json.Unmarshal(result, &soft)
	if !strings.Contains(soft.Error, "offset") {
		t.Fatalf("offset beyond content must be a soft error: %+v", soft)
	}

	parser.content = "abcXYZ" // same URL changed between pages.
	continuation := first.NextOffset
	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &continuation, LimitBytes: &limit, ReadToken: first.ReadToken})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(result, &soft)
	if !strings.Contains(soft.Error, "read_token") {
		t.Fatalf("changed content must reject continuation: %+v", soft)
	}

	input, _ = json.Marshal(fileReadInput{FileURL: fileURL, Offset: &continuation, LimitBytes: &limit})
	result, err = tool.Execute(ctxUser123(), input)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(result, &soft)
	if soft.Error != "ERROR: read_token_required" {
		t.Fatalf("continuation without read_token must be rejected: %+v", soft)
	}
}

func TestFileReadTool_InputSchemaContainsResumableFields(t *testing.T) {
	tool := &fileReadTool{}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, field := range []string{"attachment_id", "offset", "limit_bytes", "read_token"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("InputSchema missing %s", field)
		}
	}
	if properties["limit_bytes"].(map[string]any)["maximum"] != float64(fileReadMaxLimitBytes) {
		t.Fatalf("limit_bytes maximum must be %d", fileReadMaxLimitBytes)
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
	if out.Content != "ERROR: head_http_error" {
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
	if out.Content != "ERROR: parse_failed" {
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
		{"segment-only-in-query", "http://169.254.169.254/meta?next=/agent-attachments/42/file.pdf", 0, true},
		{"segment-only-in-fragment", "http://127.0.0.1/meta#/agent-outputs/42/file.pdf", 0, true},
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

// TestFileReadTool_Execute_NonCOSURLIsRejected confirms production file_read
// only accepts platform-managed COS objects. This prevents a user-controlled
// URL from turning the server-side HEAD/parser fetch into SSRF.
func TestFileReadTool_Execute_NonCOSURLIsRejected(t *testing.T) {
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

	// httptest URL is NOT a managed COS URL — reject before HEAD or presign.
	input, _ := json.Marshal(fileReadInput{FileURL: baseURL(srv.URL)})
	result, err := tool.Execute(ctxUser123(), ToolInput(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if presignCalled {
		t.Error("presignFn must NOT be called for non-COS URLs")
	}
	assert.Contains(t, string(result), `"error":"ERROR: unmanaged_file_url"`)
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
	if !strings.Contains(out.Content, "ERROR: presign_failed") || strings.Contains(out.Content, "cos creds missing") {
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
	if !strings.Contains(out.Content, "ERROR: presign_failed") || strings.Contains(out.Content, "rate-limited") {
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
