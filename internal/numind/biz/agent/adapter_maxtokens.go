package agent

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
)

// Agent output-token bounds. A thinking model (deepseek-v4-pro) emits its reasoning
// FIRST and the tool call LAST; with no explicit cap the request runs at the
// provider's default, the reasoning can exhaust that budget, and the trailing tool
// call is truncated mid-JSON ("unexpected end of JSON input") — dev run 133. We set
// an explicit, generous cap (the technique Claude Code uses) so the model always has
// room to finish the tool call.
//
//   - Ceiling 64000 mirrors Claude Code's escalated max: far more than any agent turn
//     needs (reasoning + a tool call is a few thousand tokens) and safe across
//     providers, while not blindly forwarding a model's huge declared max (e.g.
//     deepseek-v4-pro declares 384000) that a gateway might reject.
//   - Floor 8192 guarantees adequate room even when the model declares no
//     max_output_tokens (capability_json omits it).
const (
	agentMaxOutputTokensFloor   = 8192
	agentMaxOutputTokensCeiling = 64000
)

// clampAgentMaxTokens maps a model's declared capability_json.max_output_tokens to
// the value the agent actually requests: 0/unset → floor; otherwise clamped into
// [floor, ceiling]. Pure (no I/O) so it is unit-tested directly.
func clampAgentMaxTokens(declared int) int {
	if declared <= 0 {
		return agentMaxOutputTokensFloor
	}
	if declared > agentMaxOutputTokensCeiling {
		return agentMaxOutputTokensCeiling
	}
	if declared < agentMaxOutputTokensFloor {
		return agentMaxOutputTokensFloor
	}
	return declared
}

// agentMaxOutputTokens resolves the agent-run task's model and returns the output
// cap to request (clamped). On ANY resolution failure it returns the floor, so the
// agent always sends an explicit, adequate cap instead of relying on the provider
// default that truncated dev run 133. Called once per run at adapter construction
// (route resolution is registry-cached, so the extra lookup is cheap). ctx is
// forwarded for Langfuse trace propagation only; routing is task-based, not ctx-based.
func agentMaxOutputTokens(ctx context.Context) int {
	route, err := aiservice.ResolveTask(ctx, profile.AgentRun)
	if err != nil || route == nil {
		log.Warnw("agentMaxOutputTokens: could not resolve agent model; using floor max_tokens",
			"task_id", profile.AgentRun, "floor", agentMaxOutputTokensFloor,
			"error", err, "route_nil", route == nil)
		return agentMaxOutputTokensFloor
	}
	return clampAgentMaxTokens(route.Capability.MaxOutputTokens)
}
