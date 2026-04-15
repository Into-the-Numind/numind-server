package llmrouter

import (
	"testing"
)

func Test_InferThinkingFormat_Aihubmix(t *testing.T) {
	cases := []struct {
		name            string
		providerName    string
		providerModelID string
		want            string
	}{
		{
			name:            "WithThinkSuffix_ReturnsNone",
			providerName:    "aihubmix",
			providerModelID: "claude-sonnet-4-6-think",
			want:            ThinkingNone,
		},
		{
			name:            "WithoutThinkSuffix_Gemini_ReturnsReasoningEffort",
			providerName:    "aihubmix",
			providerModelID: "gemini-3.1-pro-preview",
			want:            ThinkingReasoningEffort,
		},
		{
			name:            "WithoutThinkSuffix_GPT_ReturnsReasoningEffort",
			providerName:    "aihubmix",
			providerModelID: "gpt-5.4",
			want:            ThinkingReasoningEffort,
		},
		{
			name:            "WithoutThinkSuffix_DeepSeek_ReturnsReasoningEffort",
			providerName:    "aihubmix",
			providerModelID: "deepseek-v3.2",
			want:            ThinkingReasoningEffort,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferThinkingFormat(tc.providerName, tc.providerModelID)
			if got != tc.want {
				t.Errorf("inferThinkingFormat(%q, %q) = %q, want %q",
					tc.providerName, tc.providerModelID, got, tc.want)
			}
		})
	}
}

func Test_InferThinkingFormat_Dmxapi_Gemini_StillNative(t *testing.T) {
	got := inferThinkingFormat("dmxapi", "gemini-3-pro-preview")
	if got != ThinkingGemini {
		t.Errorf("inferThinkingFormat(%q, %q) = %q, want %q",
			"dmxapi", "gemini-3-pro-preview", got, ThinkingGemini)
	}
}

func Test_InferThinkingFormat_Dmxapi_Claude_StillThinkingNone(t *testing.T) {
	got := inferThinkingFormat("dmxapi", "claude-sonnet-4-6-thinking")
	if got != ThinkingNone {
		t.Errorf("inferThinkingFormat(%q, %q) = %q, want %q",
			"dmxapi", "claude-sonnet-4-6-thinking", got, ThinkingNone)
	}
}
