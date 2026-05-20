package budget

import "time"

// AdminTestStatus is the current-month state of a parent user's admin_test grant.
// Returned by AdminTestConsumer.Status; serialized by credit_service.GetBalance
// as AdminTestPool field.
type AdminTestStatus struct {
	Granted      int64
	Used         int64
	Remaining    int64
	PeriodStart  time.Time
	PeriodEnd    time.Time
	DaysToExpire int
}

// BudgetExceededDetail describes which dimension tripped CanProceed and the
// numeric used/limit at the time. Written into agent_run.terminal_metadata
// as JSON for #11 student-ux to render.
type BudgetExceededDetail struct {
	Dimension Dimension
	Used      int64
	Limit     int64
}

// Snapshot is a runtime view of a Run's BudgetTracker state.
// Returned by BudgetTracker.Snapshot for audit / debug.
type Snapshot struct {
	Turns        int
	Credits      int64
	Elapsed      time.Duration
	DailyCredits int64
	Limits       Limits
	StartedAt    time.Time
}

// Limits is the 4-dimensional budget configuration passed to BudgetTracker.Start.
type Limits struct {
	MaxTurns        int           // default 50; agent_definition.max_turns_per_run not yet introduced (v1)
	MaxCredits      int64         // from agent_definition.credit_cap_per_session (×1, no coefficient in v1)
	MaxWallTime     time.Duration // default 300s
	MaxDailyCredits int64         // from agent_definition.daily_credit_cap or default 2000
}

// Default constants for admin_test pool.
const (
	// DefaultAdminTestGrant 与 model.CreditAdminTestGrant.GrantedAmount 字段类型 (uint32) 对齐。
	DefaultAdminTestGrant uint32 = 5000
	// DefaultAdminTestGrantInt64 — int64 形式给 API / Status 返回用。
	DefaultAdminTestGrantInt64 int64 = 5000
)
