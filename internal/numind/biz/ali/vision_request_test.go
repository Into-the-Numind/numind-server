package ali

import "testing"

func TestSOPVisionRequestUsesQwen35FlashWithoutThinking(t *testing.T) {
	body, err := buildVisionRequestBody(
		"https://example.invalid/cover.jpg",
		"识别图片中的文字",
		SOPVisionModel,
	)
	if err != nil {
		t.Fatalf("build Vision request body: %v", err)
	}

	if got := body["model"]; got != "qwen3.5-flash" {
		t.Fatalf("model = %v, want qwen3.5-flash", got)
	}
	if got, ok := body["enable_thinking"].(bool); !ok || got {
		t.Fatalf("enable_thinking = %v, want false", body["enable_thinking"])
	}
	if _, ok := body["thinking_budget"]; ok {
		t.Fatal("non-thinking request must not include thinking_budget")
	}
}
