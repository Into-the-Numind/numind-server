package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTextInput(t *testing.T, content, filename string) ToolInput {
	t.Helper()
	type textIn struct {
		Content  string `json:"content"`
		Filename string `json:"filename,omitempty"`
	}
	b, err := json.Marshal(textIn{Content: content, Filename: filename})
	require.NoError(t, err)
	return b
}

func TestCreateTextTool_Name(t *testing.T) {
	tool := &createTextTool{}
	assert.Equal(t, "create_text", tool.Name())
}

func TestCreateTextTool_IsEnabled(t *testing.T) {
	tool := &createTextTool{}
	assert.True(t, tool.IsEnabled(ToolConfig{}))
}

func TestCreateTextTool_InputSchema_ValidJSON(t *testing.T) {
	tool := &createTextTool{}
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(schema, &m), "InputSchema must be valid JSON")
}

func TestCreateTextTool_Execute_Normal(t *testing.T) {
	tool := &createTextTool{}
	ctx := context.Background()
	content := "Hello, World!"
	input := makeTextInput(t, content, "hello.txt")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "text", out.Format)
	assert.Equal(t, "hello.txt", out.Filename)
	assert.Equal(t, int64(len(content)), out.SizeBytes)
}

func TestCreateTextTool_Execute_EmptyContent(t *testing.T) {
	tool := &createTextTool{}
	ctx := context.Background()
	input := makeTextInput(t, "", "empty.txt")
	result, err := tool.Execute(ctx, input)
	// Empty content is valid — should succeed.
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "text", out.Format)
	assert.Equal(t, int64(0), out.SizeBytes)
}

func TestCreateTextTool_Execute_MultiLine(t *testing.T) {
	tool := &createTextTool{}
	ctx := context.Background()
	content := "line1\nline2\nline3"
	input := makeTextInput(t, content, "multi.txt")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	// Newlines must be preserved in the byte count.
	assert.Equal(t, int64(len(content)), out.SizeBytes)
}

func TestCreateTextTool_Execute_AutoFilename(t *testing.T) {
	tool := &createTextTool{}
	ctx := context.Background()
	input := makeTextInput(t, "no filename", "")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.True(t, strings.HasSuffix(out.Filename, ".txt"), "auto-generated filename must end with .txt; got %q", out.Filename)
}

func TestCreateTextTool_IsReadOnly(t *testing.T) {
	tool := &createTextTool{}
	assert.False(t, tool.IsReadOnly())
}
