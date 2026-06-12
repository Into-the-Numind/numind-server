package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDocumentGenerateTool_Execute_EmptyPromptReturnsError(t *testing.T) {
	tool := &documentGenerateTool{}
	// tool-soft-error-sweep: now a SOFT error (nil Go error) so the run survives.
	input, _ := json.Marshal(documentGenerateInput{Prompt: ""})
	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("expected soft error for empty prompt, got hard error: %v", err)
	}
	if !contains(string(result), "prompt is required") {
		t.Errorf("soft error should mention missing prompt, got: %s", result)
	}
}

func TestDocumentGenerateTool_Execute_BadJSON(t *testing.T) {
	tool := &documentGenerateTool{}
	// tool-soft-error-sweep: now a SOFT error (nil Go error) so the run survives.
	result, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err != nil {
		t.Fatalf("expected soft error for bad JSON, got hard error: %v", err)
	}
	if !contains(string(result), "invalid input") {
		t.Errorf("soft error should mention invalid input, got: %s", result)
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

// TestDocumentGenerateTool_StubBehavior asserts the #3 stub contract:
// Execute returns clear stub error even with valid input; IsEnabled gated.
func TestDocumentGenerateTool_StubBehavior(t *testing.T) {
	tool := &documentGenerateTool{}
	// Stub: IsEnabled false regardless of ToolConfig (until #12 registers taskID)
	if tool.IsEnabled(ToolConfig{}) {
		t.Error("stub document_generate must be IsEnabled=false by default")
	}
	// Execute with valid prompt: stub message comes back SOFT so the run
	// survives even if IsEnabled gating is bypassed (tool-soft-error-sweep).
	raw, _ := json.Marshal(documentGenerateInput{Prompt: "Hello"})
	result, err := tool.Execute(context.Background(), ToolInput(raw))
	if err != nil {
		t.Fatalf("expected soft stub error, got hard error: %v", err)
	}
	if !contains(string(result), "请勿重试") {
		t.Errorf("soft stub error must carry the no-retry instruction, got: %s", result)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
