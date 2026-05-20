package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/pkg/aiservice"
)

type documentGenerateTool struct {
	BaseTool
}

type documentGenerateInput struct {
	Prompt string `json:"prompt"`
	// Format is "markdown" or "plain"; defaults to "markdown" when empty.
	Format string `json:"format,omitempty"`
}

var _ FullTool = (*documentGenerateTool)(nil)

func (t *documentGenerateTool) Name() string { return "document_generate" }
func (t *documentGenerateTool) Description() string {
	return "Generate a long-form document based on the prompt. Returns markdown text."
}
func (t *documentGenerateTool) UserFacingName() string  { return "文档生成" }
func (t *documentGenerateTool) NarrationVerb() string   { return "生成" }
func (t *documentGenerateTool) IsReadOnly() bool        { return false }
func (t *documentGenerateTool) MaxResultSizeChars() int { return 50000 }

func (t *documentGenerateTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in documentGenerateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	if in.Prompt == "" {
		return nil, fmt.Errorf("document_generate: prompt is required")
	}
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleSystem,
				Content: aiservice.MessageContent{Text: "You are a professional document writer. Output markdown."},
			},
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: in.Prompt},
			},
		},
	}
	resp, err := aiservice.Chat(ctx, "agent-tool-document-generate", req)
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]string{"content": resp.Content})
	return ToolResult(out), nil
}
