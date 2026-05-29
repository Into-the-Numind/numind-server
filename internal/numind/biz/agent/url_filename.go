package agent

import (
	"net/url"
	"strings"
)

// extractFilenameFromURL pulls the last non-empty path segment out of rawURL
// and runs it through sanitizeOutputFilename. Returns "input_file" when the
// URL is unparseable or has no usable path component.
//
// History: this helper lived in tool_invoke_skill.go until 2026-05-29's
// skill-progressive-loader refactor deleted that file. tool_run_python.go is
// the remaining caller (used when downloading user-supplied input_files into
// the sandbox). Promoted to its own tiny file so removal of any single tool
// can no longer accidentally take it down.
func extractFilenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "input_file"
	}
	// EscapedPath() returns the path with percent-encoding preserved (not
	// decoded), which ensures "%20" is kept raw and sanitized to "_20" rather
	// than decoded to " ".
	rawPath := u.EscapedPath()
	parts := strings.Split(rawPath, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part != "" {
			return sanitizeOutputFilename(part)
		}
	}
	return "input_file"
}
