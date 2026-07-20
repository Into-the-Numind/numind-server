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
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// fileReadInput is the JSON input for the file_read tool.
type fileReadInput struct {
	AttachmentID uint64 `json:"attachment_id,omitempty"`
	FileURL      string `json:"file_url"`
	Prompt       string `json:"prompt,omitempty"`
	Offset       *int   `json:"offset,omitempty"`
	LimitBytes   *int   `json:"limit_bytes,omitempty"`
	ReadToken    string `json:"read_token,omitempty"`
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
	documentParser  fileParser // application/pdf + office docs (docx/doc/pptx/xlsx/rtf), local parse
	imageParser     fileParser // image/*  (OCR)
	textParser      fileParser // text/plain, text/markdown
	attachmentStore store.IAgentAttachmentStore
	// headFn is the HTTP HEAD function; swapped in tests to avoid network calls.
	headFn func(url string) (*http.Response, error)
	// headRequestFn is the production HEAD path. It carries the run context,
	// enforces a timeout, and refuses redirects so a managed COS URL cannot
	// redirect the server into a private network. Tests may keep using headFn.
	headRequestFn func(context.Context, string) (*http.Response, error)
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
	return NewFileReadToolWithStore(doc, img, txt, nil)
}

// NewFileReadToolWithStore constructs the production file_read. Managed
// uploads use attStore as the canonical parsed-content cache; nil preserves the
// legacy URL parsing path for isolated tests and compatibility callers.
func NewFileReadToolWithStore(doc, img, txt fileParser, attStore store.IAgentAttachmentStore) FullTool {
	return &fileReadTool{
		documentParser:  doc,
		imageParser:     img,
		textParser:      txt,
		attachmentStore: attStore,
		headRequestFn:   managedFileHEAD,
		presignFn:       util.GenerateSignedURLForMethod,
	}
}

var _ FullTool = (*fileReadTool)(nil)

