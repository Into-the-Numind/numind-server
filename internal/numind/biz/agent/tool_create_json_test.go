package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeJSONInput(t *testing.T, data interface{}, filename string, pretty bool) ToolInput {
	t.Helper()
	type jsonIn struct {
		Data     interface{} `json:"data"`
		Filename string      `json:"filename,omitempty"`
		Pretty   bool        `json:"pretty,omitempty"`
	}
	b, err := json.Marshal(jsonIn{Data: data, Filename: filename, Pretty: pretty})
	require.NoError(t, err)
	return b
}

func TestCreateJSONTool_Name(t *testing.T) {
	tool := &createJSONTool{}
	assert.Equal(t, "create_json", tool.Name())
}

func TestCreateJSONTool_IsEnabled(t *testing.T) {
	tool := &createJSONTool{}
	assert.True(t, tool.IsEnabled(ToolConfig{}))
}

func TestCreateJSONTool_InputSchema_ValidJSON(t *testing.T) {
	tool := &createJSONTool{}
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(schema, &m), "InputSchema must be valid JSON")
}

func TestCreateJSONTool_Execute_Pretty(t *testing.T) {
	tool := &createJSONTool{}
	ctx := context.Background()
	data := map[string]interface{}{"key": "value", "num": 42}
	input := makeJSONInput(t, data, "pretty.json", true)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "json", out.Format)
	assert.Equal(t, "pretty.json", out.Filename)
	// Pretty output is larger than compact (has newlines + indentation).
	// We just verify the size is non-zero and format is correct.
	assert.True(t, out.SizeBytes > 0)
}

func TestCreateJSONTool_Execute_Compact(t *testing.T) {
	tool := &createJSONTool{}
	ctx := context.Background()
	data := map[string]interface{}{"a": 1}
	input := makeJSONInput(t, data, "compact.json", false)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "json", out.Format)
	assert.Equal(t, "compact.json", out.Filename)
}

func TestCreateJSONTool_Execute_NilData(t *testing.T) {
	tool := &createJSONTool{}
	ctx := context.Background()
	// null is a valid JSON value.
	input := ToolInput(`{"data": null, "filename": "null.json"}`)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "json", out.Format)
	// "null" is 4 bytes.
	assert.Equal(t, int64(4), out.SizeBytes)
}

func TestCreateJSONTool_Execute_AutoFilename(t *testing.T) {
	tool := &createJSONTool{}
	ctx := context.Background()
	input := makeJSONInput(t, "hello", "", false)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.True(t, strings.HasSuffix(out.Filename, ".json"), "auto-generated filename must end with .json; got %q", out.Filename)
}
