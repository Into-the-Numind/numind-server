package budget

import "errors"

// Sentinel errors used inside biz/budget for errors.Is matching.
// The global errno.ErrAdminTestExhausted (HTTP-aware) is the public-facing
// errno; ErrAdminTestExhausted (this package) is the internal sentinel.
// credit_service.ReserveAgentTest bridges via errors.Is.
var (
	ErrAdminTestExhausted = errors.New("admin_test pool exhausted for parent user this month")
	ErrBudgetExceeded     = errors.New("budget tracker dimension exceeded")
)
