package agent

// BE-2: create_docx — native Markdown -> .docx deterministic fast path.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateDocx_EmptyMarkdownSoftError: empty markdown returns a SOFT tool error
// (nil Go error, JSON "error" field) so the run is not killed.
func TestCreateDocx_EmptyMarkdownSoftError(t *testing.T) {
	tool := &createDocxTool{}
	for _, input := range []string{`{}`, `{"markdown":""}`, `{"markdown":"   "}`} {
		res, err := tool.Execute(context.Background(), ToolInput(input))
		require.NoError(t, err, "must be a soft error (nil Go error)")
		var out map[string]any
		require.NoError(t, json.Unmarshal(res, &out))
		assert.Contains(t, out["error"], "create_docx", "input %q should yield a create_docx soft error", input)
	}
}

// TestCreateDocx_InvalidJSONSoftError: malformed JSON is a soft error too.
func TestCreateDocx_InvalidJSONSoftError(t *testing.T) {
	tool := &createDocxTool{}
	res, err := tool.Execute(context.Background(), ToolInput(`{not json`))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(res, &out))
	assert.Contains(t, out["error"], "invalid input")
}

func TestCreateDocx_InputFileLimitSoftError(t *testing.T) {
	tool := &createDocxTool{}
	files := make([]string, createDocxMaxInputFiles+1)
	for i := range files {
		files[i] = "https://example.com/image.png"
	}
	input, err := json.Marshal(createDocxInput{Markdown: "# Report", InputFiles: files})
	require.NoError(t, err)

	res, err := tool.Execute(context.Background(), ToolInput(input))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(res, &out))
	assert.Contains(t, out["error"], "too many input_files")
}

func TestCreateDocx_FilenameForcedDocxExtension(t *testing.T) {
	cases := []struct {
		in      string
		wantExt string
	}{
		{"report", ".docx"},
		{"report.docx", ".docx"},
		{"周报", ".docx"},
		{"a/b/c", ".docx"},
	}
	for _, c := range cases {
		got := resolveOfficeFilename(c.in, "document", ".docx")
		assert.Truef(t, strings.HasSuffix(strings.ToLower(got), c.wantExt),
			"in=%q resolved=%q want suffix %q", c.in, got, c.wantExt)
	}
}

func TestCreateDocx_ExecuteUploadsStandardDocument(t *testing.T) {
	tool := &createDocxTool{}
	res, err := tool.Execute(context.Background(), ToolInput(`{"markdown":"# 标题\n\n正文\n\n- A","filename":"report"}`))
	require.NoError(t, err)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(res, &out))
	assert.Equal(t, "docx", out.Format)
	assert.True(t, strings.HasSuffix(out.Filename, ".docx"))
	assert.NotEmpty(t, out.URL)
	assert.Greater(t, out.SizeBytes, int64(0))
}
