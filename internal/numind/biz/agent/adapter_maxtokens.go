package agent

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
)

// agentMaxOutputTokensFallback is the per-turn output cap used ONLY when a model
// declares no max_output_tokens in its capability_json. A configured model uses its
// declared value VERBATIM — those are the models' real limits (deepseek-v4-pro 384k,
// Claude 128k, gpt 128k, gemini 64k) and must NOT be artificially capped: a too-low
// cap would truncate a long report, the very failure this mechanism exists to prevent.
//
// Background: a thinking model (deepseek-v4-pro) emits its reasoning FIRST and the
// tool call LAST; with no explicit max_tokens the request runs at the provider's low
// default, the reasoning exhausts that budget, and the trailing tool call is truncated
// mid-JSON ("unexpected end of JSON input") — dev run 133. Setting an explicit
// max_tokens to the model's real limit (Claude Code's technique) gives it room to
// finish both the reasoning AND a long answer.
const agentMaxOutputTokensFallback = 8192

// resolveAgentMaxTokens maps a model's declared capability_json.max_output_tokens to
// the value the agent requests. A declared value is used VERBATIM (respect the real
// model limit, high or low); 0/unset falls back to a safe default. No artificial
// ceiling — capping below a model's real limit would truncate long outputs. Pure
// (no I/O) so it is unit-tested directly.
func resolveAgentMaxTokens(declared int) int {
	if declared <= 0 {
		return agentMaxOutputTokensFallback
	}
	return declared
}

// agentMaxOutputTokens resolves the agent-run task's model and returns the output cap
// to request (the model's declared max_output_tokens, or the fallback). On ANY
// resolution failure it returns the fallback, so the agent always sends an explicit,
// adequate cap instead of relying on the provider default that truncated dev run 133.
// Called once per run at adapter construction (route resolution is registry-cached,
// so the extra lookup is cheap). ctx is forwarded for Langfuse trace propagation
// only; routing is task-based, not ctx-based.
func agentMaxOutputTokens(ctx context.Context) int {
	route, err := aiservice.ResolveTask(ctx, profile.AgentRun)
	if err != nil || route == nil {
		log.Warnw("agentMaxOutputTokens: could not resolve agent model; using fallback max_tokens",
			"task_id", profile.AgentRun, "fallback", agentMaxOutputTokensFallback,
			"error", err, "route_nil", route == nil)
		return agentMaxOutputTokensFallback
	}
	return resolveAgentMaxTokens(route.Capability.MaxOutputTokens)
}
