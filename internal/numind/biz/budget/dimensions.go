package budget

import (
	"time"

	"numind-server/internal/pkg/model"
)

// Dimension labels which axis of the 4-dim budget tripped a CanProceed=exceeded.
type Dimension string

const (
	DimMaxTurns        Dimension = "max_turns"
	DimMaxCredits      Dimension = "max_credits"
	DimMaxWallTime     Dimension = "max_wall_time"
	DimMaxDailyCredits Dimension = "max_daily_credits"
)

// DefaultLimits returns the 4-dim defaults used when agent_definition fields are zero/nil.
//
// Values:
//   - MaxTurns        = 50  (蓝本 §4.1.8 step limit default)
//   - MaxCredits      = 800 (蓝本 §6.6 default 配置上限)
//   - MaxWallTime     = 300s
//   - MaxDailyCredits = 2000
func DefaultLimits() Limits {
	return Limits{
		MaxTurns:        50,
		MaxCredits:      800,
		MaxWallTime:     300 * time.Second,
		MaxDailyCredits: 2000,
	}
}

// LimitsFromAgentDef reads limits from agent_definition row fields.
// nil / zero values fall back to DefaultLimits — callers don't need to handle.
//
// 字段类型注意（model.AgentDefinition #5 落地）：
//   - CreditCapPerSession *uint
//   - DailyCreditCap      *uint
//
// 都需要 nil-pointer 守护后 deref。
//
// MaxTurnsPerRun 字段 v1 未引入 agent_definition；走 DefaultLimits.MaxTurns。
func LimitsFromAgentDef(ad *model.AgentDefinition) Limits {
	d := DefaultLimits()
	if ad == nil {
		return d
	}
	if ad.CreditCapPerSession != nil && *ad.CreditCapPerSession > 0 {
		d.MaxCredits = int64(*ad.CreditCapPerSession)
	}
	if ad.DailyCreditCap != nil && *ad.DailyCreditCap > 0 {
		d.MaxDailyCredits = int64(*ad.DailyCreditCap)
	}
	return d
}
