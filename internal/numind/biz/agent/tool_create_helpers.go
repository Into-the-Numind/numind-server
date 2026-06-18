package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"
)

// fileCreateOutput is the unified JSON response for all file-generation tools.
type fileCreateOutput struct {
	URL       string `json:"url"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	Format    string `json:"format"`
}

// maxFilenameBytes is the maximum byte length for a sanitized output filename.
const maxFilenameBytes = 200

// maxFileBytes is the maximum allowed generated file size (100 MiB).
const maxFileBytes = 100 * 1024 * 1024

// sanitizeFilenameRe matches characters that are NOT alphanumeric, dot, hyphen, or underscore.
var sanitizeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// objectKeyNameRe matches characters NOT allowed in a *readable* object-key name
// segment. Unlike sanitizeFilenameRe (ASCII-only) it KEEPS every Unicode letter and
// digit — so an AI-chosen Chinese name like "本周工作小结.docx" survives into the COS
// key instead of being mangled to "______.docx" — and replaces only separators,
// punctuation, whitespace and control chars with "_". '(' and ')' are NOT letters/
// digits, so they are still stripped: cos_resign.go's ')' link-boundary invariant holds.
var objectKeyNameRe = regexp.MustCompile(`[^\p{L}\p{N}._-]`)

// dotdotRe matches two or more consecutive dots (path traversal sequences).
var dotdotRe = regexp.MustCompile(`\.\.+`)

// sanitizeOutputFilename strips unsafe characters from a filename and truncates to
// maxFilenameBytes. Specifically:
//   - Characters not in [a-zA-Z0-9._-] are replaced with "_".
//   - Sequences of two or more dots (e.g. "..") are collapsed to a single dot.
//   - The result is truncated to maxFilenameBytes bytes.
//   - An empty result is replaced with "output".
//
// Called by tool_create_helpers.go and tool_create_png_chart.go (task 4.3).
func sanitizeOutputFilename(name string) string {
	safe := sanitizeFilenameRe.ReplaceAllString(name, "_")
	// Collapse ".." and longer dot-sequences to prevent path traversal in object keys.
	safe = dotdotRe.ReplaceAllString(safe, ".")
	// Truncate to maxFilenameBytes bytes (not runes — object keys are byte-limited).
	if len(safe) > maxFilenameBytes {
		safe = safe[:maxFilenameBytes]
	}
	if safe == "" || safe == "_" {
		return "output"
	}
	return safe
}

// sanitizeObjectKeyName produces a READABLE, path-safe object-key name segment from
// a filename, preserving Unicode letters/digits so the COS key reflects the AI's
// content-related name (user requirement: 存到 COS 必须是相关的名字). Specifically:
//   - chars outside [\p{L}\p{N}._-] (incl. "/" "\\" whitespace, control chars,
//     parens) are replaced with "_"
//   - sequences of two or more dots ("..") collapse to a single dot (anti-traversal)
//   - the result is truncated to maxFilenameBytes bytes on a UTF-8 rune boundary
//     (never splitting a multibyte char), then leading/trailing "_" / "." trimmed
//   - an empty result falls back to "output"
//
// COS object keys are UTF-8 safe and the cos-go-sdk-v5 percent-encodes the key
// consistently for both the request path and the signature, so multibyte names
// round-trip through upload → presign → download. Readers that parse the key back
// OUT of a URL string (cos_resign.go, tool_file_read.go) url.PathUnescape it first.
func sanitizeObjectKeyName(name string) string {
	safe := objectKeyNameRe.ReplaceAllString(name, "_")
	safe = dotdotRe.ReplaceAllString(safe, ".")
	safe = truncateUTF8(safe, maxFilenameBytes)
	safe = strings.Trim(safe, "_.")
	if safe == "" {
		return "output"
	}
	return safe
}

// truncateUTF8 truncates s to at most maxBytes bytes without splitting a multibyte
// rune (a naive s[:maxBytes] on a CJK string can land mid-rune → invalid UTF-8 in
// the object key). Backs off to the nearest preceding rune boundary.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// userIDFromContext extracts the userID that runner.Run injects via
// middleware.NewContextWithUserID. Returns 0 when the key is absent (e.g. in tests
// that do not wire a full runner context).
//
// Called by tool_create_helpers.go and tool_create_png_chart.go (task 4.3).
func userIDFromContext(ctx context.Context) uint {
	uid, _ := middleware.UserIDFromCtx(ctx)
	return uid
}

// uploadGeneratedFile uploads data to COS under
//
//	agent-outputs/<userID>/<yyyymmddHHMMSS>-<sanitizedFilename>
//
// and returns a marshalled fileCreateOutput as ToolResult.
//
// When COS is disabled (util.UploadBytesToCOS returns ""), the URL field is set to
// /local-uploads/<objectKey> so callers get a usable placeholder instead of an error.
//
// Called by all four tool_create_*.go files and by tool_create_png_chart.go (task 4.3).
func uploadGeneratedFile(
	ctx context.Context,
	data []byte,
	contentType string,
	filename string,
	format string,
) (ToolResult, error) {
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("generated file too large: %d bytes (limit %d)", len(data), maxFileBytes)
	}

	userID := userIDFromContext(ctx)
	// Readable key: keep the AI's Chinese/Unicode name in the COS object key (not
	// mangled to "______"). Display/download names already come from `filename` via
	// the reflected Content-Disposition below; this aligns the stored key too.
	sanitized := sanitizeObjectKeyName(filename)
	ts := time.Now().Format("20060102-150405")
	objectKey := fmt.Sprintf("agent-outputs/%d/%s-%s", userID, ts, sanitized)

	rawURL, err := util.UploadBytesToCOS(ctx, objectKey, contentType, data)
	if err != nil {
		return nil, fmt.Errorf("uploadGeneratedFile: COS upload: %w", err)
	}
	if rawURL == "" {
		// COS disabled in this environment — return a local placeholder URL.
		rawURL = fmt.Sprintf("/local-uploads/%s", objectKey)
	} else {
		// COS is enabled — use a 24-hour presigned URL per decision T4.
		// FOLLOW-UP: this URL is now embedded as markdown in the PERSISTED final
		// answer (durable-render fix), so after 24h a reopened session shows a
		// broken image. Proper fix: store the COS object key and presign lazily on
		// read (getSessionSnapshot/transformMessages). Tracked as a follow-up.
		const presignExpiry = 24 * 60 * 60 // 86400 seconds
		var signed string
		var signErr error
		if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "text/html") {
			// Images render inline (<img>); HTML renders inside the artifact card's
			// sandboxed iframe (问题五) — both need a PLAIN signed URL.
			// GenerateSignedDownloadURL forces Content-Disposition: attachment, which
			// makes the browser DOWNLOAD instead of displaying — breaking inline image
			// render AND HTML iframe preview (User-reported: image dev 2026-06-08,
			// HTML 问题五 dev 2026-06-18).
			signed, signErr = util.GenerateSignedURL(ctx, objectKey, presignExpiry)
		} else {
			// Non-image artifacts are downloads; the attachment disposition keeps
			// Chrome from flagging the cross-site download as "不安全".
			signed, signErr = util.GenerateSignedDownloadURL(ctx, objectKey, filename, presignExpiry)
		}
		if signErr == nil && signed != "" {
			rawURL = signed
		}
		// On sign error fall through to the public URL returned by UploadBytesToCOS.
	}

	out := fileCreateOutput{
		URL:       rawURL,
		Filename:  filename,
		SizeBytes: int64(len(data)),
		Format:    format,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("uploadGeneratedFile: marshal output: %w", err)
	}
	return b, nil
}

// toolArtifact is one generated-file artifact parsed out of a tool's JSON result.
type toolArtifact struct {
	URL      string
	Filename string
	Mime     string
}

// artifactsFromToolResult inspects a tool's JSON result for generated-file
// artifacts and returns ALL of them. It supports two shapes:
//
//   - fileCreateOutput single file: {"url":...,"filename":...,"format":...} —
//     emitted by image_gen / create_html / create_csv / create_* tools.
//   - runPythonOutput multi-file: {"files":[{"filename":...,"url":...},...]} —
//     emitted by run_python (the docx/pptx/xlsx/html path via load_skill). Each
//     file's mime is inferred from its filename (no format hint in this shape).
//
// It returns an empty slice when the output is not JSON or contains no file with a
// non-empty url, so non-file tools are unaffected. Bug-from-Customer (问题4): the
// old single-file-only parser parsed run_python's url as empty → the generated
// docx/html were never collected → their inline links (incl. markdown table rows)
// were neither stripped nor lifted into standalone file cards.
func artifactsFromToolResult(output string) []toolArtifact {
	trimmed := strings.TrimSpace(output)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	// Multi-file shape first: run_python emits {"files":[...]}. We detect it by a
	// non-empty files[] with at least one usable url; otherwise fall through to the
	// single-file shape (a create_* output has no "files" key, so rp.Files is nil).
	var rp runPythonOutput
	if err := json.Unmarshal([]byte(trimmed), &rp); err == nil && len(rp.Files) > 0 {
		var arts []toolArtifact
		for _, f := range rp.Files {
			if f.URL == "" {
				continue
			}
			arts = append(arts, toolArtifact{
				URL:      f.URL,
				Filename: f.Filename,
				Mime:     mimeFromArtifact(f.Filename, ""),
			})
		}
		if len(arts) > 0 {
			return arts
		}
	}

	// Single-file shape: fileCreateOutput {"url":...,"filename":...,"format":...}.
	var fc fileCreateOutput
	if err := json.Unmarshal([]byte(trimmed), &fc); err == nil && fc.URL != "" {
		return []toolArtifact{{
			URL:      fc.URL,
			Filename: fc.Filename,
			Mime:     mimeFromArtifact(fc.Filename, fc.Format),
		}}
	}
	return nil
}

// artifactFromToolResult returns the FIRST generated-file artifact from a tool's
// JSON result (or empty strings when there is none). Retained for the SSE
// tool_call_result emitters (runner_stream.go), which deliver a single artifact via
// ToolCallResultPayload.ArtifactURL — without it the frontend never receives the
// generated file and the user sees "图片已生成" with no image. For collecting ALL
// files (run_python multi-file), callers use artifactsFromToolResult instead.
func artifactFromToolResult(output string) (url, filename, mime string) {
	arts := artifactsFromToolResult(output)
	if len(arts) == 0 {
		return "", "", ""
	}
	return arts[0].URL, arts[0].Filename, arts[0].Mime
}

// mimeFromArtifact derives a MIME type from a filename extension (preferred) or
// the format hint. Lets the frontend decide whether an artifact is an inline
// image. Returns application/octet-stream when unknown.
func mimeFromArtifact(filename, format string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ext == "" {
		ext = strings.ToLower(format)
	}
	switch ext {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "pdf":
		return "application/pdf"
	case "html", "htm":
		return "text/html"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "txt", "md", "text":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}
