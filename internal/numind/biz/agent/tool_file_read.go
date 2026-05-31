package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"
)

// fileReadInput is the JSON input for the file_read tool.
type fileReadInput struct {
	FileURL string `json:"file_url"`
	Prompt  string `json:"prompt,omitempty"`
}

// fileReadOutput is the JSON output returned by the file_read tool.
type fileReadOutput struct {
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	Content   string `json:"content"`
	PageCount int    `json:"page_count,omitempty"`
	ByteSize  int    `json:"byte_size"`
	Truncated bool   `json:"truncated"`
}

// fileParser is the narrow interface for each MIME-specific backend.
type fileParser interface {
	Parse(ctx context.Context, fileURL, prompt string) (content string, pageCount int, truncated bool, err error)
}

// fileReadTool implements FullTool for the file_read built-in.
// Parsers are injected at construction (T7 wires defaults via NewFileReadTool).
type fileReadTool struct {
	BaseTool
	pdfParser   fileParser // application/pdf
	imageParser fileParser // image/*  (OCR)
	textParser  fileParser // text/plain, text/markdown
	// headFn is the HTTP HEAD function; swapped in tests to avoid network calls.
	headFn func(url string) (*http.Response, error)
	// presignFn signs a COS object key for transient anonymous fetch, BOUND TO
	// AN HTTP METHOD. Tencent COS signs (method, URI, expiry, …) — a URL signed
	// for GET will get 403 if requested with HEAD. file_read therefore signs
	// twice per execution: once for HEAD (the probe) and once for GET (the
	// downstream parser fetch — qwen-long server-side fetch, OCR fetch, text
	// http.Get all issue GET).
	//
	// Swapped to a stub in tests. nil = bypass (any non-COS URL flows through
	// unchanged).
	presignFn func(ctx context.Context, method, objectKey string, expirySeconds int64) (string, error)
}

// NewFileReadTool constructs a fileReadTool with injected parsers.
// T7 (runner wiring) calls this with the production implementations.
func NewFileReadTool(pdf, img, txt fileParser) FullTool {
	return &fileReadTool{
		pdfParser:   pdf,
		imageParser: img,
		textParser:  txt,
		headFn:      http.Head,
		presignFn:   util.GenerateSignedURLForMethod,
	}
}

var _ FullTool = (*fileReadTool)(nil)

func (t *fileReadTool) Name() string { return "file_read" }
func (t *fileReadTool) Description() string {
	return "Read the contents of an uploaded file by URL. " +
		"Input: { file_url: string, prompt?: string }. " +
		"Returns: { file_name, mime_type, content, page_count?, byte_size, truncated }."
}
func (t *fileReadTool) UserFacingName() string      { return "读取文件" }
func (t *fileReadTool) NarrationVerb() string       { return "读取文件" }
func (t *fileReadTool) IsReadOnly() bool            { return true }
func (t *fileReadTool) IsSearchOrReadCommand() bool { return true }
func (t *fileReadTool) AlwaysLoad() bool            { return true }

// fileReadMaxBytes is the maximum content size returned per file read.
const fileReadMaxBytes = 200 * 1024

// attachmentPathRE matches /agent-attachments/<userID>/ OR /agent-outputs/<userID>/
// in a URL path. Both prefixes are user-owned: agent-attachments holds files the
// user uploaded (biz/attachment/upload.go:126), agent-outputs holds files generated
// by tools like create_text / create_csv / create_html / create_json / run_python
// during the same agent run (tool_create_helpers.go:90). file_read accepts both so
// the LLM can read back its own generated artifacts within the same conversation.
// The ownership check downstream (ctxUserID == urlUserID) is unchanged — only the
// recognised URL surface widens.
var attachmentPathRE = regexp.MustCompile(`/agent-(?:attachments|outputs)/(\d+)/`)

// cosURLPathRE matches a Tencent COS attachment URL and captures the object
// key (everything after the host).
//
//	https://numind-dev-xxx.cos.ap-chengdu.myqcloud.com/agent-attachments/1/x.pdf
//	  → captured group: "agent-attachments/1/x.pdf"
//
// Non-COS URLs (httptest fixtures, public CDN links pasted by an admin, etc.)
// will NOT match — Execute treats them as pass-through and skips presigning.
var cosURLPathRE = regexp.MustCompile(`^https?://[^/]+\.cos\.[^/]+\.myqcloud\.com/(.+)$`)

