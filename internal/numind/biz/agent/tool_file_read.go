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

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
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
}

// NewFileReadTool constructs a fileReadTool with injected parsers.
// T7 (runner wiring) calls this with the production implementations.
func NewFileReadTool(pdf, img, txt fileParser) *fileReadTool {
	return &fileReadTool{
		pdfParser:   pdf,
		imageParser: img,
		textParser:  txt,
		headFn:      http.Head,
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

// attachmentPathRE matches /agent-attachments/<userID>/ in a URL path.
var attachmentPathRE = regexp.MustCompile(`/agent-attachments/(\d+)/`)

// Execute reads the file at the given URL, verifies ownership, detects MIME type,
// dispatches to the appropriate parser, and returns structured JSON output.
func (t *fileReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in fileReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, errno.ErrBind.SetMessage("file_read: invalid input JSON: %s", err.Error())
	}
	if in.FileURL == "" {
		return nil, errno.ErrInvalidParameter.SetMessage("file_read: file_url is required")
	}

	// Verify the file belongs to the requesting user.
	ctxUserID, ok := middleware.UserIDFromCtx(ctx)
	if !ok || ctxUserID == 0 {
		return nil, errno.ErrPermissionDenied.SetMessage("file_read: user not authenticated")
	}
	urlUserID, err := extractUserIDFromURL(in.FileURL)
	if err != nil {
		return nil, errno.ErrInvalidParameter.SetMessage("file_read: %s", err.Error())
	}
	if ctxUserID != urlUserID {
		return nil, errno.ErrPermissionDenied.SetMessage("file_read: file not owned by current user")
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

	// HEAD the file to detect content type and byte size without downloading.
	headFn := t.headFn
	if headFn == nil {
		headFn = http.Head
	}
	headResp, err := headFn(in.FileURL)
	if err != nil {
		return nil, errno.ErrAIProviderError.SetMessage("file_read: HEAD request failed: %s", err.Error())
	}
	if headResp.StatusCode >= 400 {
		return nil, errno.ErrAIProviderError.SetMessage("file_read: HEAD returned HTTP %d", headResp.StatusCode)
	}

	mimeType := headResp.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	byteSize := int(headResp.ContentLength)

	// Dispatch to the appropriate parser by MIME type.
	var content string
	var pageCount int
	var truncated bool

	switch {
	case mimeType == "application/pdf":
		if t.pdfParser == nil {
			return nil, errno.ErrAIProviderError.SetMessage("file_read: PDF parser not configured")
		}
		content, pageCount, truncated, err = t.pdfParser.Parse(ctx, in.FileURL, in.Prompt)
	case strings.HasPrefix(mimeType, "image/"):
		if t.imageParser == nil {
			return nil, errno.ErrAIProviderError.SetMessage("file_read: image parser not configured")
		}
		content, _, truncated, err = t.imageParser.Parse(ctx, in.FileURL, in.Prompt)
	case mimeType == "text/plain" || mimeType == "text/markdown":
		if t.textParser == nil {
			return nil, errno.ErrAIProviderError.SetMessage("file_read: text parser not configured")
		}
		content, _, truncated, err = t.textParser.Parse(ctx, in.FileURL, in.Prompt)
	default:
		return nil, errno.ErrInvalidParameter.SetMessage(
			"file_read: unsupported MIME type %q (supported: application/pdf, image/*, text/plain, text/markdown)",
			mimeType,
		)
	}
	if err != nil {
		return nil, errno.ErrAIProviderError.SetMessage("file_read: parse error: %s", err.Error())
	}

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
// /agent-attachments/<userID>/...
func extractUserIDFromURL(fileURL string) (uint, error) {
	m := attachmentPathRE.FindStringSubmatch(fileURL)
	if len(m) < 2 {
		return 0, fmt.Errorf("URL does not match agent-attachments path format")
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid user ID in URL: %w", err)
	}
	return uint(id), nil
}
