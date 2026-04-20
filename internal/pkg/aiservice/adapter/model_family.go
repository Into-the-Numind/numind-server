package adapter

import "strings"

// ModelFamily categorises a provider_model_id for per-family dispatch in the adapter layer.
// Exported so adapter tests can assert the classification.
type ModelFamily string

const (
	ModelFamilyOpenAIReasoning    ModelFamily = "openai-reasoning"
	ModelFamilyClaudeThinkingSlug ModelFamily = "claude-thinking-suffix"
	ModelFamilyClaude             ModelFamily = "claude"
	ModelFamilyGemini             ModelFamily = "gemini"
	ModelFamilyDeepSeek           ModelFamily = "deepseek"
	ModelFamilyGeneric            ModelFamily = "generic"
)

// InferModelFamily dispatches based on provider_model_id prefix/suffix.
//
// Matching order:
//  1. Claude -think suffix variant (exact Claude + suffix check) — e.g. "claude-sonnet-4-6-think"
//     Per T2 protocol audit: only Claude and DeepSeek recognise the -think suffix at AiHubMix;
//     however the Claude variant has special semantics (temp=1 server-forced) that we tag here.
//     DeepSeek -think falls through to generic deepseek (no adapter-side special handling needed).
//  2. OpenAI reasoning family — strict enumeration of gpt-5, gpt-5-, gpt-5., o1, o1-, o3, o3-, o4, o4-.
//     Strict enumeration intentionally rejects "gpt-50-xxx" and "o10-xxx" collision probes
//     (see test file).
//  3. Generic family prefixes (claude-, gemini-, deepseek-).
//  4. Fallback Generic.
func InferModelFamily(providerModelID string) ModelFamily {
	// Claude -think suffix variant (highest priority — must precede claude- prefix check)
	if strings.HasPrefix(providerModelID, "claude-") && strings.HasSuffix(providerModelID, "-think") {
		return ModelFamilyClaudeThinkingSlug
	}

	// OpenAI reasoning family — strict enumeration
	switch {
	case providerModelID == "gpt-5",
		strings.HasPrefix(providerModelID, "gpt-5-"),
		strings.HasPrefix(providerModelID, "gpt-5."),
		providerModelID == "o1",
		strings.HasPrefix(providerModelID, "o1-"),
		providerModelID == "o3",
		strings.HasPrefix(providerModelID, "o3-"),
		providerModelID == "o4",
		strings.HasPrefix(providerModelID, "o4-"):
		return ModelFamilyOpenAIReasoning
	}

	// Generic family prefixes
	switch {
	case strings.HasPrefix(providerModelID, "claude-"):
		return ModelFamilyClaude
	case strings.HasPrefix(providerModelID, "gemini-"):
		return ModelFamilyGemini
	case strings.HasPrefix(providerModelID, "deepseek-"):
		return ModelFamilyDeepSeek
	default:
		return ModelFamilyGeneric
	}
}
