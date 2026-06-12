package agent

import (
	"context"
	"encoding/json"
	"time"
)

// createTextTool writes plain text content to a .txt file and uploads it to COS.
type createTextTool struct {
	BaseTool
}

var _ FullTool = (*createTextTool)(nil)

func (t *createTextTool) Name() string { return "create_text" }
func (t *createTextTool) Description() string {
	return "Write plain text content to a .txt file and upload it to cloud storage. Returns a download URL. " +
		"Use for logs, notes, or unstructured text output. Empty content is accepted (produces a valid empty file)."
}
func (t *createTextTool) UserFacingName() string      { return "生成文本文件" }
func (t *createTextTool) NarrationVerb() string       { return "生成" }
func (t *createTextTool) IsReadOnly() bool            { return false }
func (t *createTextTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createTextTool) InterruptBehavior() string   { return "cancel" }

func (t *createTextTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content":  {"type": "string", "description": "Plain text content to write"},
			"filename": {"type": "string", "description": "Optional output filename (e.g. notes.txt)"}
		},
		"required": ["content"]
	}`)
}

type createTextInput struct {
	Content  string `json:"content"`
	Filename string `json:"filename,omitempty"`
}

func (t *createTextTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createTextInput
	// Model-input and recoverable failures stay soft (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_text", "invalid input: %v", err)
	}

	filename := in.Filename
	if filename == "" {
		filename = "generated_" + time.Now().Format("20060102_150405") + ".txt"
	}

	data := []byte(in.Content)
	result, err := uploadGeneratedFile(ctx, data, "text/plain; charset=utf-8", filename, "text")
	if err != nil {
		return softToolError("create_text", "upload failed: %v", err)
	}
	return result, nil
}
