package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/aiservice"
)

// TestConcatMessages_ToolCallArgsDeltaExtra reproduces the dev regression
// (agent_run 177, 2026-06-18): BE-1 smuggled *aiservice.ToolCallArgsDelta through
// schema.Message.Extra. When a tool's arguments stream across MULTIPLE chunks (the
// common case — e.g. file_read's url, run_python's code), Eino's tools node
// concatenates the streamed assistant frames via schema.ConcatMessages, which in
// turn concats the Extra map key-by-key. Without a registered concat func for
// *aiservice.ToolCallArgsDelta, two non-zero values under the same key make Eino's
// ConcatSliceValue fail with "cannot concat multiple non-zero value of type
// *aiservice.ToolCallArgsDelta" → [NodeRunError] run node[tools] pre processor fail
// → terminal_reason=model_error → user sees "服务暂时不可用".
//
// BEFORE the fix this test FAILS at ConcatMessages (Bug-from-Customer repro, rule 11).
// AFTER the fix (init() registers a concat func) it passes and the merged Extra
// carries the reconstructed full arguments.
func TestConcatMessages_ToolCallArgsDeltaExtra(t *testing.T) {
	frame := func(delta string) *schema.Message {
		return &schema.Message{
			Role: schema.Assistant,
			Extra: map[string]any{
				extraKeyToolCallArgsDelta: &aiservice.ToolCallArgsDelta{
					ToolCallID:   "tc-1",
					FunctionName: "file_read",
					ArgsDelta:    delta,
				},
			},
		}
	}
	// Two frames carry the args-delta side-channel — exactly what happens when the
	// provider streams function.arguments in more than one chunk.
	msgs := []*schema.Message{
		frame(`{"url":"htt`),
		frame(`ps://x/a.docx"}`),
	}

	merged, err := schema.ConcatMessages(msgs)
	if err != nil {
		t.Fatalf("ConcatMessages failed — regression: no concat func registered for "+
			"*aiservice.ToolCallArgsDelta (this is the dev run-killer): %v", err)
	}

	ad, _ := merged.Extra[extraKeyToolCallArgsDelta].(*aiservice.ToolCallArgsDelta)
	if ad == nil {
		t.Fatalf("merged Extra missing *aiservice.ToolCallArgsDelta")
	}
	if got, want := ad.ArgsDelta, `{"url":"https://x/a.docx"}`; got != want {
		t.Errorf("merged ArgsDelta = %q, want concatenated full args %q", got, want)
	}
	if ad.FunctionName != "file_read" || ad.ToolCallID != "tc-1" {
		t.Errorf("merged identity lost: FunctionName=%q ToolCallID=%q", ad.FunctionName, ad.ToolCallID)
	}
}
