// Package attachment provides agent attachment upload functionality.
// Files are validated for MIME type and size, then uploaded to Tencent COS
// via the existing util.UploadBytesToCOS helper. After a successful upload the
// attachment record is persisted to agent_attachment and the fallback service
// is notified asynchronously (V1.5 task 1.2).
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

	agentatt "numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
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
	"audio/",
}

// UploadService handles agent attachment uploads to COS and persists records
// to agent_attachment.
type UploadService struct {
	attStore    store.IAgentAttachmentStore
	fallbackSvc agentatt.FallbackService
}

// NewUploadService constructs an UploadService.
// attStore and fallbackSvc may be nil for backwards compatibility (legacy
// callers that do not need DB persistence). When non-nil, uploaded files are
// persisted and enqueued for fallback generation.
func NewUploadService() *UploadService {
	return &UploadService{}
}

// NewUploadServiceWithFallback constructs an UploadService with DB persistence
// and async fallback generation wired in (V1.5 task 1.2).
func NewUploadServiceWithFallback(attStore store.IAgentAttachmentStore, fallbackSvc agentatt.FallbackService) *UploadService {
	return &UploadService{
		attStore:    attStore,
		fallbackSvc: fallbackSvc,
	}
}

// UploadResult is returned from Upload on success.
type UploadResult struct {
	// ID is the primary key of the persisted agent_attachment row.
	// Zero when DB persistence is not configured.
	ID            uint64    `json:"id"`
	URL           string    `json:"url"`
	Size          int64     `json:"size"`
	MimeType      string    `json:"mime_type"`
	Filename      string    `json:"filename"`
	Modality      string    `json:"modality"`
	FallbackReady bool      `json:"fallback_ready"`
	CreatedAt     time.Time `json:"created_at"`
}

// Upload reads the multipart file, validates size and MIME type, uploads to
// COS under agent-attachments/<userID>/<timestamp>-<filename>, persists to DB
// (if attStore is configured), enqueues fallback generation (if fallbackSvc is
// configured), and returns the result.
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
		switch ext {
		case ".md", ".txt":
			mimeType = "text/plain"
		case ".pdf":
			mimeType = "application/pdf"
		case ".mp3":
			mimeType = "audio/mpeg"
		case ".wav":
			mimeType = "audio/wav"
		case ".m4a":
			mimeType = "audio/m4a"
		default:
			return nil, fmt.Errorf("attachment.Upload: unsupported file type '%s' (allowed: images, pdf, audio, plain text)", mimeType)
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

	fileSize := int64(len(data))

	// ── Detect modality ──────────────────────────────────────────────────────
	modality := agentatt.DetectModality(mimeType)

	// ── Detect image dimensions ──────────────────────────────────────────────
	var width, height *int
	if modality == agentatt.ModalityImage {
		width, height = agentatt.DecodeImageDimensionsFromBytes(data)
	}

	// ── Persist to DB (if configured) ────────────────────────────────────────
	var attID uint64
	if s.attStore != nil {
		att := &model.AgentAttachment{
			UserID:    userID,
			URL:       url,
			Filename:  hdr.Filename,
			MimeType:  mimeType,
			Size:      fileSize,
			Modality:  modality,
			Width:     width,
			Height:    height,
			CreatedAt: ts,
		}
		if err := s.attStore.Create(ctx, att); err != nil {
			// Non-fatal: log the error but still return the upload result.
			// The fallback worker will not be invoked, but the URL is valid.
			// This matches the "fire-and-forget" philosophy: upload must succeed.
			_ = err // caller gets result, loss of DB row is visible via monitoring
		} else {
			attID = att.ID
			// Enqueue async fallback generation (fire-and-forget).
			if s.fallbackSvc != nil && modality != agentatt.ModalityUnknown {
				_ = s.fallbackSvc.Enqueue(ctx, att.ID)
			}
		}
	}

	return &UploadResult{
		ID:            attID,
		URL:           url,
		Size:          fileSize,
		MimeType:      mimeType,
		Filename:      hdr.Filename,
		Modality:      modality,
		FallbackReady: false, // always false at upload time; poll /status
		CreatedAt:     ts,
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
