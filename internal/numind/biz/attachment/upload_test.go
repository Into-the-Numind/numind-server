package attachment

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
)

// readSeekCloser wraps a *bytes.Reader to implement multipart.File (adds Close).
type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

// testFile builds a *multipart.FileHeader-like structure for testing.
// Returns a multipart.File and a synthetic FileHeader.
func testFile(content []byte, filename string) (multipart.File, *multipart.FileHeader) {
	r := readSeekCloser{bytes.NewReader(content)}
	// Build a minimal FileHeader.
	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	mh.Set("Content-Type", "application/octet-stream")
	hdr := &multipart.FileHeader{
		Filename: filename,
		Header:   mh,
		Size:     int64(len(content)),
	}
	// Satisfy the interface: multipart.File = io.Reader + io.ReaderAt + io.Seeker + io.Closer.
	var f multipart.File = r
	return f, hdr
}

// Ensure io package is used (io.NopCloser exists; imported for clarity).
var _ = io.NopCloser

// TestUploadService_HappyPath_Image verifies that a small PNG (magic bytes) is accepted.
func TestUploadService_HappyPath_Image(t *testing.T) {
	// Minimal 1×1 PNG magic header.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52}
	file, hdr := testFile(pngHeader, "test.png")

	svc := NewUploadService()
	result, err := svc.Upload(context.Background(), 1, file, hdr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.URL == "" {
		t.Error("expected non-empty URL")
	}
	if !strings.HasPrefix(result.MimeType, "image/") {
		t.Errorf("expected image MIME type, got %s", result.MimeType)
	}
}

// TestUploadService_HappyPath_TextFile verifies that a .txt file is accepted.
func TestUploadService_HappyPath_TextFile(t *testing.T) {
	content := []byte("Hello, this is a plain text file.\n")
	file, hdr := testFile(content, "notes.txt")

	svc := NewUploadService()
	result, err := svc.Upload(context.Background(), 2, file, hdr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Filename != "notes.txt" {
		t.Errorf("expected filename 'notes.txt', got '%s'", result.Filename)
	}
}

// TestUploadService_HappyPath_MarkdownFile verifies that a .md file is accepted.
func TestUploadService_HappyPath_MarkdownFile(t *testing.T) {
	content := []byte("# Title\n\nsome markdown content")
	file, hdr := testFile(content, "readme.md")

	svc := NewUploadService()
	result, err := svc.Upload(context.Background(), 3, file, hdr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), result.Size)
	}
}

// TestUploadService_Oversize_422 verifies that files exceeding 20MB are rejected.
func TestUploadService_Oversize_422(t *testing.T) {
	oversizeContent := make([]byte, MaxUploadSize+1)
	file, hdr := testFile(oversizeContent, "big.bin")

	svc := NewUploadService()
	_, err := svc.Upload(context.Background(), 1, file, hdr)
	if err == nil {
		t.Fatal("expected error for oversize file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

// TestUploadService_BadMIME_422 verifies that disallowed MIME types (e.g., .exe) are rejected.
func TestUploadService_BadMIME_422(t *testing.T) {
	// Use MZ header (Windows PE) which http.DetectContentType returns as application/octet-stream.
	content := []byte{0x4D, 0x5A, 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	file, hdr := testFile(content, "malware.exe")

	svc := NewUploadService()
	_, err := svc.Upload(context.Background(), 1, file, hdr)
	if err == nil {
		t.Fatal("expected error for disallowed MIME type")
	}
	if !strings.Contains(err.Error(), "不支持的文件类型") {
		t.Errorf("expected friendly unsupported-type error, got: %v", err)
	}
}

// TestSanitizeFilename verifies path traversal prevention.
func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"../../../etc/passwd", "passwd"}, // filepath.Base extracts last component
		{"normal.txt", "normal.txt"},
		{".hidden", "hidden"},
		{"file with spaces.pdf", "file_with_spaces.pdf"},
		{"", "file"},
	}
	for _, tc := range cases {
		got := sanitizeFilename(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// TestUploadService_AcceptsDocx reproduces the User-reported bug (dev 2026-06-08):
// uploading a .docx attachment in agent mode was rejected with HTTP 500
// "unsupported file type 'application/zip'". docx is a zip container, so
// http.DetectContentType returns "application/zip" which is not in the whitelist,
// and the extension fallback switch had no .docx branch. Pre-fix this FAILS at
// Upload (returns an error); post-fix the office MIME is recovered from the
// extension and the upload is accepted as a "document" modality.
func TestUploadService_AcceptsDocx(t *testing.T) {
	// "PK\x03\x04" is the zip local-file-header signature → DetectContentType
	// returns "application/zip" (exactly what a real .docx sniffs as).
	zipBytes := append([]byte("PK\x03\x04"), make([]byte, 64)...)
	file, hdr := testFile(zipBytes, "会议纪要.docx")

	svc := NewUploadService()
	result, err := svc.Upload(context.Background(), 1, file, hdr)
	if err != nil {
		t.Fatalf("docx upload must be accepted, got error: %v", err)
	}
	if !strings.Contains(result.MimeType, "wordprocessingml") {
		t.Errorf("expected docx office MIME, got %q", result.MimeType)
	}
	if result.Modality != "document" {
		t.Errorf("expected modality 'document', got %q", result.Modality)
	}
}

// TestIsMIMEAllowed tests the MIME whitelist.
func TestIsMIMEAllowed(t *testing.T) {
	allowed := []string{
		"image/png",
		"image/jpeg",
		"image/gif",
		"application/pdf",
		"text/plain",
		"text/plain; charset=utf-8",
		"text/markdown",
	}
	for _, m := range allowed {
		if !isMIMEAllowed(m) {
			t.Errorf("expected %q to be allowed", m)
		}
	}

	disallowed := []string{
		"application/octet-stream",
		"application/zip",
		"video/mp4",
		"application/javascript",
	}
	for _, m := range disallowed {
		if isMIMEAllowed(m) {
			t.Errorf("expected %q to be disallowed", m)
		}
	}
}
