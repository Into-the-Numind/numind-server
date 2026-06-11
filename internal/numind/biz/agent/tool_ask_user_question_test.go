package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the single-question case (one element in the questions
// array — the most common shape). Multi-question behavior and per-question
// validation rules live in tool_ask_user_question_multi_test.go.

// oneQuestion wraps a single question into the array input for brevity.
func oneQuestion(item askUserQuestionItem) []byte {
	in, _ := json.Marshal(askUserQuestionInput{Questions: []askUserQuestionItem{item}})
	return in
}

// requireSoftReject asserts Execute soft-rejected the input: no Go error (so the
// run is NOT killed), not a yield, and a tool result carrying an error message the
// model can read and retry on. After the ask-question-soft-error hotfix (dev run
// #133), every malformed/over-limit tool call takes this path instead of a hard
// error that would terminate the whole run.
func requireSoftReject(t *testing.T, result ToolResult, err error) {
	t.Helper()
	require.NoError(t, err, "soft error: the run must survive (no hard error)")
	var ye *yieldError
	require.False(t, errors.As(err, &ye), "soft error is not a yield")
	require.NotNil(t, result, "soft error returns a tool result for the model")
	assert.Contains(t, string(result), "error", "soft error result carries a message")
}

func TestAskUserQuestionTool_HappyPath(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "Which region should we target?",
		Options: []YieldOption{
			{Key: "north", Label: "北方"},
			{Key: "south", Label: "南方"},
		},
		Header:      "选择",
		MultiSelect: false,
	})

	result, err := tool.Execute(context.Background(), ToolInput(in))

	// Execute must return (nil, *yieldError).
	require.Nil(t, result)
	require.Error(t, err)

	var ye *yieldError
	require.True(t, errors.As(err, &ye), "error must be *yieldError")
	require.Len(t, ye.Payload.Questions, 1)
	assert.Equal(t, "Which region should we target?", ye.Payload.Questions[0].Question)
	assert.Len(t, ye.Payload.Questions[0].Options, 2)
	assert.Equal(t, "north", ye.Payload.Questions[0].Options[0].Key)
	assert.Equal(t, "选择", ye.Payload.Questions[0].Header)
	assert.False(t, ye.Payload.Questions[0].MultiSelect)

	// errors.Is with sentinel.
	assert.True(t, errors.Is(err, ErrYieldForUserQuestion))
}

func TestAskUserQuestionTool_HappyPath_ThreeOptions(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "What format?",
		Options: []YieldOption{
			{Key: "a", Label: "PDF"},
			{Key: "b", Label: "Word"},
			{Key: "c", Label: "PPT"},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	require.True(t, errors.As(err, &ye))
	assert.Len(t, ye.Payload.Questions[0].Options, 3)
}

func TestAskUserQuestionTool_InvalidJSON(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), ToolInput(`not-json`))
	requireSoftReject(t, result, err)
}

func TestAskUserQuestionTool_EmptyQuestion(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "",
		Options:  []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}},
	})
	result, err := tool.Execute(context.Background(), ToolInput(in))
	requireSoftReject(t, result, err)
}

func TestAskUserQuestionTool_NoOptions_OpenEnded(t *testing.T) {
	// ask-question-options-tolerant: 0 options is a valid open-ended question.
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "No options?",
		Options:  []YieldOption{},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "0 options is a valid open-ended question")
}

func TestAskUserQuestionTool_FiveOptions_ClampsToFour(t *testing.T) {
	// ask-question-options-tolerant: >4 options clamp to 4 instead of erroring.
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "Five options?",
		Options: []YieldOption{
			{Key: "a", Label: "A"}, {Key: "b", Label: "B"},
			{Key: "c", Label: "C"}, {Key: "d", Label: "D"},
			{Key: "e", Label: "E"},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye))
	assert.Len(t, ye.Payload.Questions[0].Options, 4)
}

