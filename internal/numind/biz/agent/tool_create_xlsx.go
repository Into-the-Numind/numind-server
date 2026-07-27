package agent

import (
	"context"
	"encoding/json"
	"strings"
)

type createXLSXTool struct {
	BaseTool
}

var _ FullTool = (*createXLSXTool)(nil)

func (t *createXLSXTool) Name() string { return "create_xlsx" }
func (t *createXLSXTool) Description() string {
	return "Generate a standard .xlsx Excel workbook without using the sandbox. Use for normal tables, multiple sheets, and simple workbook output. For formulas, charts, advanced styling, templates, or large data processing, use load_skill(\"xlsx-author\") then run_python."
}
func (t *createXLSXTool) UserFacingName() string      { return "生成 Excel 工作簿" }
func (t *createXLSXTool) NarrationVerb() string       { return "生成" }
func (t *createXLSXTool) IsDestructive() bool         { return false }
func (t *createXLSXTool) IsReadOnly() bool            { return false }
func (t *createXLSXTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createXLSXTool) InterruptBehavior() string   { return "cancel" }
func (t *createXLSXTool) MaxResultSizeChars() int     { return 4096 }

func (t *createXLSXTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"sheets": {
				"type": "array",
				"description": "Workbook sheets. Each sheet has name, optional headers, and rows. Prefer this for multi-sheet workbooks.",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"headers": {"type": "array", "items": {"type": "string"}},
						"rows": {"type": "array", "items": {}}
					},
					"required": ["rows"]
				}
			},
			"headers": {"type": "array", "items": {"type": "string"}, "description": "Single-sheet headers when sheets is omitted."},
			"rows": {"type": "array", "items": {}, "description": "Single-sheet rows when sheets is omitted. Rows can be arrays or objects."},
			"filename": {"type": "string", "description": "Optional output filename (e.g. report.xlsx). The .xlsx extension is added automatically if missing."}
		}
	}`)
}

type createXLSXInput struct {
	Sheets   []xlsxSheetInput `json:"sheets,omitempty"`
	Headers  []string         `json:"headers,omitempty"`
	Rows     []any            `json:"rows,omitempty"`
	Filename string           `json:"filename,omitempty"`
}

type xlsxSheetInput struct {
	Name    string   `json:"name,omitempty"`
	Headers []string `json:"headers,omitempty"`
	Rows    []any    `json:"rows"`
}

func (t *createXLSXTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createXLSXInput
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_xlsx", "invalid input: %v", err)
	}
	if len(normalizeXLSXSheets(in)) == 0 {
		return softToolError("create_xlsx", "rows or sheets is required")
	}
	data, err := buildNativeXLSX(in)
	if err != nil {
		return softToolError("create_xlsx", "%v", err)
	}

	filename := resolveOfficeFilename(strings.TrimSpace(in.Filename), "workbook", ".xlsx")
	result, err := uploadGeneratedFile(ctx, data, xlsxContentTypeNative, filename, "xlsx")
	if err != nil {
		return softToolError("create_xlsx", "upload failed: %v", err)
	}
	return result, nil
}
