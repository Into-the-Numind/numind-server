package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCSVInput(t *testing.T, data [][]string, headers []string, filename string) ToolInput {
	t.Helper()
	type csvIn struct {
		Data     [][]string `json:"data"`
		Headers  []string   `json:"headers,omitempty"`
		Filename string     `json:"filename,omitempty"`
	}
	b, err := json.Marshal(csvIn{Data: data, Headers: headers, Filename: filename})
	require.NoError(t, err)
	return b
}

func TestCreateCSVTool_Name(t *testing.T) {
	tool := &createCSVTool{}
	assert.Equal(t, "create_csv", tool.Name())
}

func TestCreateCSVTool_IsEnabled(t *testing.T) {
	tool := &createCSVTool{}
	// IsEnabled should return true regardless of cfg values.
	assert.True(t, tool.IsEnabled(ToolConfig{}))
	assert.True(t, tool.IsEnabled(ToolConfig{EnableSandbox: true}))
}

func TestCreateCSVTool_InputSchema_ValidJSON(t *testing.T) {
	tool := &createCSVTool{}
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(schema, &m), "InputSchema must be valid JSON")
}

func TestCreateCSVTool_Execute_WithHeaders(t *testing.T) {
	tool := &createCSVTool{}
	ctx := context.Background()
	input := makeCSVInput(t,
		[][]string{{"Alice", "1000", "North"}, {"Bob", "2000", "South"}, {"Carol", "1500", "East"}},
		[]string{"Name", "Sales", "Region"},
		"test.csv",
	)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "csv", out.Format)
	assert.Equal(t, "test.csv", out.Filename)
	assert.True(t, out.SizeBytes > 0)
}

func TestCreateCSVTool_Execute_NoHeaders(t *testing.T) {
	tool := &createCSVTool{}
	ctx := context.Background()
	input := makeCSVInput(t,
		[][]string{{"x", "y"}, {"a", "b"}},
		nil,
		"no_headers.csv",
	)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Equal(t, "csv", out.Format)
}

func TestCreateCSVTool_Execute_BOM(t *testing.T) {
	// We cannot easily inspect raw bytes through COS in unit tests,
	// but we can verify SizeBytes accounts for the 3-byte BOM by comparing
	// what an empty+BOM csv should produce.
	tool := &createCSVTool{}
	ctx := context.Background()
	// Single row, single cell.
	input := makeCSVInput(t, [][]string{{"hello"}}, nil, "bom_test.csv")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	// BOM (3) + "hello\n" (6) = at least 9 bytes.
	assert.GreaterOrEqual(t, out.SizeBytes, int64(9), "BOM + content must be accounted for")
}

func TestCreateCSVTool_Execute_EmptyData(t *testing.T) {
	tool := &createCSVTool{}
	ctx := context.Background()

	// nil data (omitted field) → empty slice after unmarshal → should error
	input := ToolInput(`{"data": []}`)
	_, err := tool.Execute(ctx, input)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "empty")
}

func TestCreateCSVTool_Execute_AutoFilename(t *testing.T) {
	tool := &createCSVTool{}
	ctx := context.Background()
	input := makeCSVInput(t, [][]string{{"a", "b"}}, nil, "")
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(result, &out))
	assert.True(t, strings.HasSuffix(out.Filename, ".csv"), "auto-generated filename should end with .csv")
}

func TestCreateCSVTool_IsReadOnly(t *testing.T) {
	tool := &createCSVTool{}
	assert.False(t, tool.IsReadOnly(), "file generation tools are not read-only")
}

// TestCreateCSVTool_FormulaInjection verifies that cells starting with formula-trigger
// characters (=, +, -, @) are prefixed with a single quote to prevent spreadsheet
// formula injection attacks (e.g. =HYPERLINK(...)  executed by Excel/LibreOffice).
func TestCreateCSVTool_FormulaInjection(t *testing.T) {
	cases := []struct {
		input   string
		escaped bool
	}{
		{`=HYPERLINK("http://evil.com","click")`, true},
		{`+1`, true},
		{`-1`, true},
		{`@SUM(A1:A10)`, true},
		{`normal value`, false},
		{``, false},
		{`100`, false},
	}
	for _, tc := range cases {
		got := escapeCSVFormula(tc.input)
		if tc.escaped {
			assert.True(t, len(got) > 0 && got[0] == '\'',
				"cell %q should be prefixed with single quote, got %q", tc.input, got)
			assert.Equal(t, "'"+tc.input, got,
				"escaped cell content mismatch for %q", tc.input)
		} else {
			assert.Equal(t, tc.input, got,
				"safe cell %q must not be modified", tc.input)
		}
	}
}
