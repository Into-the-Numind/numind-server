package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMemoryKind_Valid covers the Valid(layer) method for both L1 and L2.
func TestMemoryKind_Valid(t *testing.T) {
	tests := []struct {
		name  string
		kind  MemoryKind
		layer string
		want  bool
	}{
		// --- L1 cases ---
		{name: "L1_summary_valid", kind: KindSummary, layer: "L1", want: true},
		{name: "L1_learning_valid", kind: KindLearning, layer: "L1", want: true},
		{name: "L1_decision_valid", kind: KindDecision, layer: "L1", want: true},
		{name: "L1_issue_valid", kind: KindIssue, layer: "L1", want: true},
		{name: "L1_fact_valid", kind: KindFact, layer: "L1", want: true},
		{name: "L1_preference_valid", kind: KindPreference, layer: "L1", want: true},
		{name: "L1_unknown_invalid", kind: MemoryKind("bogus"), layer: "L1", want: false},
		{name: "L1_empty_invalid", kind: MemoryKind(""), layer: "L1", want: false},

		// --- L2 cases ---
		// summary is L1-only; all others are valid for L2
		{name: "L2_summary_invalid", kind: KindSummary, layer: "L2", want: false},
		{name: "L2_learning_valid", kind: KindLearning, layer: "L2", want: true},
		{name: "L2_decision_valid", kind: KindDecision, layer: "L2", want: true},
		{name: "L2_issue_valid", kind: KindIssue, layer: "L2", want: true},
		{name: "L2_fact_valid", kind: KindFact, layer: "L2", want: true},
		{name: "L2_preference_valid", kind: KindPreference, layer: "L2", want: true},
		{name: "L2_unknown_invalid", kind: MemoryKind("other"), layer: "L2", want: false},

		// --- edge cases: unknown layer string ---
		{name: "unknown_layer_summary_invalid", kind: KindSummary, layer: "L3", want: false},
		{name: "unknown_layer_learning_valid", kind: KindLearning, layer: "L3", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.kind.Valid(tc.layer)
			assert.Equal(t, tc.want, got)
		})
	}
}
