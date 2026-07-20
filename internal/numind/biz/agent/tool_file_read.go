package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"
)

// fileReadInput is the JSON input for the file_read tool.
type fileReadInput struct {
	FileURL    string `json:"file_url"`
	Prompt     string `json:"prompt,omitempty"`
	Offset     *int   `json:"offset,omitempty"`
	LimitBytes *int   `json:"limit_bytes,omitempty"`
	ReadToken  string `json:"read_token,omitempty"`
}

// fileReadOutput is the JSON output returned by the file_read tool.
type fileReadOutput struct {
	FileName        string `json:"file_name"`
	MimeType        string `json:"mime_type"`
	Content         string `json:"content"`
	PageCount       int    `json:"page_count,omitempty"`
	ByteSize        int    `json:"byte_size"`
	ContentByteSize int    `json:"content_byte_size"`
	Offset          int    `json:"offset"`
	ReturnedBytes   int    `json:"returned_bytes"`
	NextOffset      int    `json:"next_offset"`
	HasMore         bool   `json:"has_more"`
	ReadToken       string `json:"read_token"`
	Truncated       bool   `json:"truncated"`
	// Error is set ONLY on the soft-error path (returnSoftError), mirroring the
	// dedicated "error" field of web_search/web_fetch/image_gen so the Eino adapter's
	// soft-error detector (softToolErrorMessage) narrates StateError, not a false
	// "✓ success" badge. Omitted on a successful read (the model reads Content).
	Error string `json:"error,omitempty"`
}

// fileParser is the narrow interface for each MIME-specific backend.
type fileParser interface {
	Parse(ctx context.Context, fileURL, prompt string) (content string, pageCount int, truncated bool, err error)
}

// fileReadTool implements FullTool for the file_read built-in.
// Parsers are injected at construction (T7 wires defaults via NewFileReadTool).
type fileReadTool struct {
	BaseTool
	documentParser fileParser // application/pdf + office docs (docx/doc/pptx/xlsx/rtf), local parse
	imageParser    fileParser // image/*  (OCR)
	textParser     fileParser // text/plain, text/markdown
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
// doc handles application/pdf + office documents (local parse); img handles
// images via OCR; txt handles plain text / markdown.
func NewFileReadTool(doc, img, txt fileParser) FullTool {
	return &fileReadTool{
		documentParser: doc,
		imageParser:    img,
		textParser:     txt,
		headFn:         http.Head,
		presignFn:      util.GenerateSignedURLForMethod,
	}
}

var _ FullTool = (*fileReadTool)(nil)

func (t *fileReadTool) Name() string { return "file_read" }
func (t *fileReadTool) Description() string {
	return "Read the contents of an uploaded file by URL. Supports PDF, Word " +
		"(.docx/.doc), Excel (.xlsx/.xls), PowerPoint (.pptx/.ppt), RTF, images " +
		"(OCR), and plain text / markdown. " +
		"Input: { file_url: string, prompt?: string, offset?: integer, limit_bytes?: integer, read_token?: string }. " +
		"Returns UTF-8-safe resumable pages with next_offset, has_more, and a content read_token."
}
func (t *fileReadTool) UserFacingName() string      { return "读取文件" }
func (t *fileReadTool) NarrationVerb() string       { return "读取文件" }
func (t *fileReadTool) IsReadOnly() bool            { return true }
func (t *fileReadTool) IsSearchOrReadCommand() bool { return true }
func (t *fileReadTool) AlwaysLoad() bool            { return true }

const (
	fileReadDefaultLimitBytes = 64 * 1024
	fileReadMaxLimitBytes     = 64 * 1024
)

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
	// The captured path is percent-encoded in the URL. Readable object keys may
	// carry UTF-8 (e.g. %E6%9C%AC for a Chinese name); decode back to the raw key
	// the COS SDK expects before presigning (it re-encodes internally, so passing
	// the encoded form would double-encode → 404). PathUnescape on a pure-ASCII key
	// (no '%') is an identity, so legacy keys are unaffected; on a malformed escape
	// keep the original (best-effort).
	if decoded, err := url.PathUnescape(m[1]); err == nil {
		return decoded, true
	}
	return m[1], true
}

// isDocumentReadable reports whether a file should be routed to the local
// DocumentParser (PDF + office formats). The authoritative signal is the MIME
// type. The URL file-extension fallback applies ONLY when the MIME type is
// generic/ambiguous (application/zip for OOXML, application/octet-stream for
// legacy OLE2, or empty) — so a specific MIME like text/plain or image/* is
// never overridden by a misleading extension.
func isDocumentReadable(mimeType, fileURL string) bool {
	// Only formats parser.DocumentParser supports; legacy .xls/.ppt excluded.
	switch mimeType {
	case "application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/msword",
		"application/rtf",
		"text/rtf":
		return true
	}
	// Extension fallback only for generic/ambiguous sniff results.
	if mimeType != "" && mimeType != "application/zip" && mimeType != "application/octet-stream" {
		return false
	}
	name := path.Base(fileURL)
	if i := strings.IndexAny(name, "?#"); i != -1 {
		name = name[:i]
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf", ".docx", ".doc", ".pptx", ".xlsx", ".rtf":
		return true
	}
	return false
}

