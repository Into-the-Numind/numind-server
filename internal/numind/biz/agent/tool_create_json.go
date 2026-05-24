package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// createJSONTool serializes data to a JSON file and uploads it to COS.
type createJSONTool struct {
	BaseTool
}

var _ FullTool = (*createJSONTool)(nil)

func (t *createJSONTool) Name() string { return "create_json" }
func (t *createJSONTool) Description() string {
	return "Serialize data to a JSON file and upload it to cloud storage. Returns a download URL. " +
		"Supports pretty-print formatting. Use for structured data output, API responses, or configuration files."
}
func (t *createJSONTool) UserFacingName() string      { return "生成 JSON 文件" }
func (t *createJSONTool) NarrationVerb() string       { return "生成" }
func (t *createJSONTool) IsReadOnly() bool            { return false }
func (t *createJSONTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createJSONTool) InterruptBehavior() string   { return "cancel" }

func (t *createJSONTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"data":     {"description": "Any JSON-serializable data (object, array, string, number, null)"},
			"filename": {"type": "string", "description": "Optional output filename (e.g. data.json)"},
			"pretty":   {"type": "boolean", "description": "Pretty-print with 4-space indentation (default: false)"}
		},
		"required": ["data"]
	}`)
}

type createJSONInput struct {
	Data     interface{} `json:"data"`
	Filename string      `json:"filename,omitempty"`
	Pretty   bool        `json:"pretty,omitempty"`
}

func (t *createJSONTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createJSONInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("create_json: invalid input: %w", err)
	}

	filename := in.Filename
	if filename == "" {
		filename = "generated_" + time.Now().Format("20060102_150405") + ".json"
	}

	var (
		out []byte
		err error
	)
	if in.Pretty {
		out, err = json.MarshalIndent(in.Data, "", "    ")
	} else {
		out, err = json.Marshal(in.Data)
	}
	if err != nil {
		return nil, fmt.Errorf("create_json: marshal data: %w", err)
	}

	return uploadGeneratedFile(ctx, out, "application/json; charset=utf-8", filename, "json")
}
