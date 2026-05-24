package sandbox

import (
	"archive/zip"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// defaultMaxOutputSizeBytes is the absolute hard ceiling for a single sandbox
// output file when no maxBytes is specified by the caller.
// The config's OutputMaxSizeMB can only lower this, not raise it.
const defaultMaxOutputSizeBytes = 50 * 1024 * 1024 // 50 MB

// zipBombExpandedCeiling is the hard-coded maximum allowed decompressed size
// for a zip/docx/xlsx/pptx archive. Not configurable — this is a security
// floor, not a business setting.
const zipBombExpandedCeiling = 500 * 1024 * 1024 // 500 MB

// ScanOutput performs output-side security checks on a sandbox-produced file.
//
// Checks (in order):
//  1. File size > maxBytes (hard ceiling 50 MB) → ErrOutputTooLarge
//  2. Zip-bomb: for zip-family files (zip/docx/xlsx/pptx), decompressed
//     size > 500 MB → ErrZipBomb
//  3. MIME mismatch: detected content type vs declaredMime → ErrMimeMismatch
//
// maxBytes controls the per-file size ceiling. If maxBytes <= 0 the default
// (50 MB) is used. The ceiling cannot be raised above 50 MB — values above
// defaultMaxOutputSizeBytes are silently capped.
//
// declaredMime may be empty; if so, the MIME check is skipped.
func ScanOutput(path string, declaredMime string, maxBytes int64) error {
	// Resolve effective size limit.
	effectiveMax := maxBytes
	if effectiveMax <= 0 || effectiveMax > defaultMaxOutputSizeBytes {
		effectiveMax = defaultMaxOutputSizeBytes
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ScanOutput: stat %s: %w", path, err)
	}

	// 1. File size check.
	if info.Size() > effectiveMax {
		return ErrOutputTooLarge
	}

	// Read the first 512 bytes for MIME detection and zip header check.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("ScanOutput: open %s: %w", path, err)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, _ := f.Read(header)
	header = header[:n]
	// NOTE: no explicit f.Close() here — defer f.Close() above handles cleanup.

	// Detect actual MIME from file content (not from extension or caller).
	detectedMime := http.DetectContentType(header)
	// Normalise to the bare type (strip "; charset=..." etc.).
	detectedMime = normaliseMime(detectedMime)

	// 2. Zip-bomb check (only for zip-family files).
	if isZipFamily(path, header) {
		if err := checkZipBomb(path); err != nil {
			return err
		}
	}

	// 3. MIME mismatch check.
	if declaredMime != "" {
		declared := normaliseMime(declaredMime)
		if !mimeCompatible(detectedMime, declared) {
			return ErrMimeMismatch
		}
	}

	return nil
}

// checkZipBomb opens the file as a zip archive and sums the uncompressed sizes
// of all entries. Returns ErrZipBomb if the total exceeds zipBombExpandedCeiling.
// Returns nil (not ErrZipBomb) if the file cannot be opened as a zip archive
// (e.g. corrupted), so non-zip files passed through isZipFamily are handled
// gracefully.
func checkZipBomb(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		// Not a valid zip; skip the bomb check.
		return nil
	}
	defer zr.Close()

	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > zipBombExpandedCeiling {
			return ErrZipBomb
		}
	}
	return nil
}

// isZipFamily returns true when the file appears to be a zip-family archive
// based on extension or the PK magic bytes (0x50 0x4B).
func isZipFamily(path string, header []byte) bool {
	lower := strings.ToLower(path)
	for _, ext := range []string{".zip", ".docx", ".xlsx", ".pptx", ".jar", ".apk"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// PK magic bytes: 0x50 0x4B 0x03 0x04
	if len(header) >= 4 && header[0] == 0x50 && header[1] == 0x4B {
		return true
	}
	return false
}

// normaliseMime strips parameters from a MIME type string.
// e.g. "text/plain; charset=utf-8" → "text/plain"
func normaliseMime(mime string) string {
	if idx := strings.Index(mime, ";"); idx != -1 {
		return strings.TrimSpace(mime[:idx])
	}
	return strings.TrimSpace(mime)
}

// mimeCompatible returns true if detected and declared MIME types are
// compatible. Compatibility is broad to avoid false positives:
//   - Exact match always OK.
//   - Office Open XML files (.docx/.xlsx/.pptx) are zip archives internally;
//     Go's DetectContentType returns "application/zip" for them.
//   - "application/octet-stream" is the Go fallback for binary data; accept it
//     for any declared binary type.
func mimeCompatible(detected, declared string) bool {
	if detected == declared {
		return true
	}
	// Office Open XML is a zip file — detected will be "application/zip".
	officeTypes := map[string]bool{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
		"application/msword":            true,
		"application/vnd.ms-excel":      true,
		"application/vnd.ms-powerpoint": true,
	}
	if officeTypes[declared] && detected == "application/zip" {
		return true
	}
	// application/octet-stream is the binary fallback — treat as compatible
	// with any declared binary (non-text) type.
	if detected == "application/octet-stream" && !strings.HasPrefix(declared, "text/") {
		return true
	}
	// text/plain is often detected for CSV, markdown, etc.
	// Note: normaliseMime already strips "; charset=..." so detected will
	// never be "text/plain; charset=utf-8" here — only "text/plain".
	if detected == "text/plain" {
		if strings.HasPrefix(declared, "text/") {
			return true
		}
	}
	return false
}
