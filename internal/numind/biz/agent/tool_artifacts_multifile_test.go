package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// run_python emits a {"files":[...]} array. The collector must pick up EVERY file
// (docx + html), not just a single fileCreateOutput. Bug-from-Customer (问题4): the
// model's run_python that produced docx/html had its URLs parsed as empty by the
// single-file artifactFromToolResult → files were never collected → the final
// answer's inline links (incl. table rows) were neither stripped nor lifted into
// standalone file cards. This test reproduces that gap.
const (
	cosPyDocx = "https://x.cos.ap-guangzhou.myqcloud.com/agent-outputs/1/报告.docx?sig=a"
	cosPyHTML = "https://x.cos.ap-guangzhou.myqcloud.com/agent-outputs/1/页面.html?sig=b"
)

const runPythonMultiFileOutput = `{"files":[` +
	`{"filename":"报告.docx","url":"https://x.cos.ap-guangzhou.myqcloud.com/agent-outputs/1/报告.docx?sig=a","size_bytes":12},` +
	`{"filename":"页面.html","url":"https://x.cos.ap-guangzhou.myqcloud.com/agent-outputs/1/页面.html?sig=b","size_bytes":34}` +
	`],"stdout":"done","exit_code":0,"duration_ms":42}`

// TestArtifactsFromToolResult_RunPythonMultiFile: the plural parser returns BOTH
// files from a run_python output, with mime inferred from each filename.
func TestArtifactsFromToolResult_RunPythonMultiFile(t *testing.T) {
	arts := artifactsFromToolResult(runPythonMultiFileOutput)
	require.Len(t, arts, 2, "both run_python output files must be parsed")

	byName := map[string]toolArtifact{}
	for _, a := range arts {
		byName[a.Filename] = a
	}

	docx, ok := byName["报告.docx"]
	require.True(t, ok, "docx artifact must be present")
	assert.Equal(t, cosPyDocx, docx.URL)
	// docx is not in mimeFromArtifact's image set → non-image mime → collector renders
	// it as a standalone doc card (the load-bearing behavior).
	assert.Equal(t, "application/octet-stream", docx.Mime)
	assert.False(t, strings.HasPrefix(docx.Mime, "image/"), "docx must not be treated as an inline image")

	html, ok := byName["页面.html"]
	require.True(t, ok, "html artifact must be present")
	assert.Equal(t, cosPyHTML, html.URL)
	assert.Equal(t, "text/html", html.Mime)
}

// TestArtifactsFromToolResult_SingleFile: a fileCreateOutput single-file shape
// (create_html / image_gen) still returns exactly one artifact (regression).
func TestArtifactsFromToolResult_SingleFile(t *testing.T) {
	out := `{"url":"https://c/page.html","filename":"page.html","size_bytes":9,"format":"html"}`
	arts := artifactsFromToolResult(out)
	require.Len(t, arts, 1)
	assert.Equal(t, "https://c/page.html", arts[0].URL)
	assert.Equal(t, "page.html", arts[0].Filename)
	assert.Equal(t, "text/html", arts[0].Mime)
}

// TestArtifactsFromToolResult_NonArtifact: non-JSON / no-url payloads → empty slice.
func TestArtifactsFromToolResult_NonArtifact(t *testing.T) {
	assert.Empty(t, artifactsFromToolResult("not json"))
	assert.Empty(t, artifactsFromToolResult(`{"results":["a","b"]}`))
	assert.Empty(t, artifactsFromToolResult(`{"files":[]}`))
}

// TestArtifactCollector_FinalizeInto_RunPythonMultiFileCards: end-to-end — both
// run_python files become STANDALONE card lines, and the model's inline links
// (including ones written inside a markdown table row) referencing the same URLs
// are STRIPPED so there is exactly one card per file and no buried naked link.
func TestArtifactCollector_FinalizeInto_RunPythonMultiFileCards(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	require.NotNil(t, c)

	for _, a := range artifactsFromToolResult(runPythonMultiFileOutput) {
		c.add(a.URL, a.Filename, a.Mime)
	}

	// Model wrote both files as inline links — one in prose, one inside a table row.
	content := "已为你生成两份文件：\n\n" +
		"详见 [报告.docx](" + cosPyDocx + ")。\n\n" +
		"| 文件 | 链接 |\n|---|---|\n" +
		"| 页面 | [页面.html](" + cosPyHTML + ") |\n"

	got := c.finalizeInto(content)

	// Standalone card lines appended for BOTH files.
	assert.Contains(t, got, "[报告.docx]("+cosPyDocx+")")
	assert.Contains(t, got, "[页面.html]("+cosPyHTML+")")

	// The model's inline link nodes (prose + table row) must be stripped: each URL
	// appears EXACTLY once (only in the appended standalone card line).
	assert.Equal(t, 1, strings.Count(got, "("+cosPyDocx+")"), "docx URL must appear once")
	assert.Equal(t, 1, strings.Count(got, "("+cosPyHTML+")"), "html URL must appear once")
}
