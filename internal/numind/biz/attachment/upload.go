// Package attachment provides agent attachment upload functionality.
// Files are validated for MIME type and size, then uploaded to Tencent COS
// via the existing util.UploadBytesToCOS helper.
package attachment

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/pkg/util"
)

// MaxUploadSize is the maximum allowed file size (20 MiB).
const MaxUploadSize = 20 * 1024 * 1024

// allowedMIMEPrefixes is the list of permitted MIME type prefixes / exact values.
var allowedMIMEPrefixes = []string{
	"image/",
	"application/pdf",
	"text/plain",
	"text/markdown",
}

// UploadService handles agent attachment uploads to COS.
type UploadService struct{}

// NewUploadService constructs an UploadService.
// The service relies on the package-level util.UploadBytesToCOS singleton
// (configured via viper cos.* keys), so no explicit COS client is required
// at construction time.
func NewUploadService() *UploadService {
	return &UploadService{}
}

// UploadResult is returned from Upload on success.
type UploadResult struct {
	URL       string    `json:"url"`
	Size      int64     `json:"size"`
	MimeType  string    `json:"mime_type"`
	Filename  string    `json:"filename"`
	CreatedAt time.Time `json:"created_at"`
}

// Upload reads the multipart file, validates size and MIME type, uploads to
// COS under agent-attachments/<userID>/<timestamp>-<filename>, and returns
// the public URL.
//
// Validation errors return a descriptive error whose message is safe to relay
// to the caller (no internal details).
func (s *UploadService) Upload(ctx context.Context, userID uint, file multipart.File, hdr *multipart.FileHeader) (*UploadResult, error) {
	if hdr == nil {
		return nil, fmt.Errorf("attachment.Upload: file header is nil")
	}

	// Size check: read the entire file but reject immediately if too large.
	if hdr.Size > MaxUploadSize {
		return nil, fmt.Errorf("attachment.Upload: file too large (%d bytes, max %d)", hdr.Size, MaxUploadSize)
	}

	// Read file bytes.
	data, err := io.ReadAll(io.LimitReader(file, MaxUploadSize+1))
	if err != nil {
		return nil, fmt.Errorf("attachment.Upload: read file: %w", err)
	}
	if int64(len(data)) > MaxUploadSize {
		return nil, fmt.Errorf("attachment.Upload: file too large (max 20MB)")
	}

	// Detect MIME type from content (not trusting Content-Type header from client).
	mimeType := http.DetectContentType(data)
	if !isMIMEAllowed(mimeType) {
		// Also allow based on file extension for common text types that DetectContentType
		// may return as "application/octet-stream" (e.g., .md, .txt files).
		ext := strings.ToLower(filepath.Ext(hdr.Filename))
		if ext == ".md" || ext == ".txt" {
			mimeType = "text/plain"
		} else if ext == ".pdf" {
			mimeType = "application/pdf"
		} else {
			return nil, fmt.Errorf("attachment.Upload: unsupported file type '%s' (allowed: images, pdf, plain text)", mimeType)
		}
	}

	// Build COS object key: agent-attachments/<userID>/<unixnano>-<filename>
	ts := time.Now()
	safeFilename := sanitizeFilename(hdr.Filename)
	objectKey := fmt.Sprintf("agent-attachments/%d/%d-%s", userID, ts.UnixNano(), safeFilename)

	url, err := util.UploadBytesToCOS(ctx, objectKey, mimeType, data)
	if err != nil {
		return nil, fmt.Errorf("attachment.Upload: COS upload: %w", err)
	}

	// When COS is disabled (local/test env), UploadBytesToCOS returns "".
	// Return a synthetic local URL so the caller still has something to work with.
	if url == "" {
		url = fmt.Sprintf("/local-uploads/%s", objectKey)
	}

	return &UploadResult{
		URL:       url,
		Size:      int64(len(data)),
		MimeType:  mimeType,
		Filename:  hdr.Filename,
		CreatedAt: ts,
	}, nil
}

// isMIMEAllowed returns true if the detected MIME type starts with one of the
// allowed prefixes or matches exactly.
func isMIMEAllowed(mimeType string) bool {
	// Strip parameters (e.g. "text/plain; charset=utf-8" → "text/plain").
	if idx := strings.Index(mimeType, ";"); idx != -1 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	for _, prefix := range allowedMIMEPrefixes {
		if strings.HasPrefix(mimeType, prefix) {
			return true
		}
	}
	return false
}

// sanitizeFilename removes path separators and leading dots from a filename
// to prevent directory traversal in COS object keys.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "..", "")
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "file"
	}
	// Replace spaces with underscores.
	name = strings.ReplaceAll(name, " ", "_")
	return name
}
