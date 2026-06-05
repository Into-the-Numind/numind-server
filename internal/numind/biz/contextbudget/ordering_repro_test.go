// Package contextbudget — ordering_repro_test.go
//
// Customer-reported bug reproduction (NDF Rule 11).
//
// Symptom: running a multi-step AI SOP, while executing step 3 the model
// produces the content that step 2 should have produced (steps get crossed).
//
// Root cause exercised here: when context compression triggers, applyPlan
// appends the history-summary fragment to the END of the fragment list (after
// the current-step instruction), and Prepare renders fragments in slice order
// without sorting by Order. The history summary therefore becomes the LAST
// message the LLM sees, so it continues from step 2 instead of step 3.
//
// This test drives Biz.Prepare through the real compression path with a tiny
// budget and asserts the rendered message order: the current-step instruction
// must be last, and the history summary must come before it.
package contextbudget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// summarySentinel is a recognizable marker placed in the compressed summary so
// the test can locate it in the rendered message slice.
const summarySentinel = "HISTORY_SUMMARY_SENTINEL_步骤12摘要"

// sentinelSummaryCompressor returns a summary fragment carrying summarySentinel.
// It deliberately sets Order=0 to prove the fix does not rely on the compressor
// supplying a correct Order (applyPlan must derive it from the replaced fragments).
type sentinelSummaryCompressor struct {
	called bool
}

func (c *sentinelSummaryCompressor) Compress(_ context.Context, _ []contextbudget.ContextFragment, _ int) (contextbudget.ContextFragment, error) {
	c.called = true
	return contextbudget.ContextFragment{
		ID:          "summary-sentinel",
		Role:        contextbudget.RoleDurable,
		Source:      contextbudget.SourceInternal,
		ContentType: contextbudget.ContentSummary,
		Content:     summarySentinel,
		Importance:  5,
		Order:       0, // intentionally wrong — Fix B must override to min(replaced)
	}, nil
}

// TestPrepare_CompressionKeepsCurrentInputLast reproduces the SOP step-crossing
// bug at the shared contextbudget layer (also covers chatbot, same middleware).
func TestPrepare_CompressionKeepsCurrentInputLast(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Tiny context window forces the planner to summarise the durable history.
	// safe_input_budget = floor((150 - 20 - 5) * 0.85) = 106 tokens.
	// Each history fragment costs ~200 zh chars * 0.6 (sampleProfile zh rate) +
	// 2 (fragment_overhead) + 4 (message_overhead) ≈ 126 tokens — alone exceeds
	// the 106 safe budget, so compression is guaranteed even if overhead shifts a
	// few tokens. The require(sc.called) below hard-gates this precondition.
	policy := &model.ContextBudgetPolicy{
		Operation:            "sop_run",
		ReservedOutputTokens: 20,
		SafeRatio:            0.85,
		FixedOverheadTokens:  5,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              1,
		IsActive:             true,
	}
	require.NoError(t, db.Create(policy).Error)
	if policy.ChargeUser { // GORM default:true gotcha
		db.Model(policy).UpdateColumn("charge_user", false)
		policy.ChargeUser = false
	}
	sampleProfile(db)

	sc := &sentinelSummaryCompressor{}
	biz := New(cbStore, Options{Compressor: sc, Clock: time.Now, Logger: noopLogger{}})

	const currentInput = "STEP3_CURRENT_INSTRUCTION_第三步指令"

	// Fragment layout mirrors a multi-step SOP gateway turn:
	//   system (immutable, never compressed)
	//   step-1 + step-2 history (durable, summarisable) — large, triggers compression
	//   current step-3 instruction (critical, never compressed) — must render last
	fragments := []contextbudget.ContextFragment{
		{
			ID: "sys", Role: contextbudget.RoleImmutable, Source: contextbudget.SourceSystem,
			ContentType: contextbudget.ContentText, Content: "你是一个 SOP 执行助手", Importance: 10,
			Order: 0, Compressibility: contextbudget.CompressNone, Critical: true,
		},
		{
			ID: "hist-1", Role: contextbudget.RoleDurable, Source: contextbudget.SourceUser,
			ContentType: contextbudget.ContentText, Content: strings.Repeat("甲", 200), Importance: 5,
			Order: 1, Compressibility: contextbudget.CompressSummarize,
		},
		{
			ID: "hist-2", Role: contextbudget.RoleDurable, Source: contextbudget.SourceAssistant,
			ContentType: contextbudget.ContentText, Content: strings.Repeat("乙", 200), Importance: 5,
			Order: 2, Compressibility: contextbudget.CompressSummarize,
		},
		{
			ID: "current", Role: contextbudget.RoleRecent, Source: contextbudget.SourceUser,
			ContentType: contextbudget.ContentText, Content: currentInput, Importance: 9,
			Order: 3, Compressibility: contextbudget.CompressNone, Critical: true,
		},
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "sop_run",
		UserID:          0,
		Route:           sampleRoute(150, 50),
		Fragments:       fragments,
		ContextWindow:   150,
		MaxOutputTokens: 50,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Precondition: compression actually happened (otherwise the bug can't manifest).
	require.True(t, sc.called, "compression must have triggered for this reproduction to be meaningful")
	require.NotEmpty(t, result.Messages, "rendered messages must not be empty")

	// Locate the current instruction and the summary in the rendered messages.
	summaryIdx, curIdx := -1, -1
	for i, m := range result.Messages {
		if strings.Contains(m.Content.Text, summarySentinel) {
			summaryIdx = i
		}
		if m.Content.Text == currentInput {
			curIdx = i
		}
	}
	require.GreaterOrEqual(t, summaryIdx, 0, "history summary must be present in rendered messages")
	require.GreaterOrEqual(t, curIdx, 0, "current step instruction must be present in rendered messages")

	// The bug: summary lands AFTER the current instruction (summaryIdx > curIdx).
	lastIdx := len(result.Messages) - 1
	assert.Equal(t, currentInput, result.Messages[lastIdx].Content.Text,
		"current step instruction MUST be the last message the LLM sees")
	assert.Less(t, summaryIdx, curIdx,
		"history summary must come BEFORE the current step instruction (bug renders it after → model continues prior step)")
}

// TestMinFragmentOrder locks the contract used by applyPlan to anchor a summary:
// the smallest Order among replaced fragments, or 0 for an empty slice.
func TestMinFragmentOrder(t *testing.T) {
	assert.Equal(t, 0, minFragmentOrder(nil), "empty slice → 0")
	assert.Equal(t, 0, minFragmentOrder([]contextbudget.ContextFragment{}), "empty slice → 0")
	assert.Equal(t, 2, minFragmentOrder([]contextbudget.ContextFragment{
		{Order: 5}, {Order: 2}, {Order: 9},
	}), "min of {5,2,9} → 2")
	assert.Equal(t, 7, minFragmentOrder([]contextbudget.ContextFragment{{Order: 7}}), "single element → its order")
}
