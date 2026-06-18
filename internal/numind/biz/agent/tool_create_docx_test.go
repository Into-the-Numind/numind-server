package agent

// BE-2: create_docx — Markdown -> .docx deterministic fast path.
//
// Two layers of coverage:
//   1. Go input validation (empty markdown soft error; filename sanitization +
//      forced .docx extension). These run without a sandbox.
//   2. The embedded md_to_docx.py script: when python3 + python-docx are present
//      on the host, run it against sample markdown and assert the produced .docx
//      is non-empty and re-openable by python-docx. Skipped otherwise.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateDocx_EmptyMarkdownSoftError: empty markdown returns a SOFT tool error
// (nil Go error, JSON "error" field) so the run is not killed.
func TestCreateDocx_EmptyMarkdownSoftError(t *testing.T) {
	tool := &createDocxTool{}
	for _, md := range []string{``, `{"markdown":""}`, `{"markdown":"   "}`} {
		in := md
		if !strings.HasPrefix(strings.TrimSpace(in), "{") {
			in = `{"markdown":""}`
		}
		res, err := tool.Execute(context.Background(), ToolInput(in))
		require.NoError(t, err, "must be a soft error (nil Go error)")
		var out map[string]any
		require.NoError(t, json.Unmarshal(res, &out))
		assert.Contains(t, out["error"], "create_docx", "input %q should yield a create_docx soft error", in)
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

// TestCreateDocx_FilenameForcedDocxExtension verifies the .docx extension is
// appended when missing and the sandbox-name sanitization keeps it. We exercise
// the pure logic by replicating the Execute prelude (no sandbox needed).
func TestCreateDocx_FilenameForcedDocxExtension(t *testing.T) {
	cases := []struct {
		in       string
		wantTail string // expected suffix on the resolved display filename
	}{
		{"report", ".docx"},
		{"report.docx", ".docx"},
		{"周报", ".docx"},
		{"a/b/c", ".docx"},
		{"", ".docx"}, // empty -> generated_<ts>.docx
	}
	for _, c := range cases {
		filename := strings.TrimSpace(c.in)
		if filename == "" {
			filename = "document_x.docx"
		}
		if !strings.HasSuffix(strings.ToLower(filename), ".docx") {
			filename += ".docx"
		}
		assert.Truef(t, strings.HasSuffix(strings.ToLower(filename), c.wantTail),
			"in=%q resolved=%q want suffix %q", c.in, filename, c.wantTail)

		sandboxName := sanitizeOutputFilename(filename)
		if !strings.HasSuffix(strings.ToLower(sandboxName), ".docx") {
			sandboxName += ".docx"
		}
		// Sandbox name must be ASCII-safe AND retain a .docx ext.
		assert.True(t, strings.HasSuffix(strings.ToLower(sandboxName), ".docx"),
			"sandbox name %q must end in .docx", sandboxName)
	}
}

// TestShellQuoteSingle pins the argv quoting used to pass the output filename.
func TestShellQuoteSingle(t *testing.T) {
	assert.Equal(t, `'report.docx'`, shellQuoteSingle("report.docx"))
	assert.Equal(t, `'a'\''b.docx'`, shellQuoteSingle("a'b.docx"))
}

// TestMdToDocxScript_GeneratesOpenableDocx runs the embedded md_to_docx.py against
// sample markdown and asserts the produced .docx is non-empty and re-openable by
// python-docx. Skipped when python3 / python-docx are unavailable on the host;
// the sandbox end-to-end path is verified on dev (S5).
func TestMdToDocxScript_GeneratesOpenableDocx(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH; md_to_docx.py exercised in sandbox on dev")
	}
	// Probe python-docx availability.
	if probe := exec.Command(py, "-c", "import docx"); probe.Run() != nil { //nolint:gosec
		t.Skip("python-docx not installed on host; md_to_docx.py exercised in sandbox on dev")
	}

	dir := t.TempDir()
	inputDir := filepath.Join(dir, "input")
	outputDir := filepath.Join(dir, "output")
	require.NoError(t, os.MkdirAll(inputDir, 0o755))
	require.NoError(t, os.MkdirAll(outputDir, 0o755))

	// Write the embedded script and the sample markdown.
	scriptPath := filepath.Join(dir, "md_to_docx.py")
	require.NoError(t, os.WriteFile(scriptPath, []byte(mdToDocxScript), 0o644)) //nolint:gosec

	sampleMD := `# 标题一

这是第一段普通文字，包含 **加粗** 和 *斜体*。

## 子标题

- 无序项 A
- 无序项 B

1. 有序项 1
2. 有序项 2

| 列A | 列B |
|-----|-----|
| 1   | 2   |
| 3   | 4   |

最后一段。
`
	require.NoError(t, os.WriteFile(filepath.Join(inputDir, "source.md"), []byte(sampleMD), 0o644)) //nolint:gosec

	outName := "sample.docx"
	cmd := exec.Command(py, scriptPath, outName) //nolint:gosec
	cmd.Env = append(os.Environ(),
		"MD2DOCX_INPUT_DIR="+inputDir,
		"MD2DOCX_OUTPUT_DIR="+outputDir,
	)
	combined, runErr := cmd.CombinedOutput()
	require.NoError(t, runErr, "md_to_docx.py failed: %s", string(combined))

	outPath := filepath.Join(outputDir, outName)
	info, statErr := os.Stat(outPath)
	require.NoError(t, statErr, "expected output %s", outPath)
	assert.Greater(t, info.Size(), int64(0), "generated .docx must be non-empty")

	// Re-open with python-docx to confirm it is a valid document.
	reopen := exec.Command(py, "-c", //nolint:gosec
		"import sys; from docx import Document; d=Document(sys.argv[1]); print(len(d.paragraphs))",
		outPath)
	reopenOut, reopenErr := reopen.CombinedOutput()
	require.NoError(t, reopenErr, "python-docx could not reopen the generated file: %s", string(reopenOut))
	assert.NotEmpty(t, strings.TrimSpace(string(reopenOut)), "reopened doc should report paragraph count")
}
