package agent

// followup3 FE-3 regression (dev, 2026-06-18): the live "writing code" box never
// appears. Root cause: BE-1 put the EventToolCallArgsDelta emit in consumeEinoStream,
// but the synthetic content-less args-delta frames flow through the REAL-TIME pump
// — streamScanToolCallChecker (the StreamToolCallChecker) — which drains the model
// output stream of EVERY step. consumeEinoStream only drains the END copy and never
// sees the intermediate tool-call turn, so its emit never fires in production.
//
// These tests assert the CHECKER emits EventToolCallArgsDelta for allowlisted tools.
// Before the fix the checker ignores the args-delta frames → TestEmits... FAILS.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
)

// TestStreamScanToolCallChecker_EmitsArgsDeltaForCodeTool feeds the checker a
// create_docx tool call streamed as args fragments (the content-less side-channel
// frames) and asserts it emits one EventToolCallArgsDelta per fragment, concatenating
// to the full arguments. This is the path that actually runs in production.
func TestStreamScanToolCallChecker_EmitsArgsDeltaForCodeTool(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](8)
	go func() {
		sw.Send(argsDeltaMsg("call_dx", "create_docx", `{"markdown":"# H`), nil)
		sw.Send(argsDeltaMsg("call_dx", "create_docx", `ello"}`), nil)
		sw.Send(&schema.Message{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "call_dx", Type: "function", Function: schema.FunctionCall{Name: "create_docx", Arguments: `{"markdown":"# Hello"}`}}},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		}, nil)
		sw.Close()
	}()

	ch := make(chan stream.Event, 64)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 1})

	isToolCall, err := streamScanToolCallChecker(ctx, sr)
	require.NoError(t, err)
	assert.True(t, isToolCall, "checker must still detect the tool call")
	close(ch)

	evs := collectEvents(ch)
	argsDeltas := allEventsOfType(evs, stream.EventToolCallArgsDelta)
	require.Len(t, argsDeltas, 2, "checker MUST emit one args-delta per fragment — "+
		"this is the real-time pump the frames flow through, not consumeEinoStream")

	var assembled string
	for _, ev := range argsDeltas {
		var p stream.ToolCallArgsDeltaPayload
		require.NoError(t, json.Unmarshal(ev.Data, &p))
		assert.Equal(t, "call_dx", p.ToolCallID)
		assert.Equal(t, "create_docx", p.FunctionName)
		assembled += p.ArgsDelta
	}
	assert.Equal(t, `{"markdown":"# Hello"}`, assembled, "concatenated args-delta must equal full arguments")
}

// TestStreamScanToolCallChecker_NoArgsDeltaForNonCodeTool asserts the allowlist gate
// holds in the checker too: file_read fragments must NOT produce any args-delta event.
func TestStreamScanToolCallChecker_NoArgsDeltaForNonCodeTool(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](8)
	go func() {
		sw.Send(argsDeltaMsg("call_fr", "file_read", `{"url":"htt`), nil)
		sw.Send(argsDeltaMsg("call_fr", "file_read", `ps://x"}`), nil)
		sw.Send(&schema.Message{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "call_fr", Type: "function", Function: schema.FunctionCall{Name: "file_read", Arguments: `{"url":"https://x"}`}}},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		}, nil)
		sw.Close()
	}()

	ch := make(chan stream.Event, 64)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 1})

	_, err := streamScanToolCallChecker(ctx, sr)
	require.NoError(t, err)
	close(ch)

	evs := collectEvents(ch)
	assert.Empty(t, allEventsOfType(evs, stream.EventToolCallArgsDelta),
		"file_read is not allowlisted → checker must emit no args-delta")
}
