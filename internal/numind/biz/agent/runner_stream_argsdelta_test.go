package agent

// BE-1: tool-call args-delta streaming + allowlist gate.
//
// consumeEinoStream reads the side-channel *aiservice.ToolCallArgsDelta that the
// adapter stashed in schema.Message.Extra and emits EventToolCallArgsDelta — but
// ONLY for allowlisted code/content tools (isCodeStreamingTool). Non-allowlisted
// tools (e.g. web_search) must emit nothing.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/aiservice"
)

// argsDeltaMsg builds an interim schema.Message carrying a tool-call args delta
// in Extra exactly as wrapChannelAsStreamReader does.
func argsDeltaMsg(id, name, frag string) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant,
		Extra: map[string]any{
			extraKeyToolCallArgsDelta: &aiservice.ToolCallArgsDelta{
				ToolCallID:   id,
				FunctionName: name,
				ArgsDelta:    frag,
			},
		},
	}
}

// TestConsumeEinoStream_EmitsArgsDeltaForCodeTool feeds a run_python tool-call
// streamed as N arguments fragments and asserts: N EventToolCallArgsDelta events
// are emitted, their concatenation equals the full arguments, and the terminal
// chunk still carries the assembled ToolCall (execution contract intact).
func TestConsumeEinoStream_EmitsArgsDeltaForCodeTool(t *testing.T) {
	r := makeRunner()
	run := makeRun(701)
	st := &LoopState{}

	full := `{"code":"print(1)"}`
	msgs := []*schema.Message{
		argsDeltaMsg("call_py", "run_python", `{"code":"`),
		argsDeltaMsg("call_py", "run_python", `print(1)`),
		argsDeltaMsg("call_py", "run_python", `"}`),
		// Terminal assistant message: the fully assembled tool call.
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_py", Type: "function", Function: schema.FunctionCall{Name: "run_python", Arguments: full}},
			},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		},
		{Role: schema.Tool, ToolCallID: "call_py", Content: "1\n"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	argsDeltas := allEventsOfType(evs, stream.EventToolCallArgsDelta)
	require.Len(t, argsDeltas, 3, "expected one args-delta event per arguments fragment")

	var assembled string
	for _, ev := range argsDeltas {
		var p stream.ToolCallArgsDeltaPayload
		require.NoError(t, json.Unmarshal(ev.Data, &p))
		assert.Equal(t, "call_py", p.ToolCallID)
		assert.Equal(t, "run_python", p.FunctionName)
		assembled += p.ArgsDelta
	}
	assert.Equal(t, full, assembled, "concatenated args-delta must equal full arguments")

	// Execution contract intact: tool_call_start fired with the full args.
	starts := allEventsOfType(evs, stream.EventToolCallStart)
	require.NotEmpty(t, starts, "tool_call_start must still fire from the assembled ToolCall")
}

// TestConsumeEinoStream_NoArgsDeltaForNonCodeTool asserts the allowlist gate:
// web_search args-delta fragments must NOT produce any EventToolCallArgsDelta.
func TestConsumeEinoStream_NoArgsDeltaForNonCodeTool(t *testing.T) {
	r := makeRunner()
	run := makeRun(702)
	st := &LoopState{}

	msgs := []*schema.Message{
		argsDeltaMsg("call_ws", "web_search", `{"que`),
		argsDeltaMsg("call_ws", "web_search", `ry":"x"}`),
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_ws", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"x"}`}},
			},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		},
		{Role: schema.Tool, ToolCallID: "call_ws", Content: "results"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	argsDeltas := allEventsOfType(evs, stream.EventToolCallArgsDelta)
	assert.Empty(t, argsDeltas, "web_search is not in the allowlist; no args-delta must be emitted")

	// The tool itself still runs (start fires) — gate is observability-only.
	starts := allEventsOfType(evs, stream.EventToolCallStart)
	require.NotEmpty(t, starts, "tool_call_start must still fire for non-allowlisted tools")
}

// TestIsCodeStreamingTool pins the allowlist membership.
func TestIsCodeStreamingTool(t *testing.T) {
	in := []string{"run_python", "create_html", "create_docx", "create_csv", "create_json", "create_text", "create_png_chart"}
	for _, n := range in {
		assert.Truef(t, isCodeStreamingTool(n), "%s should be allowlisted", n)
	}
	out := []string{"web_search", "kb_search", "ask_user_question", "image_gen", "", "create_pdf"}
	for _, n := range out {
		assert.Falsef(t, isCodeStreamingTool(n), "%s should NOT be allowlisted", n)
	}
}
