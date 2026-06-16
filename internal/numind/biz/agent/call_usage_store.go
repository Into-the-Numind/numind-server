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
// NOTE: BOTH aiserviceAdapter.Generate and aiserviceAdapter.Stream stash usage
// (keyed by call-id) so the in-memory MaxCredits guardrail accrues on the production
// streaming path too — each tool-deciding turn's usage is consumed by the following
// PostToolCall. The cap still cannot stop the FINAL answer turn (no subsequent tool
// call to gate on), but it now bounds runaway multi-turn loops, which is the risk it
// exists for. The authoritative three-pool credit deduction (via aiservice
// ContextBudgetCredits, reconciled on the final chunk) is separate and unaffected.
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
