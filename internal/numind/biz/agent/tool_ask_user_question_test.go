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
