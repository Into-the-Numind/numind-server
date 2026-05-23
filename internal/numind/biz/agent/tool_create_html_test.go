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
	input := makeHTMLInput(t, "content", "{{.unclosed", "", "bad.html")
	_, err := tool.Execute(ctx, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template parse error")
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
