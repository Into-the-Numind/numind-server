package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskUserQuestionTool_HappyPath(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
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
	assert.Equal(t, "Which region should we target?", ye.Payload.Question)
	assert.Len(t, ye.Payload.Options, 2)
	assert.Equal(t, "north", ye.Payload.Options[0].Key)
	assert.Equal(t, "选择", ye.Payload.Header)
	assert.False(t, ye.Payload.MultiSelect)

	// errors.Is with sentinel.
	assert.True(t, errors.Is(err, ErrYieldForUserQuestion))
}

func TestAskUserQuestionTool_HappyPath_ThreeOptions(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
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
	assert.Len(t, ye.Payload.Options, 3)
}

func TestAskUserQuestionTool_InvalidJSON(t *testing.T) {
	tool := NewAskUserQuestionTool()
	_, err := tool.Execute(context.Background(), ToolInput(`not-json`))
	require.Error(t, err)
	assert.NotNil(t, err)
	// Must NOT be a yieldError.
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestionTool_EmptyQuestion(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Question: "",
		Options:  []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestionTool_NoOptions(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Question: "No options?",
		Options:  []YieldOption{},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestionTool_TooManyOptions(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Question: "Five options?",
		Options: []YieldOption{
			{Key: "a", Label: "A"}, {Key: "b", Label: "B"},
			{Key: "c", Label: "C"}, {Key: "d", Label: "D"},
			{Key: "e", Label: "E"},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestionTool_MissingOptionKey(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Question: "Missing key?",
		Options:  []YieldOption{{Key: "", Label: "A"}, {Key: "b", Label: "B"}},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestionTool_HeaderTooLong(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Question: "Long header?",
		Options:  []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}},
		Header:   "这个标题超过了十二个字符的限制",
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
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
	in, _ := json.Marshal(askUserQuestionInput{Question: "Pick?", Options: opts})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "10 options must clamp + yield, not crash the run")
	assert.Len(t, ye.Payload.Options, 4, "options clamped to 4")
}

// A pure open-ended question (0 options) is valid — the user answers entirely
// via the always-present free-text box (ask-question-freetext).
func TestAskUserQuestionTool_ZeroOptions_OpenEndedYields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{Question: "请提供你们的陪跑周期和价格", Options: []YieldOption{}})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "0 options (open-ended) must yield, not error")
	assert.Empty(t, ye.Payload.Options)
}

// Exactly 1 option is not a meaningful choice — still rejected.
func TestAskUserQuestionTool_OneOption_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{Question: "Only one?", Options: []YieldOption{{Key: "a", Label: "A"}}})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}
