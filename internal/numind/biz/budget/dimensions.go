package budget

import (
	"time"

	"numind-server/internal/pkg/model"
)

// Dimension labels which axis of the 4-dim budget tripped a CanProceed=exceeded.
type Dimension string

const (
	DimMaxTurns        Dimension = "max_turns"
	DimMaxWallTime     Dimension = "max_wall_time"
	DimMaxDailyCredits Dimension = "max_daily_credits"
)

// DefaultLimits returns the budget defaults used when agent_definition fields are zero/nil.
//
// Values:
//   - MaxTurns        = 300
//   - MaxWallTime     = 30m
//   - MaxDailyCredits = 200000
//
// 注：per-session credit cap（旧 MaxCredits / agent_definition.credit_cap_per_session）
// 已整体移除（2026-06-17 agent-credit-cap-redesign）。单次 run 的成本不再单独设上限，
// 只受 MaxTurns（卡死保护）+ MaxWallTime（墙钟）+ MaxDailyCredits（用户日总额）约束。
//
// MaxWallTime was 900s, but production-like tagging / multi-artifact runs can
// legitimately exceed 15 min. 30m gives real long-running agent sessions
// headroom while still bounding stuck work.
//
// MaxTurns was 50, but a research-heavy run (web_search/web_fetch in parallel
// batches) burned through that budget before reaching create_html / invoke_skill
// (dev agent_run 76 on 2026-05-29). Raised 50 → 100, then 100 → 300, so the
// agent has actual room for long tagging / research + artifact-generation
// chains in one pass. Cost remains
// bounded by MaxDailyCredits (200000); MaxTurns is the stuck-loop guard, and the
// per-session cap is removed. Eino's per-graph MaxStep (runner.go /
// runner_runstream.go) is kept > this value (currently 360) so termination
// always flows through this budget gate.
func DefaultLimits() Limits {
	return Limits{
		MaxTurns:        300,
		MaxWallTime:     30 * time.Minute,
		MaxDailyCredits: 200000,
	}
}

// LimitsFromAgentDef reads limits from agent_definition row fields.
// nil / zero values fall back to DefaultLimits — callers don't need to handle.
//
// 字段类型注意（model.AgentDefinition #5 落地）：
//   - DailyCreditCap *uint （需要 nil-pointer 守护后 deref）
//
// per-session credit cap 已移除（2026-06-17）；只剩 daily cap 可由 agent_definition 覆盖。
// MaxTurnsPerRun 字段 v1 未引入 agent_definition；走 DefaultLimits.MaxTurns。
func LimitsFromAgentDef(ad *model.AgentDefinition) Limits {
	d := DefaultLimits()
	if ad == nil {
		return d
	}
	if ad.DailyCreditCap != nil && *ad.DailyCreditCap > 0 {
		d.MaxDailyCredits = int64(*ad.DailyCreditCap)
	}
	return d
}
