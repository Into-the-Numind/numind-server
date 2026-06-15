package chatbot

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
)

// chatbotMaxOutputTokensFallback is the per-turn output cap used ONLY when the
// resolved chatbot model declares no max_output_tokens in its capability_json.
// A configured model uses its declared value verbatim — those are the models' real
// limits and must NOT be artificially capped.
//
// Background: a thinking model (agnes-2.0-flash, activated via
// chat_template_kwargs.enable_thinking) emits its reasoning FIRST and the answer
// LAST. With no explicit max_tokens the request runs at the provider's low default;
// long reasoning exhausts that budget and generation stops (finish_reason=length)
// before the answer is emitted — the whole answer is stranded in reasoning_content
// and content comes back empty (observed: dev chatbot 10 session 223, completion
// 972 tokens all thinking, 0 content). Sending an explicit max_tokens gives room
// for both reasoning AND the answer. Same root cause as the agent path fix
// (resolveAgentMaxTokens); the chatbot stream path was never covered.
const chatbotMaxOutputTokensFallback = 8192

// resolveChatbotMaxTokens maps a model's declared capability_json.max_output_tokens
// to the value the chatbot stream requests. A declared value is used verbatim
// (respect the real model limit); 0/unset falls back to a safe default. No
// artificial ceiling — capping below a model's real limit would truncate long
// answers. Pure (no I/O) so it is unit-tested directly.
func resolveChatbotMaxTokens(declared int) int {
	if declared <= 0 {
		return chatbotMaxOutputTokensFallback
	}
	return declared
}

// chatbotMaxOutputTokens resolves the ChatbotStream task's model and returns the
// output cap to request (the model's declared max_output_tokens, or the fallback).
// On ANY resolution failure it returns the fallback, so the chatbot always sends an
// explicit, adequate cap instead of relying on the provider default that strands a
// thinking model's answer in reasoning_content with empty content. Route resolution
// is registry-cached, so the extra lookup is cheap.
func chatbotMaxOutputTokens(ctx context.Context) int {
	route, err := aiservice.ResolveTask(ctx, profile.ChatbotStream)
	if err != nil || route == nil {
		log.Warnw("chatbotMaxOutputTokens: could not resolve chatbot model; using fallback max_tokens",
			"task_id", profile.ChatbotStream, "fallback", chatbotMaxOutputTokensFallback,
			"error", err, "route_nil", route == nil)
		return chatbotMaxOutputTokensFallback
	}
	return resolveChatbotMaxTokens(route.Capability.MaxOutputTokens)
}
