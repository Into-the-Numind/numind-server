package agent

import "testing"

// T6 (agent-mode-billing): the shared callID→Usage store. Adapter writes; the
// budgetgate-facing lookup reads-and-deletes (bounded growth).
func TestCallUsageLookup_ReadAndDelete(t *testing.T) {
	m := NewCallUsageStore()
	m.Store("call-1", Usage{PromptTokens: 100, CompletionTokens: 50})

	lk := NewCallUsageLookup(m)
	u, ok := lk.LookupUsage("call-1")
	if !ok || u.PromptTokens != 100 || u.CompletionTokens != 50 {
		t.Fatalf("LookupUsage = %+v, %v", u, ok)
	}
	// Read-and-delete: a second lookup of the same call-id misses.
	if _, ok := lk.LookupUsage("call-1"); ok {
		t.Error("call-id should be deleted after first read")
	}
	// Missing key.
	if _, ok := lk.LookupUsage("nope"); ok {
		t.Error("missing key should return false")
	}
	// nil-safe.
	var nilLk *CallUsageLookup
	if _, ok := nilLk.LookupUsage("x"); ok {
		t.Error("nil lookup should return false")
	}
}