func (t *fileReadTool) returnSoftError(fileName, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(fileReadOutput{
		FileName:  fileName,
		MimeType:  "application/octet-stream",
		Content:   "ERROR: " + msg,
		ByteSize:  len(msg) + 7,
		Truncated: false,
		Error:     "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *fileReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"file_url": {"type": "string", "format": "uri", "description": "URL of the uploaded file to read (e.g. an agent attachment)."},
			"prompt":   {"type": "string", "description": "Optional instruction describing what to extract from the file."},
			"offset": {"type": "integer", "minimum": 0, "default": 0, "description": "UTF-8 byte offset in the normalized parsed text."},
			"limit_bytes": {"type": "integer", "minimum": 1, "maximum": 65536, "default": 65536},
			"read_token": {"type": "string", "description": "Return the previous page's content fingerprint when continuing."}
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
	offset := 0
	if in.Offset != nil {
		offset = *in.Offset
	}
	if offset < 0 {
		return t.returnSoftError(path.Base(in.FileURL), "offset must be non-negative")
	}
	limitBytes := fileReadDefaultLimitBytes
	if in.LimitBytes != nil {
		limitBytes = *in.LimitBytes
	}
	if limitBytes < 1 || limitBytes > fileReadMaxLimitBytes {
		return t.returnSoftError(path.Base(in.FileURL), "limit_bytes must be between 1 and %d", fileReadMaxLimitBytes)
	}
	if offset > 0 && in.ReadToken == "" {
		return t.returnSoftError(path.Base(in.FileURL), "read_token is required when offset is greater than 0")
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
	var parserTruncated bool

	switch {
	case isDocumentReadable(mimeType, in.FileURL):
		if t.documentParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "document parser not configured")
		}
		content, pageCount, parserTruncated, err = t.documentParser.Parse(ctx, fetchURL, in.Prompt)
	case strings.HasPrefix(mimeType, "image/"):
		if t.imageParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "image parser not configured")
		}
		content, _, parserTruncated, err = t.imageParser.Parse(ctx, fetchURL, in.Prompt)
	case mimeType == "text/plain" || mimeType == "text/markdown":
		if t.textParser == nil {
			return t.returnSoftError(path.Base(in.FileURL), "text parser not configured")
		}
		content, _, parserTruncated, err = t.textParser.Parse(ctx, fetchURL, in.Prompt)
	default:
		return t.returnSoftError(path.Base(in.FileURL), "unsupported MIME type %q (supported: PDF, Word/Excel/PowerPoint, images, plain text)", mimeType)
	}
	if err != nil {
		return t.returnSoftError(path.Base(in.FileURL), "parse error: %v", err)
	}
	if parserTruncated {
		return t.returnSoftError(path.Base(in.FileURL), "parser returned incomplete content; restart after the source parser is fixed")
	}

	normalizedContent := strings.ToValidUTF8(content, "\uFFFD")
	contentHash := sha256.Sum256([]byte(normalizedContent))
	readToken := fmt.Sprintf("sha256:%x", contentHash)
	if in.ReadToken != "" && in.ReadToken != readToken {
		return t.returnSoftError(path.Base(in.FileURL), "read_token does not match current file content; restart from offset 0")
	}
	contentByteSize := len(normalizedContent)
	if offset > contentByteSize {
		return t.returnSoftError(path.Base(in.FileURL), "offset %d exceeds content_byte_size %d", offset, contentByteSize)
	}
	if offset < contentByteSize && !utf8.RuneStart(normalizedContent[offset]) {
		return t.returnSoftError(path.Base(in.FileURL), "offset %d is not a UTF-8 rune boundary", offset)
	}

	end := offset + limitBytes
	if end > contentByteSize {
		end = contentByteSize
	}
	for end > offset && end < contentByteSize && !utf8.RuneStart(normalizedContent[end]) {
		end--
	}
	if end == offset && offset < contentByteSize {
		return t.returnSoftError(path.Base(in.FileURL), "limit_bytes is too small to include the next UTF-8 rune")
	}
	pageContent := normalizedContent[offset:end]
	hasMore := end < contentByteSize

	// FileName comes from the canonical URL (not the presigned one), so the
	// user sees "report.pdf" instead of a long query-string-decorated key.
	out, _ := json.Marshal(fileReadOutput{
		FileName:        path.Base(in.FileURL),
		MimeType:        mimeType,
		Content:         pageContent,
		PageCount:       pageCount,
		ByteSize:        byteSize,
		ContentByteSize: contentByteSize,
		Offset:          offset,
		ReturnedBytes:   len(pageContent),
		NextOffset:      end,
		HasMore:         hasMore,
		ReadToken:       readToken,
		Truncated:       hasMore,
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
