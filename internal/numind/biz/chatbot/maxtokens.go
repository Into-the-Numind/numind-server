package chatbot

import "context"

// chatbotMaxOutputTokensFallback is the per-turn output cap the chatbot stream
// requests so a thinking model's reasoning cannot exhaust the provider default
// budget before the answer is emitted. Without an explicit max_tokens, agnes-2.0-flash
// (thinking via enable_thinking_kwarg) emits reasoning first, runs out of the low
// provider-default budget (finish_reason=length), and the whole answer is stranded
// in reasoning_content with empty content (dev chatbot 10 session 223: completion 972
// tokens all thinking, 0 content). 8192 is ample for a chatbot answer + reasoning.
const chatbotMaxOutputTokensFallback = 8192

// resolveChatbotMaxTokens maps a declared capability max_output_tokens to the value
// requested: declared value verbatim, 0/unset falls back to the safe default. Pure
// (no I/O) so it is unit-tested directly.
func resolveChatbotMaxTokens(declared int) int {
	if declared <= 0 {
		return chatbotMaxOutputTokensFallback
	}
	return declared
}

// chatbotMaxOutputTokens returns the output cap for the chatbot stream.
//
// NOTE: this prod release line predates aiservice.ResolveTask and the registry route
// capability max_output_tokens field, so the model's declared limit cannot be read
// here. Use the safe fallback unconditionally — 8192 prevents the provider default
// from truncating a thinking model's answer into reasoning_content. (develop's version
// reads the model's real declared limit via ResolveTask; this fixed-fallback variant
// is superseded when retrieval/rerank land that machinery on the release line.)
func chatbotMaxOutputTokens(ctx context.Context) int {
	return chatbotMaxOutputTokensFallback
}