func TestAskUserQuestionTool_MissingOptionKey(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "Missing key?",
		Options:  []YieldOption{{Key: "", Label: "A"}, {Key: "b", Label: "B"}},
	})
	result, err := tool.Execute(context.Background(), ToolInput(in))
	requireSoftReject(t, result, err)
}

func TestAskUserQuestionTool_HeaderTooLong(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{
		Question: "Long header?",
		Options:  []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}},
		Header:   "这个标题超过了十二个字符的限制",
	})
	result, err := tool.Execute(context.Background(), ToolInput(in))
	requireSoftReject(t, result, err)
}

// test(qa): reproduce dev run #133 — after the agent finished its web research it
// tried to "ask everything at once" via a multi-question ask_user_question call.
// The tool-call JSON arrived truncated ("unexpected end of JSON input" — large
// payload cut off by the model's output limit / stream), Execute returned a HARD
// error, it propagated as NodeRunError, and the whole run died model_error — the
// user saw "服务暂时不可用". A truncated / empty / malformed tool call must be a
// SOFT error (a tool result fed back to the model, nil Go error) so the run
// survives and the model can retry with a smaller call.
//
// Before the fix this FAILS (each case returns a hard error).
func TestAskUserQuestionTool_MalformedArgs_SoftErrorNotKill(t *testing.T) {
	tool := NewAskUserQuestionTool()
	cases := []struct{ name, args string }{
		{"truncated mid-json", `{"questions":[{"question":"创始人的故事是什么？","options":[{"key":"a","label":"`},
		{"empty args", ``},
		{"empty object (0 questions)", `{}`},
		{"not json", `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), ToolInput(tc.args))
			// MUST NOT hard-error — a hard error kills the entire run.
			require.NoError(t, err, "malformed/truncated args must be a soft error, not a run-killer")
			var ye *yieldError
			require.False(t, errors.As(err, &ye), "must not be a yield (it did not succeed)")
			// Soft error carries a message back to the model so it can retry.
			require.NotNil(t, result)
			assert.Contains(t, string(result), "error")
		})
	}
}

func TestAskUserQuestionTool_Metadata(t *testing.T) {
	tool := NewAskUserQuestionTool()
	assert.Equal(t, "ask_user_question", tool.Name())
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.AlwaysLoad())
	assert.Equal(t, "反问", tool.NarrationVerb())
	assert.Equal(t, "反问", tool.UserFacingName())
}

// test(qa): reproduce dev run #127 — the agent (after ask-question-freetext
// taught it options are "suggestions, not exhaustive") supplied 10 options;
// Execute's hard 2-4 check failed the tool call, the run died model_error and
// the user saw "服务不可用". Expected: clamp to the first 4 and yield normally.
func TestAskUserQuestionTool_TenOptions_ClampsAndYields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	opts := make([]YieldOption, 10)
	for i := range opts {
		opts[i] = YieldOption{Key: string(rune('a' + i)), Label: string(rune('A' + i))}
	}
	in := oneQuestion(askUserQuestionItem{Question: "Pick?", Options: opts})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "10 options must clamp + yield, not crash the run")
	assert.Len(t, ye.Payload.Questions[0].Options, 4, "options clamped to 4")
}

// A pure open-ended question (0 options) is valid — the user answers entirely
// via the always-present free-text box (ask-question-freetext).
func TestAskUserQuestionTool_ZeroOptions_OpenEndedYields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{Question: "请提供你们的陪跑周期和价格", Options: []YieldOption{}})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "0 options (open-ended) must yield, not error")
	assert.Empty(t, ye.Payload.Questions[0].Options)
}

// Exactly 1 option is not a meaningful choice — still rejected.
func TestAskUserQuestionTool_OneOption_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in := oneQuestion(askUserQuestionItem{Question: "Only one?", Options: []YieldOption{{Key: "a", Label: "A"}}})
	result, err := tool.Execute(context.Background(), ToolInput(in))
	requireSoftReject(t, result, err)
}
