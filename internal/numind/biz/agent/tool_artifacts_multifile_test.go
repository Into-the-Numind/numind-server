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
// run_python files get STANDALONE card lines appended (so each is reliably a card),
// and the model's inline links (prose + table row) are PRESERVED, never stripped —
// stripping a table cell emptied it and detached the card (dev 2026-06-18 followup,
// user-reported on dev).
func TestArtifactCollector_FinalizeInto_RunPythonMultiFileCards(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	require.NotNil(t, c)

	for _, a := range artifactsFromToolResult(runPythonMultiFileOutput) {
		c.add(a.URL, a.Filename, a.Mime)
	}

	// Model wrote both files as inline links — one in prose (trailing punctuation, not
	// standalone), one inside a table row.
	content := "已为你生成两份文件：\n\n" +
		"详见 [报告.docx](" + cosPyDocx + ")。\n\n" +
		"| 文件 | 链接 |\n|---|---|\n" +
		"| 页面 | [页面.html](" + cosPyHTML + ") |\n"

	got := c.finalizeInto(content)

	// Standalone card lines appended for BOTH files (the actionable cards the frontend
	// lifts into AgentArtifactItem).
	var docxCard, htmlCard bool
	for _, line := range strings.Split(got, "\n") {
		tl := strings.TrimSpace(line)
		if tl == "[报告.docx]("+cosPyDocx+")" {
			docxCard = true
		}
		if tl == "[页面.html]("+cosPyHTML+")" {
			htmlCard = true
		}
	}
	assert.True(t, docxCard, "docx must get a standalone card line")
	assert.True(t, htmlCard, "html must get a standalone card line")

	// The model's inline prose link and table row are PRESERVED (not stripped → no empty
	// cell). Each URL therefore appears twice: original context + appended card.
	assert.Contains(t, got, "详见 [报告.docx]("+cosPyDocx+")。", "prose inline link preserved")
	assert.Contains(t, got, "| 页面 | [页面.html]("+cosPyHTML+") |", "table row preserved (no empty cell)")
}
