package agent

import (
	"context"
	"encoding/json"
	"strings"
)

type createPPTXTool struct {
	BaseTool
}

var _ FullTool = (*createPPTXTool)(nil)

func (t *createPPTXTool) Name() string { return "create_pptx" }
func (t *createPPTXTool) Description() string {
	return "Generate a standard .pptx PowerPoint deck without using the sandbox. Use for simple title, subtitle, bullet, and notes slides. For branded templates, complex layouts, images, charts, or precise visual design, use load_skill(\"pptx-author\") then run_python."
}
func (t *createPPTXTool) UserFacingName() string      { return "生成 PowerPoint 演示文稿" }
func (t *createPPTXTool) NarrationVerb() string       { return "生成" }
func (t *createPPTXTool) IsDestructive() bool         { return false }
func (t *createPPTXTool) IsReadOnly() bool            { return false }
func (t *createPPTXTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createPPTXTool) InterruptBehavior() string   { return "cancel" }
func (t *createPPTXTool) MaxResultSizeChars() int     { return 4096 }

func (t *createPPTXTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"slides": {
				"type": "array",
				"description": "Slides for a standard deck.",
				"items": {
					"type": "object",
					"properties": {
						"title": {"type": "string"},
						"subtitle": {"type": "string"},
						"bullets": {"type": "array", "items": {"type": "string"}},
						"notes": {"type": "string"}
					},
					"required": ["title"]
				}
			},
			"filename": {"type": "string", "description": "Optional output filename (e.g. deck.pptx). The .pptx extension is added automatically if missing."}
		},
		"required": ["slides"]
	}`)
}

type createPPTXInput struct {
	Slides   []pptxSlideInput `json:"slides"`
	Filename string           `json:"filename,omitempty"`
}

type pptxSlideInput struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Bullets  []string `json:"bullets,omitempty"`
	Notes    string   `json:"notes,omitempty"`
}

func (t *createPPTXTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createPPTXInput
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_pptx", "invalid input: %v", err)
	}
	if len(in.Slides) == 0 {
		return softToolError("create_pptx", "slides is required")
	}
	data, err := buildNativePPTX(in)
	if err != nil {
		return softToolError("create_pptx", "%v", err)
	}

	filename := resolveOfficeFilename(strings.TrimSpace(in.Filename), "deck", ".pptx")
	result, err := uploadGeneratedFile(ctx, data, pptxContentTypeNative, filename, "pptx")
	if err != nil {
		return softToolError("create_pptx", "upload failed: %v", err)
	}
	return result, nil
}
