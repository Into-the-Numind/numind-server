package agent

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"time"
)

// createCSVTool generates a CSV file from tabular data and uploads it to COS.
type createCSVTool struct {
	BaseTool
}

var _ FullTool = (*createCSVTool)(nil)

func (t *createCSVTool) Name() string { return "create_csv" }
func (t *createCSVTool) Description() string {
	return "Generate a CSV file from tabular data and upload it to cloud storage. Returns a download URL. " +
		"Prefer this over run_python for structured table data — it is faster, requires no sandbox, " +
		"and produces an Excel-compatible file with UTF-8 BOM."
}
func (t *createCSVTool) UserFacingName() string      { return "生成 CSV 文件" }
func (t *createCSVTool) NarrationVerb() string       { return "生成" }
func (t *createCSVTool) IsReadOnly() bool            { return false }
func (t *createCSVTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createCSVTool) InterruptBehavior() string   { return "cancel" }

func (t *createCSVTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"data":     {"type": "array", "items": {"type": "array", "items": {"type": "string"}}, "description": "Rows of data, each row is an array of strings"},
			"headers":  {"type": "array", "items": {"type": "string"}, "description": "Optional header row"},
			"filename": {"type": "string", "description": "Optional output filename (e.g. report.csv)"}
		},
		"required": ["data"]
	}`)
}

type createCSVInput struct {
	Data     [][]string `json:"data"`
	Headers  []string   `json:"headers,omitempty"`
	Filename string     `json:"filename,omitempty"`
}

func (t *createCSVTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createCSVInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("create_csv: invalid input: %w", err)
	}

	if len(in.Data) == 0 {
		return nil, fmt.Errorf("create_csv: data is empty — provide at least one row")
	}

	filename := in.Filename
	if filename == "" {
		filename = "generated_" + time.Now().Format("20060102_150405") + ".csv"
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	// Escape headers to prevent formula injection.
	escapedHeaders := make([]string, len(in.Headers))
	for i, h := range in.Headers {
		escapedHeaders[i] = escapeCSVFormula(h)
	}
	if len(escapedHeaders) > 0 {
		if err := w.Write(escapedHeaders); err != nil {
			return nil, fmt.Errorf("create_csv: write headers: %w", err)
		}
	}
	for _, row := range in.Data {
		// Rows with > 100 columns are permitted — warn-only log is deferred to avoid
		// importing the log package; wide tables are unusual but valid.
		escapedRow := make([]string, len(row))
		for j, cell := range row {
			escapedRow[j] = escapeCSVFormula(cell)
		}
		if err := w.Write(escapedRow); err != nil {
			return nil, fmt.Errorf("create_csv: write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("create_csv: flush: %w", err)
	}

	// Prepend UTF-8 BOM so Excel opens the file correctly without encoding prompts.
	bom := []byte{0xEF, 0xBB, 0xBF}
	data := append(bom, buf.Bytes()...)

	return uploadGeneratedFile(ctx, data, "text/csv; charset=utf-8", filename, "csv")
}

// escapeCSVFormula prevents CSV formula injection by prefixing cells that start
// with formula-trigger characters (=, +, -, @) with a single quote. This forces
// Excel / LibreOffice to treat the cell as a text literal rather than a formula.
//
// This mitigates attacks where a cell value like =HYPERLINK("http://evil.com","click")
// would be executed by a spreadsheet application when the file is opened.
func escapeCSVFormula(s string) string {
	if len(s) == 0 {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}