// extractCOSObjectKey returns the COS object key for a COS bucket URL and a
// boolean indicating whether the URL was recognized as COS. The recognized
// form matches cosURLPathRE — bucket.cos.region.myqcloud.com hosts.
func extractCOSObjectKey(fileURL string) (string, bool) {
	m := cosURLPathRE.FindStringSubmatch(fileURL)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

func (t *fileReadTool) returnSoftError(fileName, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(fileReadOutput{
		FileName:  fileName,
		MimeType:  "application/octet-stream",
		Content:   "ERROR: " + msg,
		ByteSize:  len(msg) + 7,
		Truncated: false,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *fileReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_url": {"type": "string", "format": "uri", "description": "URL of the uploaded file to read (e.g. an agent attachment)."},
			"prompt":   {"type": "string", "description": "Optional instruction describing what to extract from the file."}
		},
		"required": ["file_url"]
	}`)
}

// Execute reads the file at the given URL, verifies ownership, detects MIME type,
// dispatches to the appropriate parser, and returns structured JSON output.
//
// All validation failures are returned as soft errors (ToolResult with "ERROR:"
// content + nil Go error). Returning a Go error here would propagate to Eino as
// a NodeRunError which TERMINATES the agent run before the LLM ever sees the
// message — see tool_web_fetch.go:80-95 for the canonical rationale. Aligns with
// Codex `RespondToModel` (codex-rs/tools/src/function_call_error.rs) and Claude
// Code `ValidationResult` (FileReadTool.ts) patterns.
func (t *fileReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in fileReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return t.returnSoftError("", "invalid input JSON: %s", err.Error())
	}
	if in.FileURL == "" {
		return t.returnSoftError("", "file_url is required")
	}

	// Verify the file belongs to the requesting user.
	ctxUserID, ok := middleware.UserIDFromCtx(ctx)
	if !ok || ctxUserID == 0 {
		return t.returnSoftError(path.Base(in.FileURL), "user not authenticated")
	}
	urlUserID, err := extractUserIDFromURL(in.FileURL)
	if err != nil {
		return t.returnSoftError(path.Base(in.FileURL), "%s", err.Error())
	}
	if ctxUserID != urlUserID {
		return t.returnSoftError(path.Base(in.FileURL), "file not owned by current user")
	}

	// Langfuse span for the full tool execution.
	var spanID string
	var traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.file_read.execute",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(in),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	// Translate private COS URLs into time-bounded presigned URLs. TWO signed
	// URLs are minted: one for HEAD (the cheap probe used here) and one for
	// GET (downstream fetch by qwen-long, OCR, text http.Get). Tencent COS
	// signing is METHOD-BOUND — a GET URL hit with HEAD returns 403
	// "SignatureDoesNotMatch". The customer-facing canonical URL (in.FileURL)
	// is preserved for filename / output. Non-COS URLs short-circuit (admin-
	// pasted public links must keep working with a single URL pass-through).
	headURL := in.FileURL
	fetchURL := in.FileURL // for parsers (GET-style downstream fetch)
	if objectKey, ok := extractCOSObjectKey(in.FileURL); ok && t.presignFn != nil {
		// 1h validity is comfortably longer than any single tool call.
		signedHead, signErr := t.presignFn(ctx, http.MethodHead, objectKey, 3600)
		if signErr != nil {
			return t.returnSoftError(path.Base(in.FileURL), "presign COS URL (HEAD) failed: %v", signErr)
		}
		signedGet, signErr := t.presignFn(ctx, http.MethodGet, objectKey, 3600)
		if signErr != nil {
			return t.returnSoftError(path.Base(in.FileURL), "presign COS URL (GET) failed: %v", signErr)
		}
		headURL = signedHead
		fetchURL = signedGet
	}

	// HEAD the file to detect content type and byte size without downloading.
	headFn := t.headFn
	if headFn == nil {
		headFn = http.Head
	}
	headResp, err := headFn(headURL)
	if err != nil {
		return t.returnSoftError(path.Base(in.FileURL), "HEAD request failed: %v", err)
	}
	defer headResp.Body.Close()
	if headResp.StatusCode >= 400 {
		return t.returnSoftError(path.Base(in.FileURL), "HEAD returned HTTP status %d", headResp.StatusCode)
	}

	mimeType := headResp.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	byteSize := int(headResp.ContentLength)

	// Dispatch to the appropriate parser by MIME type.
	// Parsers receive fetchURL (GET-signed) — they each issue their own
	// downstream fetch (qwen-long server-side fetch / OCR fetch / text GET)
	// and would 403 against bare private COS URLs OR HEAD-signed URLs.
	var content string
	var pageCount int
	var truncated bool

	switch {
	case mimeType == "application/pdf":
		if t.pdfParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "PDF parser not configured")
		}
		content, pageCount, truncated, err = t.pdfParser.Parse(ctx, fetchURL, in.Prompt)
	case strings.HasPrefix(mimeType, "image/"):
		if t.imageParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "image parser not configured")
		}
		content, _, truncated, err = t.imageParser.Parse(ctx, fetchURL, in.Prompt)
	case mimeType == "text/plain" || mimeType == "text/markdown":
		if t.textParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "text parser not configured")
		}
		content, _, truncated, err = t.textParser.Parse(ctx, fetchURL, in.Prompt)
	default:
		return t.returnSoftError(path.Base(in.FileURL), "unsupported MIME type %q (supported: application/pdf, image/*, text/plain, text/markdown)", mimeType)
	}
	if err != nil {
		return t.returnSoftError(path.Base(in.FileURL), "parse error: %v", err)
	}

	// FileName comes from the canonical URL (not the presigned one), so the
	// user sees "report.pdf" instead of a long query-string-decorated key.
	out, _ := json.Marshal(fileReadOutput{
		FileName:  path.Base(in.FileURL),
		MimeType:  mimeType,
		Content:   content,
		PageCount: pageCount,
		ByteSize:  byteSize,
		Truncated: truncated,
	})
	return ToolResult(out), nil
}

// extractUserIDFromURL extracts the user ID from a URL path of the form
// /agent-attachments/<userID>/... or /agent-outputs/<userID>/...
func extractUserIDFromURL(fileURL string) (uint, error) {
	m := attachmentPathRE.FindStringSubmatch(fileURL)
	if len(m) < 2 {
		return 0, fmt.Errorf("URL must contain /agent-attachments/<userID>/ or /agent-outputs/<userID>/ path segment (got %q) — file_read only accepts URLs to your uploaded files or files you generated via create_text/create_csv/create_html/create_json/run_python in this conversation", fileURL)
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid user ID in URL: %w", err)
	}
	return uint(id), nil
}
