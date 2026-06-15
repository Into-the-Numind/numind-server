package aiservice

// defaultMaxTokensFallback is the per-call output cap used ONLY when the resolved
// model declares no max_output_tokens in its capability_json. A configured model
// uses its declared value verbatim (those are the models' real limits, e.g. agnes
// 65500) — capping below the real limit would truncate long answers. Set high
// (60000) because every current model supports far more than a few thousand output
// tokens; a low fallback would truncate a long answer on any model that is missing
// its capability config.
const defaultMaxTokensFallback = 60000

// defaultMaxTokensFromCapability returns the per-call output cap the gateway sends
// when the caller left req.MaxTokens unset (0). The resolved model's admin-configured
// max_output_tokens (route.Capability.MaxOutputTokens) is used verbatim; 0/unset
// falls back to a safe floor.
//
// Why this exists: without an explicit max_tokens the request runs at the provider's
// low default. A thinking model (agnes-2.0-flash, activated via
// chat_template_kwargs.enable_thinking) emits its reasoning FIRST and the answer
// LAST; the low default is exhausted during reasoning (finish_reason=length) before
// the answer is emitted, so the whole answer is stranded in reasoning_content and
// content comes back empty (dev chatbot 10 session 223: completion 972 tokens all
// thinking, 0 content). Sending the model's real configured cap gives room for both
// reasoning AND the answer. Pure (no I/O) so it is unit-tested directly.
func defaultMaxTokensFromCapability(declared int) int {
	if declared <= 0 {
		return defaultMaxTokensFallback
	}
	return declared
}
