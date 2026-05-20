package compact

// Token caps for the LLM max_output_tokens recovery chain (blueprint §4.1.6).
//
//	DefaultMaxTokens   = 8192  — initial LLM request cap
//	EscalatedMaxTokens = 65536 — escalate target on first max_tokens stop
const (
	DefaultMaxTokens   = 8192
	EscalatedMaxTokens = 65536
)

// EscalateMaxTokens raises max_tokens to EscalatedMaxTokens on the first
// max_output_tokens failure (blueprint §4.1.6 Step 1). Both branches
// intentionally return EscalatedMaxTokens — escalation always lands at the
// cap, never higher. The two-branch shape stays so future tuning can
// distinguish "needed escalation" from "already at cap" without changing
// callers. The recovery stage in handleMaxOutputError does not call this
// function; it preserves currentMaxTokens to let the LLM exhaust its budget.
func EscalateMaxTokens(current int) int {
	if current < EscalatedMaxTokens {
		return EscalatedMaxTokens
	}
	return EscalatedMaxTokens
}
