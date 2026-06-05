// Package sop — context_ordering_repro_test.go
//
// Customer-reported bug reproduction (NDF Rule 11), SOP producer side.
//
// buildSOPGatewayFragments builds the current-step user input via
// NewCriticalUserFragment, which (pre-fix) does not set Order, leaving it at the
// zero value. Historical fragments get Order=i (their message index), so the
// current input ends up with a LOWER Order than the history — meaning a stable
// sort by Order would place the current step instruction BEFORE the history
// instead of last. This is the producer-level half of the step-crossing bug.
package sop

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/contextbudget"
)

// TestBuildSOPGatewayFragments_CurrentInputHasHighestOrder asserts that the
// current-step input fragment carries an Order strictly greater than every
// history fragment, so that ordering by Order renders it last.
func TestBuildSOPGatewayFragments_CurrentInputHasHighestOrder(t *testing.T) {
	msgs := []LLMMessage{
		{Role: "system", Content: "系统指令"},
		{Role: "user", Content: "第一步输入"},
		{Role: "assistant", Content: "第一步输出"},
		{Role: "user", Content: "第二步输入"},
		{Role: "assistant", Content: "第二步输出"},
		{Role: "user", Content: "第三步当前输入"}, // current turn — last user message
	}

	frags := buildSOPGatewayFragments(msgs)
	require.NotEmpty(t, frags, "fragments must not be empty")

	// Identify the single critical user fragment (the current step input) and the
	// maximum Order among all other (history/system) fragments.
	var current *contextbudget.ContextFragment
	maxHistOrder := 0
	haveHist := false
	for i := range frags {
		f := frags[i]
		if f.Critical && f.Source == contextbudget.SourceUser {
			current = &frags[i]
			continue
		}
		if !haveHist || f.Order > maxHistOrder {
			maxHistOrder = f.Order
			haveHist = true
		}
	}

	require.NotNil(t, current, "current step critical user fragment must exist")
	require.True(t, haveHist, "history fragments must exist")
	assert.Greater(t, current.Order, maxHistOrder,
		"current step input Order (%d) must exceed every history fragment Order (max %d) so it renders last",
		current.Order, maxHistOrder)
}
