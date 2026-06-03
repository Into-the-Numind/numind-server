package agent

import "sync"

// NewCallUsageStore returns a process-level callID→Usage map shared between the
// per-run aiserviceAdapters (which Store the real token Usage after each LLM
// call, keyed by ctx call-id) and budgetgate (which reads it in PostToolCall to
// feed BudgetTracker.RecordUsage → the MaxCredits dimension).
//
// agent-mode-billing T6: the previous design constructed adapters with a nil
// usageStore in runner.go/runner_runstream.go, so budgetgate's WithUsageLookup
// never saw any usage (MaxCredits stuck at 0). A single shared store, injected
// into both the runner (via WithCallUsageStore) and WrapHooks (via
// WithUsageLookup over NewCallUsageLookup), closes the loop.
func NewCallUsageStore() *sync.Map { return &sync.Map{} }

// CallUsageLookup adapts a shared *sync.Map to budgetgate.UsageLookupable.
// LookupUsage is read-and-delete (LoadAndDelete) so the map stays bounded to
// in-flight calls — each callID is stashed once by the adapter and consumed once
// by PostToolCall, avoiding unbounded growth.
//
// NOTE (streaming MaxCredits limitation): only aiserviceAdapter.Generate stashes
// usage; aiserviceAdapter.Stream (the production SSE path's final-answer turn)
// does not. So the in-memory MaxCredits guardrail counts tool-deciding Generate
// turns but undercounts a streaming run's final-answer tokens. This affects ONLY
// the in-memory safety cap — the authoritative three-pool credit deduction
// (T1-T5, via aiservice ContextBudgetCredits which reconciles ChatStream on the
// final chunk) is unaffected. MaxTurns works for all paths (RecordStep fires in
// PreToolCall regardless). Completing streaming MaxCredits = a follow-up.
type CallUsageLookup struct{ m *sync.Map }

// NewCallUsageLookup wraps the shared store as a UsageLookupable for WrapHooks.
func NewCallUsageLookup(m *sync.Map) *CallUsageLookup { return &CallUsageLookup{m: m} }

// LookupUsage returns the Usage stashed for callID and removes it.
func (c *CallUsageLookup) LookupUsage(callID string) (Usage, bool) {
	if c == nil || c.m == nil {
		return Usage{}, false
	}
	v, ok := c.m.LoadAndDelete(callID)
	if !ok {
		return Usage{}, false
	}
	u, _ := v.(Usage)
	return u, true
}
