package sandbox

import (
	"archive/zip"
	"bytes"
	"regexp"
	"strings"
)

// MaxInputFileSizeBytes is the hard ceiling for a single input file
// injected into the sandbox via CopyFileIn. Fixed at 50 MB; not configurable.
const MaxInputFileSizeBytes = 50 * 1024 * 1024 // 50 MB

// MaxInputFilenameLen is the maximum allowed byte length of an input filename.
const MaxInputFilenameLen = 255

// safeFilenameRegex matches only shell-safe filename characters.
// Allows: letters, digits, dot, underscore, hyphen.
// Denies: spaces, slashes, colons, semicolons, angle brackets, etc.
var safeFilenameRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// SanitizeInputFile validates an input file before injecting it into the
// sandbox via CopyFileIn.
//
// Checks (in order):
//  1. File size ≤ 50 MB             → ErrInputTooLarge if exceeded
//  2. Filename length ≤ 255 bytes   → ErrUnsafeFilename if exceeded
//  3. Filename characters safe       → ErrUnsafeFilename if path traversal
//  4. Macro scan (docx/xlsx only)   → ErrMacroDetected if vbaProject.bin
//
// SanitizeInputFile is exported so pool_skill.go can call it, and tests
// can also invoke it directly.
func SanitizeInputFile(filename string, data []byte) error {
	// 1. Size check.
	if len(data) > MaxInputFileSizeBytes {
		return ErrInputTooLarge
	}

	// 2 & 3. Filename checks.
	if len(filename) > MaxInputFilenameLen {
		return ErrUnsafeFilename
	}
	if !safeFilenameRegex.MatchString(filename) {
		return ErrUnsafeFilename
	}
	// Additional path traversal guard: reject if the name contains ".."
	// (regex above prevents "/" and "\" but double-check for ".." explicitly).
	if strings.Contains(filename, "..") {
		return ErrUnsafeFilename
	}

	// 4. Macro scan for Office files.
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".docx") || strings.HasSuffix(lower, ".xlsx") ||
		strings.HasSuffix(lower, ".pptx") || strings.HasSuffix(lower, ".xlsm") ||
		strings.HasSuffix(lower, ".docm") {
		if hasMacro(data) {
			return ErrMacroDetected
		}
	}

	return nil
}

// hasMacro returns true if the Office Open XML bytes contain a vbaProject.bin
// entry, which indicates an embedded VBA macro.
func hasMacro(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// Not a valid zip — can't have a macro.
		return false
	}
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "vbaProject.bin") ||
			strings.EqualFold(f.Name, "xl/vbaProject.bin") ||
			strings.EqualFold(f.Name, "word/vbaProject.bin") ||
			strings.HasSuffix(strings.ToLower(f.Name), "/vbaProject.bin") {
			return true
		}
	}
	return false
}
