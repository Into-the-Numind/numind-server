package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

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
	sanitized := sanitizeOutputFilename(filename)
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
		const presignExpiry = 24 * 60 * 60 // 86400 seconds
		signed, signErr := util.GenerateSignedURL(ctx, objectKey, presignExpiry)
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
