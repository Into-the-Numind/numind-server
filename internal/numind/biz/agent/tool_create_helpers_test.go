package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestSanitizeObjectKeyName_PreservesUnicode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Chinese letters/digits survive (the whole point of the readable-key change).
		{"本周工作小结.docx", "本周工作小结.docx"},
		{"Q3销售复盘报告.docx", "Q3销售复盘报告.docx"},
		// Space → "_"; Chinese preserved around it.
		{"Q3 销售复盘.docx", "Q3_销售复盘.docx"},
		// Plain ASCII unchanged (parity with sanitizeOutputFilename for safe names).
		{"report-2026.csv", "report-2026.csv"},
		// Parens (ASCII and full-width) are not \p{L}/\p{N} → "_"; protects the
		// ')' link-boundary invariant in cos_resign.go. Leading "_" (from the
		// opening paren) is then trimmed by strings.Trim(safe, "_.").
		{"(草稿)报告.docx", "草稿_报告.docx"},
		{"（草稿）报告.docx", "草稿_报告.docx"},
		// Path traversal: slash → "_", ".." collapsed to "." (same as ASCII path).
		{"../etc/passwd", "etc_passwd"},
		{"/abs/路径.txt", "abs_路径.txt"},
	}
	for _, tt := range tests {
		got := sanitizeObjectKeyName(tt.input)
		assert.Equal(t, tt.want, got, "input=%q", tt.input)
	}
}

func TestSanitizeObjectKeyName_Empty(t *testing.T) {
	assert.Equal(t, "output", sanitizeObjectKeyName(""))
	assert.Equal(t, "output", sanitizeObjectKeyName("___"))
	assert.Equal(t, "output", sanitizeObjectKeyName("。。")) // full-width dots → "_" → trimmed
}

func TestSanitizeObjectKeyName_TruncatesOnRuneBoundary(t *testing.T) {
	// 67 Chinese runes = 201 bytes > maxFilenameBytes(200); must back off to 66
	// runes = 198 bytes and stay valid UTF-8 (never split a multibyte rune).
	long := strings.Repeat("本", 67)
	got := sanitizeObjectKeyName(long)
	assert.LessOrEqual(t, len(got), maxFilenameBytes)
	assert.True(t, utf8.ValidString(got), "truncated key must be valid UTF-8")
	assert.Equal(t, strings.Repeat("本", 66), got)
}

func TestTruncateUTF8(t *testing.T) {
	assert.Equal(t, "abc", truncateUTF8("abc", 10))    // shorter than max → unchanged
	assert.Equal(t, "abcde", truncateUTF8("abcde", 5)) // exactly max → unchanged
	assert.Equal(t, "ab", truncateUTF8("abcde", 2))    // ASCII cut
	assert.Equal(t, "本", truncateUTF8("本本", 4))        // 6 bytes → 4 max → 1 rune (3 bytes)
	assert.True(t, utf8.ValidString(truncateUTF8("本本本", 5)))
}

// TestUploadGeneratedFile_ChineseNameInKey verifies the AI's Chinese filename
// reaches the COS object key (not mangled to "______"), satisfying the user's
// requirement that documents stored to COS carry a content-related name.
func TestUploadGeneratedFile_ChineseNameInKey(t *testing.T) {
	ctx := middleware.NewContextWithUserID(context.Background(), 7)
	data := []byte("# 本周工作小结\n\n完成了文档系统。")
	result, err := uploadGeneratedFile(ctx, data, "text/markdown", "本周工作小结.md", "md")
	require.NoError(t, err)

	var out fileCreateOutput
	require.NoError(t, unmarshalFileCreateOutput(result, &out))

	// Object key (in the placeholder URL when COS disabled) keeps the Chinese name.
	assert.Contains(t, out.URL, "agent-outputs/7/", "URL should contain userID segment")
	assert.Contains(t, out.URL, "本周工作小结.md", "COS key must carry the readable Chinese name")
	assert.NotContains(t, out.URL, "______", "Chinese must NOT be mangled to underscores")
	// Filename field stays the exact original (drives download disposition + display).
	assert.Equal(t, "本周工作小结.md", out.Filename)
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
