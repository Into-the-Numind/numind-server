package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/middleware"
)

// unmarshalFileCreateOutput is a test helper that parses a ToolResult into fileCreateOutput.
func unmarshalFileCreateOutput(result ToolResult, out *fileCreateOutput) error {
	return json.Unmarshal(result, out)
}

func TestSanitizeOutputFilename_NormalInput(t *testing.T) {
	name := "report-2026.csv"
	got := sanitizeOutputFilename(name)
	assert.Equal(t, "report-2026.csv", got)
}

func TestSanitizeOutputFilename_SpecialChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my file.csv", "my_file.csv"},
		// "../etc/passwd": slash→"_", ".." collapsed to "." → "._etc_passwd"
		{"../etc/passwd", "._etc_passwd"},
		{"/absolute/path.txt", "_absolute_path.txt"},
		{"hello world  ", "hello_world__"},
	}
	for _, tt := range tests {
		got := sanitizeOutputFilename(tt.input)
		assert.Equal(t, tt.want, got, "input=%q", tt.input)
	}
}

func TestSanitizeOutputFilename_TooLong(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := sanitizeOutputFilename(long)
	assert.Len(t, got, maxFilenameBytes)
}

func TestSanitizeOutputFilename_Empty(t *testing.T) {
	got := sanitizeOutputFilename("")
	assert.Equal(t, "output", got)
}

func TestSanitizeOutputFilename_OnlySpecialChars(t *testing.T) {
	got := sanitizeOutputFilename("_")
	// single underscore is the boundary case — sanitize keeps it but equals "_"
	// which is remapped to "output"
	assert.Equal(t, "output", got)
}

func TestUserIDFromContext_WithValue(t *testing.T) {
	ctx := middleware.NewContextWithUserID(context.Background(), 42)
	assert.Equal(t, uint(42), userIDFromContext(ctx))
}

func TestUserIDFromContext_WithoutValue(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, uint(0), userIDFromContext(ctx))
}

// TestUploadGeneratedFile_COSDisabled verifies that when COS is disabled,
// the helper returns a /local-uploads/... placeholder URL and no error.
func TestUploadGeneratedFile_COSDisabled(t *testing.T) {
	// COS is always disabled in unit-test environments (no viper config loaded).
	ctx := middleware.NewContextWithUserID(context.Background(), 7)
	data := []byte("hello test")
	result, err := uploadGeneratedFile(ctx, data, "text/plain", "hello.txt", "text")
	require.NoError(t, err)
	require.NotNil(t, result)

	// Parse the JSON result.
	var out fileCreateOutput
	require.NoError(t, unmarshalFileCreateOutput(result, &out))

	assert.Contains(t, out.URL, "/local-uploads/", "expected placeholder URL when COS disabled")
	assert.Contains(t, out.URL, "agent-outputs/7/", "URL should contain userID path segment")
	assert.Equal(t, "hello.txt", out.Filename)
	assert.Equal(t, int64(len(data)), out.SizeBytes)
	assert.Equal(t, "text", out.Format)
}

// TestUploadGeneratedFile_TooLarge verifies that files exceeding maxFileBytes are rejected.
func TestUploadGeneratedFile_TooLarge(t *testing.T) {
	ctx := context.Background()
	// Allocate a slice that is just over the limit.
	data := make([]byte, maxFileBytes+1)
	_, err := uploadGeneratedFile(ctx, data, "text/plain", "big.txt", "text")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}
