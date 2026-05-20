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
// max_output_tokens failure (blueprint §4.1.6 Step 1). Once at or above
// EscalatedMaxTokens we cap at EscalatedMaxTokens — the recovery stage in
// handleMaxOutputError does not call this function; it preserves the current
// max instead, letting the LLM run out its full output budget.
func EscalateMaxTokens(current int) int {
	if current < EscalatedMaxTokens {
		return EscalatedMaxTokens
	}
	return EscalatedMaxTokens
}
