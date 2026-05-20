package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDocumentGenerateTool_Execute_EmptyPromptReturnsError(t *testing.T) {
	tool := &documentGenerateTool{}
	input, _ := json.Marshal(documentGenerateInput{Prompt: ""})
	_, err := tool.Execute(context.Background(), ToolInput(input))
	if err == nil {
		t.Error("expected error for empty prompt")
	}
}

func TestDocumentGenerateTool_Execute_BadJSON(t *testing.T) {
	tool := &documentGenerateTool{}
	_, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err == nil {
		t.Error("expected JSON unmarshal error")
	}
}

func TestDocumentGenerateTool_Metadata(t *testing.T) {
	tool := &documentGenerateTool{}
	if tool.Name() != "document_generate" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
	if tool.IsReadOnly() {
		t.Error("document_generate should not be read-only (it calls an LLM)")
	}
	if tool.MaxResultSizeChars() != 50000 {
		t.Errorf("expected MaxResultSizeChars=50000, got %d", tool.MaxResultSizeChars())
	}
	if tool.UserFacingName() != "文档生成" {
		t.Errorf("unexpected UserFacingName: %s", tool.UserFacingName())
	}
	if tool.NarrationVerb() != "生成" {
		t.Errorf("unexpected NarrationVerb: %s", tool.NarrationVerb())
	}
}

func TestDocumentGenerateTool_Execute_InputUnmarshal(t *testing.T) {
	// Verify input struct roundtrips correctly for the non-aiservice path.
	raw, _ := json.Marshal(documentGenerateInput{Prompt: "Write a report", Format: "markdown"})
	var parsed documentGenerateInput
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Prompt != "Write a report" {
		t.Errorf("unexpected prompt: %s", parsed.Prompt)
	}
	if parsed.Format != "markdown" {
		t.Errorf("unexpected format: %s", parsed.Format)
	}
}