func (t *fileReadTool) Name() string { return "file_read" }
func (t *fileReadTool) Description() string {
	return "Read the contents of an uploaded file by attachment ID (preferred) or URL. Supports PDF, Word " +
		"(.docx/.doc), Excel (.xlsx/.xls), PowerPoint (.pptx/.ppt), RTF, images " +
		"(OCR), and plain text / markdown. " +
		"Input: { attachment_id?: integer, file_url?: string, prompt?: string, offset?: integer, limit_bytes?: integer, read_token?: string }. " +
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
// Non-COS URLs do not match. Production (presignFn configured) rejects them;
// tests may set presignFn=nil to exercise local httptest fixtures.
var cosHostnameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.cos\.[a-z0-9-]+\.myqcloud\.com$`)

// extractCOSObjectKey returns the COS object key for a COS bucket URL and a
// boolean indicating whether the URL was recognized as COS. The recognized
// form matches cosURLPathRE — bucket.cos.region.myqcloud.com hosts.
func extractCOSObjectKey(fileURL string) (string, bool) {
	u, err := url.ParseRequestURI(fileURL)
	if err != nil || u.Scheme != "https" || u.User != nil || !cosHostnameRE.MatchString(strings.ToLower(u.Hostname())) {
		return "", false
	}
	objectKey := strings.TrimPrefix(u.Path, "/")
	if objectKey == "" || path.Clean("/"+objectKey) != "/"+objectKey {
		return "", false
	}
	return objectKey, true
}

func managedFileHEAD(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build HEAD request: %w", err)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client.Do(req)
}

func safeFileName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return ""
	}
	return name
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
			"attachment_id": {"type": "integer", "minimum": 1, "description": "Current user's uploaded attachment ID. Prefer this over file_url when available."},
			"file_url": {"type": "string", "format": "uri", "description": "URL of the uploaded file to read (e.g. an agent attachment)."},
			"prompt":   {"type": "string", "description": "Optional instruction describing what to extract from the file."},
			"offset": {"type": "integer", "minimum": 0, "default": 0, "description": "UTF-8 byte offset in the normalized parsed text."},
			"limit_bytes": {"type": "integer", "minimum": 1, "maximum": 65536, "default": 65536},
			"read_token": {"type": "string", "description": "Return the previous page's content fingerprint when continuing."}
		}
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
	// Open the safe span before validation/network preflight so rejected SSRF,
	// ownership, signing, and HEAD attempts remain observable. Raw input is never
	// attached; only bounded scalar metadata is added to the terminal output.
	span := startSafePipelineToolSpan(ctx, "tool.file_read.execute", map[string]any{})
	spanOutput := map[string]any{"returned_bytes": 0, "has_more": false}
	spanErrorClass := "preflight_error"
	defer func() { span.End(spanOutput, spanErrorClass) }()

	var in fileReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return t.returnSoftError("", "invalid_input_json")
	}
	if in.AttachmentID == 0 && in.FileURL == "" {
		return t.returnSoftError("", "attachment_reference_required")
	}
	fileName := safeFileName(in.FileURL)
	offset := 0
	if in.Offset != nil {
		offset = *in.Offset
	}
	if offset < 0 {
		return t.returnSoftError(fileName, "invalid_offset")
	}
	limitBytes := fileReadDefaultLimitBytes
	if in.LimitBytes != nil {
		limitBytes = *in.LimitBytes
	}
	if limitBytes < 1 || limitBytes > fileReadMaxLimitBytes {
		return t.returnSoftError(fileName, "invalid_limit")
	}
	if offset > 0 && in.ReadToken == "" {
		return t.returnSoftError(fileName, "read_token_required")
	}

	// Verify the file belongs to the requesting user.
	ctxUserID, ok := middleware.UserIDFromCtx(ctx)
	if !ok || ctxUserID == 0 {
		return t.returnSoftError(fileName, "unauthenticated")
	}

	// Preferred managed-upload path: resolve an opaque attachment ID with a
	// user-scoped query and page the persisted canonical content. No URL HEAD,
	// presign, download, OCR, or DocumentParser call occurs on this path.
	if in.AttachmentID > 0 {
		if t.attachmentStore == nil {
			return t.returnSoftError(fileName, "attachment_store_unavailable")
		}
		managed, getErr := t.attachmentStore.GetByIDAndUser(ctx, in.AttachmentID, ctxUserID)
		if getErr != nil {
			return t.returnSoftError(fileName, "file_not_owned_or_missing")
		}
		return t.readCachedAttachment(ctx, managed, in, spanOutput, &spanErrorClass)
	}

	urlUserID, err := extractUserIDFromURL(in.FileURL)
	if err != nil {
		return t.returnSoftError(fileName, "unmanaged_file_url")
	}
	if ctxUserID != urlUserID {
		return t.returnSoftError(fileName, "file_not_owned")
	}

	// Rolling compatibility: old clients send only the upload URL. Resolve it
	// to the same user-scoped row before considering the legacy network parser.
	// A lookup miss is expected for agent-outputs and pre-DB objects.
	if t.attachmentStore != nil {
		if managed, getErr := t.attachmentStore.GetByURLAndUser(ctx, in.FileURL, ctxUserID); getErr == nil {
			return t.readCachedAttachment(ctx, managed, in, spanOutput, &spanErrorClass)
		}
	}

	// Translate private COS URLs into time-bounded presigned URLs. TWO signed
	// URLs are minted: one for HEAD (the cheap probe used here) and one for
	// GET (downstream fetch by qwen-long, OCR, text http.Get). Tencent COS
	// signing is METHOD-BOUND — a GET URL hit with HEAD returns 403
	// "SignatureDoesNotMatch". The customer-facing canonical URL (in.FileURL)
	// is preserved for filename / output. Production rejects non-COS URLs so
	// an attachment-looking path on an attacker-controlled host cannot SSRF.
	headURL := in.FileURL
	fetchURL := in.FileURL // for parsers (GET-style downstream fetch)
	objectKey, isManagedCOS := extractCOSObjectKey(in.FileURL)
	if t.presignFn != nil && !isManagedCOS {
		return t.returnSoftError(fileName, "unmanaged_file_url")
	}
	if isManagedCOS && t.presignFn != nil {
		// 1h validity is comfortably longer than any single tool call.
		signedHead, signErr := t.presignFn(ctx, http.MethodHead, objectKey, 3600)
		if signErr != nil {
			return t.returnSoftError(fileName, "presign_failed")
		}
		signedGet, signErr := t.presignFn(ctx, http.MethodGet, objectKey, 3600)
		if signErr != nil {
			return t.returnSoftError(fileName, "presign_failed")
		}
		headURL = signedHead
		fetchURL = signedGet
	}

	// HEAD the file to detect content type and byte size without downloading.
	var headResp *http.Response
	if t.headRequestFn != nil {
		headResp, err = t.headRequestFn(ctx, headURL)
	} else if t.headFn != nil {
		headResp, err = t.headFn(headURL)
	} else {
		headResp, err = managedFileHEAD(ctx, headURL)
	}
	if err != nil {
		return t.returnSoftError(fileName, "head_failed")
	}
	defer headResp.Body.Close()
	if headResp.StatusCode < 200 || headResp.StatusCode >= 300 {
		return t.returnSoftError(fileName, "head_http_error")
	}

	mimeType := headResp.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	byteSize := int(headResp.ContentLength)
	spanOutput["mime_type"] = mimeType
	spanOutput["offset"] = offset
	spanOutput["limit_bytes"] = limitBytes
	spanErrorClass = pipelineToolTraceNoError

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
			spanErrorClass = "configuration_error"
			return t.returnSoftError(fileName, "parser_not_configured")
		}
		content, pageCount, parserTruncated, err = t.documentParser.Parse(ctx, fetchURL, in.Prompt)
	case strings.HasPrefix(mimeType, "image/"):
		if t.imageParser == nil {
			spanErrorClass = "configuration_error"
			return t.returnSoftError(fileName, "parser_not_configured")
		}
		content, _, parserTruncated, err = t.imageParser.Parse(ctx, fetchURL, in.Prompt)
	case mimeType == "text/plain" || mimeType == "text/markdown":
		if t.textParser == nil {
			spanErrorClass = "configuration_error"
			return t.returnSoftError(fileName, "parser_not_configured")
		}
		content, _, parserTruncated, err = t.textParser.Parse(ctx, fetchURL, in.Prompt)
	default:
		spanErrorClass = "unsupported_mime"
		return t.returnSoftError(fileName, "unsupported_mime")
	}
	if err != nil {
		spanErrorClass = "parse_error"
		return t.returnSoftError(fileName, "parse_failed")
	}
	if parserTruncated {
		spanErrorClass = "incomplete_parser_output"
		return t.returnSoftError(fileName, "incomplete_parser_output")
	}

	return t.paginateContent(fileName, mimeType, byteSize, pageCount, content, in, spanOutput, &spanErrorClass)
}

func (t *fileReadTool) readCachedAttachment(
	ctx context.Context,
	att *model.AgentAttachment,
	in fileReadInput,
	spanOutput map[string]any,
	spanErrorClass *string,
) (ToolResult, error) {
	fileName := att.Filename
	if fileName == "" {
		fileName = safeFileName(att.URL)
	}
	spanOutput["cache_hit"] = true
	spanOutput["attachment_id"] = att.ID
	spanOutput["mime_type"] = att.MimeType

	if !att.FallbackReady {
		*spanErrorClass = "file_processing"
		return t.returnSoftError(fileName, "file_processing")
	}
	if att.FallbackError != nil && *att.FallbackError != "" {
		*spanErrorClass = "parse_error"
		return t.returnSoftError(fileName, "parse_failed")
	}

	var content string
	if att.ParsedContent != nil {
		content = *att.ParsedContent
	} else if att.TextFallback != nil {
		// Rolling compatibility for a successfully processed row created before
		// the parsed_content migration. Persist the already-produced text once;
		// never download or parse the source again.
		content = *att.TextFallback
		normalized := strings.ToValidUTF8(content, "\uFFFD")
		sum := sha256.Sum256([]byte(normalized))
		now := time.Now()
		if err := t.attachmentStore.UpdateFallback(ctx, att.ID, map[string]interface{}{
			"parsed_content":           normalized,
			"parsed_content_sha256":    fmt.Sprintf("sha256:%x", sum),
			"parsed_content_byte_size": int64(len(normalized)),
			"parsed_at":                now,
		}); err != nil {
			*spanErrorClass = "cache_persist_error"
			return t.returnSoftError(fileName, "cache_persist_failed")
		}
	} else {
		*spanErrorClass = "parse_error"
		return t.returnSoftError(fileName, "parse_failed")
	}

	*spanErrorClass = pipelineToolTraceNoError
	spanOutput["offset"] = valueOrZero(in.Offset)
	spanOutput["limit_bytes"] = valueOrDefault(in.LimitBytes, fileReadDefaultLimitBytes)
	return t.paginateContent(fileName, att.MimeType, int(att.Size), att.ParsedPageCount, content, in, spanOutput, spanErrorClass)
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func (t *fileReadTool) paginateContent(
	fileName, mimeType string,
	byteSize, pageCount int,
	content string,
	in fileReadInput,
	spanOutput map[string]any,
	spanErrorClass *string,
) (ToolResult, error) {
	offset := valueOrZero(in.Offset)
	limitBytes := valueOrDefault(in.LimitBytes, fileReadDefaultLimitBytes)
	normalizedContent := strings.ToValidUTF8(content, "\uFFFD")
	contentHash := sha256.Sum256([]byte(normalizedContent))
	readToken := fmt.Sprintf("sha256:%x", contentHash)
	if in.ReadToken != "" && in.ReadToken != readToken {
		*spanErrorClass = "stale_read_token"
		return t.returnSoftError(fileName, "stale_read_token")
	}
	contentByteSize := len(normalizedContent)
	if offset > contentByteSize {
		*spanErrorClass = "offset_out_of_range"
		return t.returnSoftError(fileName, "offset_out_of_range")
	}
	if offset < contentByteSize && !utf8.RuneStart(normalizedContent[offset]) {
		*spanErrorClass = "invalid_utf8_boundary"
		return t.returnSoftError(fileName, "invalid_utf8_boundary")
	}

	end := offset + limitBytes
	if end > contentByteSize {
		end = contentByteSize
	}
	for end > offset && end < contentByteSize && !utf8.RuneStart(normalizedContent[end]) {
		end--
	}
	if end == offset && offset < contentByteSize {
		*spanErrorClass = "limit_too_small"
		return t.returnSoftError(fileName, "limit_too_small")
	}
	pageContent := normalizedContent[offset:end]
	hasMore := end < contentByteSize

	out, _ := json.Marshal(fileReadOutput{
		FileName:        fileName,
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
	spanOutput["returned_bytes"] = len(pageContent)
	spanOutput["has_more"] = hasMore
	return ToolResult(out), nil
}

// extractUserIDFromURL extracts the user ID from a URL path of the form
// /agent-attachments/<userID>/... or /agent-outputs/<userID>/...
func extractUserIDFromURL(fileURL string) (uint, error) {
	u, err := url.Parse(fileURL)
	if err != nil {
		return 0, fmt.Errorf("invalid file URL")
	}
	m := attachmentPathRE.FindStringSubmatch(u.Path)
	if len(m) < 2 {
		return 0, fmt.Errorf("file URL is not a managed agent object")
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid user ID in URL: %w", err)
	}
	return uint(id), nil
}
