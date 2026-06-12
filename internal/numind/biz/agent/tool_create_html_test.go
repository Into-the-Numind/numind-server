package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeHTMLInput(t *testing.T, content interface{}, tmpl, title, filename string) ToolInput {
	t.Helper()
	type htmlIn struct {
		Content  interface{} `json:"content"`
		Template string      `json:"template,omitempty"`
		Title    string      `json:"title,omitempty"`
		Filename string      `json:"filename,omitempty"`
	}
	b, err := json.Marshal(htmlIn{Content: content, Template: tmpl, Title: title, Filename: filename})
	require.NoError(t, err)
	return b
}

func TestCreateHTMLTool_Name(t *testing.T) {
	tool := &createHTMLTool{}
	assert.Equal(t, "create_html", tool.Name())
}

func TestCreateHTMLTool_IsEnabled(t *testing.T) {
	tool := &createHTMLTool{}
	assert.True(t, tool.IsEnabled(ToolConfig{}))
}

func TestCreateHTMLTool_InputSchema_ValidJSON(t *testing.T) {
	tool := &createHTMLTool{}
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(schema, &m), "InputSchema must be valid JSON")
}

func TestCreateHTMLTool_Execute_DefaultTemplate_StringContent(t *testing.T) {
	tool := &createHTMLTool{}
	ctx := context.Background()
	input := makeHTMLInput(t, "Hello, World!", "", "Test Page", "test.html")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "html", out.Format)
	assert.Equal(t, "test.html", out.Filename)
	assert.True(t, out.SizeBytes > 0)
}

func TestCreateHTMLTool_Execute_CustomTemplate(t *testing.T) {
	tool := &createHTMLTool{}
	ctx := context.Background()
	customTmpl := `<html><body>Hello, {{.Name}}!</body></html>`
	// map content with "Name" key matching the template
	content := map[string]interface{}{"Name": "Alice"}
	input := makeHTMLInput(t, content, customTmpl, "", "custom.html")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "html", out.Format)
	assert.Equal(t, "custom.html", out.Filename)
}

func TestCreateHTMLTool_Execute_BadTemplate(t *testing.T) {
	tool := &createHTMLTool{}
	ctx := context.Background()
	// tool-soft-error-sweep: a model-supplied bad template is input-derived →
	// SOFT error (nil Go error) so the run survives.
	input := makeHTMLInput(t, "content", "{{.unclosed", "", "bad.html")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.Contains(t, string(result), "template parse error")
}

func TestCreateHTMLTool_Execute_AutoFilename(t *testing.T) {
	tool := &createHTMLTool{}
	ctx := context.Background()
	input := makeHTMLInput(t, "no filename", "", "", "")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.True(t, strings.HasSuffix(out.Filename, ".html"), "auto-generated filename should end with .html; got %q", out.Filename)
}

// TestRenderHTML_FullDocumentVerbatim verifies that a complete HTML document
// passed as string content is served byte-for-byte, NOT escaped or wrapped.
//
// This is the regression test for the 2026-05-29 bug: create_html previously ran
// all string content through html/template's {{.Body}} (plain string), which
// HTML-escaped a full document and nested it inside a generic wrapper — the
// browser then showed the page SOURCE as literal text instead of rendering it.
// create_html's purpose is to publish agent-authored HTML; see the threat-model
// note on renderHTML for why raw output is correct and safe here.
func TestRenderHTML_FullDocumentVerbatim(t *testing.T) {
	doc := `<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>报告</title>
<style>body{background:#0d1117;color:#c9d1d9}</style></head>
<body><h1>GitHub 周榜</h1><div class="card">内容</div></body>
</html>`

	out, err := renderHTML(createHTMLInput{Content: doc})
	require.NoError(t, err)
	rendered := string(out)

	// Served verbatim: identical bytes, no escaping, no extra wrapper.
	assert.Equal(t, doc, rendered, "full HTML document must be served byte-for-byte")
	assert.NotContains(t, rendered, "&lt;", "document must NOT be HTML-escaped")
	assert.Contains(t, rendered, "<style>body{background:#0d1117", "CSS must survive intact")
	// The generic wrapper's hard-coded body{max-width:900px} style must NOT be injected.
	assert.NotContains(t, rendered, "max-width:900px", "full document must not be re-wrapped")
}

// TestRenderHTML_FragmentWrappedRaw verifies that a bare fragment is wrapped in
// the default styled document and rendered as real HTML (not escaped).
func TestRenderHTML_FragmentWrappedRaw(t *testing.T) {
	out, err := renderHTML(createHTMLInput{Title: "标题", Content: `<h1>Hello</h1><p>世界</p>`})
	require.NoError(t, err)
	rendered := string(out)

	assert.Contains(t, rendered, "<!DOCTYPE html>", "fragment must be wrapped in a full document")
	assert.Contains(t, rendered, "<title>标题</title>", "title must populate the wrapper")
	assert.Contains(t, rendered, "<h1>Hello</h1>", "fragment markup must render raw, not escaped")
	assert.NotContains(t, rendered, "&lt;h1&gt;", "fragment must NOT be HTML-escaped")
}

// TestRenderHTML_CustomTemplateEscapes verifies the escaping opt-in is preserved:
// a caller-supplied template still escapes interpolated variables via html/template.
func TestRenderHTML_CustomTemplateEscapes(t *testing.T) {
	out, err := renderHTML(createHTMLInput{
		Template: `<!DOCTYPE html><html><body>{{.Body}}</body></html>`,
		Content:  `<script>alert(1)</script>`,
	})
	require.NoError(t, err)
	rendered := string(out)

	assert.NotContains(t, rendered, "<script>alert(1)</script>",
		"custom template must escape interpolated variables")
	assert.Contains(t, rendered, "&lt;script&gt;",
		"<script> must be escaped as &lt;script&gt; under the custom-template path")
}

// TestRenderHTML_MapWithoutTemplate verifies a {title, body} map (case-insensitive)
// is wrapped in the default document with a raw body.
func TestRenderHTML_MapWithoutTemplate(t *testing.T) {
	out, err := renderHTML(createHTMLInput{
		Content: map[string]interface{}{"title": "地图标题", "body": "<section>正文</section>"},
	})
	require.NoError(t, err)
	rendered := string(out)

	assert.Contains(t, rendered, "<title>地图标题</title>")
	assert.Contains(t, rendered, "<section>正文</section>", "map body must render raw")
}
