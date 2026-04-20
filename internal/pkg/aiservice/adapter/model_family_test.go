package adapter

import "testing"

func TestInferModelFamily(t *testing.T) {
	cases := []struct {
		name            string
		providerModelID string
		want            ModelFamily
	}{
		// OpenAI reasoning family
		{"gpt-5 exact", "gpt-5", ModelFamilyOpenAIReasoning},
		{"gpt-5.4 dot variant", "gpt-5.4", ModelFamilyOpenAIReasoning},
		{"gpt-5-preview", "gpt-5-preview", ModelFamilyOpenAIReasoning},
		{"gpt-5-2026-03-05", "gpt-5-2026-03-05", ModelFamilyOpenAIReasoning},
		{"gpt-50 collision probe", "gpt-50-anything", ModelFamilyGeneric},
		{"o1 exact", "o1", ModelFamilyOpenAIReasoning},
		{"o1-preview", "o1-preview", ModelFamilyOpenAIReasoning},
		{"o3 exact", "o3", ModelFamilyOpenAIReasoning},
		{"o3-mini", "o3-mini", ModelFamilyOpenAIReasoning},
		{"o4-turbo", "o4-turbo", ModelFamilyOpenAIReasoning},
		{"o10 collision probe", "o10-anything", ModelFamilyGeneric},
		// Claude family
		{"claude base", "claude-sonnet-4-6", ModelFamilyClaude},
		{"claude-think variant", "claude-sonnet-4-6-think", ModelFamilyClaudeThinkingSlug},
		{"claude haiku think", "claude-haiku-4-6-think", ModelFamilyClaudeThinkingSlug},
		{"claude bare prefix", "claude-", ModelFamilyClaude},
		{"claude think with trailing suffix", "claude-sonnet-4-6-think-preview", ModelFamilyClaude},
		// Gemini family (note: -think suffix on gemini does NOT match claude-thinking-suffix)
		{"gemini base", "gemini-3.1-pro-preview", ModelFamilyGemini},
		{"gemini think fake", "gemini-3.1-pro-preview-think", ModelFamilyGemini},
		// DeepSeek family
		{"deepseek base", "deepseek-v3.2", ModelFamilyDeepSeek},
		{"deepseek think", "deepseek-v3.2-think", ModelFamilyDeepSeek},
		// Generic / edge
		{"qwen", "qwen-turbo", ModelFamilyGeneric},
		{"text-embedding-v4", "text-embedding-v4", ModelFamilyGeneric},
		{"empty string", "", ModelFamilyGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InferModelFamily(tc.providerModelID)
			if got != tc.want {
				t.Errorf("InferModelFamily(%q) = %v, want %v", tc.providerModelID, got, tc.want)
			}
		})
	}
}
