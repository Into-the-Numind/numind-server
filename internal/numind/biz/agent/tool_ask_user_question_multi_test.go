package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent-multi-question T1: ask_user_question accepts a `questions` array (1-4
// independent questions), each with its own header/options/multi_select. These
// tests pin the new array protocol and per-question validation.

func TestAskUserQuestion_TwoQuestions_Yields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{
				Question: "你们的陪跑周期多长？",
				Header:   "陪跑模式",
				Options:  []YieldOption{{Key: "a", Label: "90天"}, {Key: "b", Label: "180天"}},
			},
			{
				Question:    "主要客群是谁？",
				Header:      "客群",
				Options:     []YieldOption{{Key: "a", Label: "宝妈"}, {Key: "b", Label: "职场人"}, {Key: "c", Label: "学生"}},
				MultiSelect: true,
			},
		},
	})

	result, err := tool.Execute(context.Background(), ToolInput(in))

	require.Nil(t, result)
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "must yield")
	require.Len(t, ye.Payload.Questions, 2)
	assert.Equal(t, "你们的陪跑周期多长？", ye.Payload.Questions[0].Question)
	assert.Equal(t, "陪跑模式", ye.Payload.Questions[0].Header)
	assert.Len(t, ye.Payload.Questions[0].Options, 2)
	assert.False(t, ye.Payload.Questions[0].MultiSelect)
	assert.Equal(t, "主要客群是谁？", ye.Payload.Questions[1].Question)
	assert.True(t, ye.Payload.Questions[1].MultiSelect)
	assert.Equal(t, "宝妈", ye.Payload.Questions[1].Options[0].Label)
}

func TestAskUserQuestion_FourQuestions_Yields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	items := make([]askUserQuestionItem, 4)
	for i := range items {
		items[i] = askUserQuestionItem{
			Question: string(rune('A'+i)) + "?",
			Options:  []YieldOption{{Key: "y", Label: "是"}, {Key: "n", Label: "否"}},
		}
	}
	in, _ := json.Marshal(askUserQuestionInput{Questions: items})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye))
	assert.Len(t, ye.Payload.Questions, 4)
}

func TestAskUserQuestion_ZeroQuestions_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{Questions: []askUserQuestionItem{}})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "0 questions must error, not yield")
}

func TestAskUserQuestion_FiveQuestions_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	items := make([]askUserQuestionItem, 5)
	for i := range items {
		items[i] = askUserQuestionItem{
			Question: string(rune('A'+i)) + "?",
			Options:  []YieldOption{{Key: "y", Label: "是"}, {Key: "n", Label: "否"}},
		}
	}
	in, _ := json.Marshal(askUserQuestionInput{Questions: items})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "5 questions must error (max 4)")
}

func TestAskUserQuestion_DuplicateQuestionText_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "同样的问题？", Options: []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
			{Question: "同样的问题？", Options: []YieldOption{{Key: "c", Label: "C"}, {Key: "d", Label: "D"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "duplicate question text must error")
}

func TestAskUserQuestion_DuplicateLabelWithinQuestion_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "选哪个？", Options: []YieldOption{{Key: "a", Label: "同名"}, {Key: "b", Label: "同名"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "duplicate option label within a question must error")
}

func TestAskUserQuestion_PerQuestion_OneOption_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "好问题？", Options: []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
			{Question: "坏问题？", Options: []YieldOption{{Key: "a", Label: "只有一个"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "a question with exactly 1 option must error")
}

func TestAskUserQuestion_PerQuestion_ZeroOptions_OpenEnded(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "请提供你们的陪跑周期和价格", Options: []YieldOption{}},
			{Question: "首选格式？", Options: []YieldOption{{Key: "a", Label: "PDF"}, {Key: "b", Label: "Word"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "0 options on one question (open-ended) is valid")
	assert.Empty(t, ye.Payload.Questions[0].Options)
	assert.Len(t, ye.Payload.Questions[1].Options, 2)
}

func TestAskUserQuestion_PerQuestion_FiveOptions_ClampsToFour(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "太多选项？", Options: []YieldOption{
				{Key: "a", Label: "A"}, {Key: "b", Label: "B"}, {Key: "c", Label: "C"},
				{Key: "d", Label: "D"}, {Key: "e", Label: "E"},
			}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye))
	assert.Len(t, ye.Payload.Questions[0].Options, 4, "per-question options clamp to 4")
}

// Clamping is per-question: an overlong question clamps to 4 while its siblings
// are untouched.
func TestAskUserQuestion_MixedOptionCounts_ClampsOnlyTheOverlong(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "太多？", Options: []YieldOption{
				{Key: "a", Label: "A"}, {Key: "b", Label: "B"}, {Key: "c", Label: "C"},
				{Key: "d", Label: "D"}, {Key: "e", Label: "E"},
			}},
			{Question: "正常？", Options: []YieldOption{{Key: "a", Label: "是"}, {Key: "b", Label: "否"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	var ye *yieldError
	require.True(t, errors.As(err, &ye))
	require.Len(t, ye.Payload.Questions, 2)
	assert.Len(t, ye.Payload.Questions[0].Options, 4, "overlong question clamped to 4")
	assert.Len(t, ye.Payload.Questions[1].Options, 2, "well-formed sibling untouched")
}

func TestAskUserQuestion_PerQuestion_MissingKeyOrLabel_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "缺 key？", Options: []YieldOption{{Key: "", Label: "A"}, {Key: "b", Label: "B"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestion_PerQuestion_HeaderTooLong_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "标题太长？", Header: "这个标题超过了十二个字符的限制", Options: []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

func TestAskUserQuestion_EmptyQuestionText_Rejected(t *testing.T) {
	tool := NewAskUserQuestionTool()
	in, _ := json.Marshal(askUserQuestionInput{
		Questions: []askUserQuestionItem{
			{Question: "", Options: []YieldOption{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
		},
	})
	_, err := tool.Execute(context.Background(), ToolInput(in))
	require.Error(t, err)
	var ye *yieldError
	assert.False(t, errors.As(err, &ye))
}

// ParsePendingQuestion bridges the pre-agent-multi-question single-question
// pending_question_json shape so in-flight waiting runs survive the rollout.

func TestParsePendingQuestion_LegacySingleQuestion_WrapsAsOneElement(t *testing.T) {
	legacy := []byte(`{"question":"老格式问题？","header":"H","multi_select":true,"options":[{"key":"a","label":"A"},{"key":"b","label":"B"}]}`)
	p, err := ParsePendingQuestion(legacy)
	require.NoError(t, err)
	require.Len(t, p.Questions, 1, "legacy single-question wraps into a one-element Questions slice")
	assert.Equal(t, "老格式问题？", p.Questions[0].Question)
	assert.Equal(t, "H", p.Questions[0].Header)
	assert.True(t, p.Questions[0].MultiSelect)
	assert.Len(t, p.Questions[0].Options, 2)
	assert.Equal(t, "a", p.Questions[0].Options[0].Key)
}

func TestParsePendingQuestion_NewArrayFormat_PassesThrough(t *testing.T) {
	raw := []byte(`{"questions":[{"question":"Q1?","options":[{"key":"a","label":"A"},{"key":"b","label":"B"}]},{"question":"Q2?","options":[]}]}`)
	p, err := ParsePendingQuestion(raw)
	require.NoError(t, err)
	require.Len(t, p.Questions, 2)
	assert.Equal(t, "Q1?", p.Questions[0].Question)
	assert.Equal(t, "Q2?", p.Questions[1].Question)
	assert.Empty(t, p.Questions[1].Options)
}

func TestParsePendingQuestion_Malformed_ReturnsError(t *testing.T) {
	_, err := ParsePendingQuestion([]byte(`not-json`))
	require.Error(t, err)
}

// Degenerate-but-valid JSON inputs must not error — they return an empty
// payload and callers guard on len(Questions)==0. Documents the migration
// safety net's contract for corrupted/empty DB rows.
func TestParsePendingQuestion_EmptyShapes_ReturnEmptyNoError(t *testing.T) {
	cases := map[string]string{
		"json null":           `null`,
		"empty object":        `{}`,
		"explicit null array": `{"questions":null}`,
		"empty array":         `{"questions":[]}`,
		"legacy no question":  `{"options":[{"key":"a","label":"A"},{"key":"b","label":"B"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := ParsePendingQuestion([]byte(raw))
			require.NoError(t, err, "degenerate-but-valid JSON must not error")
			assert.Empty(t, p.Questions, "no questions to synthesize")
		})
	}
}
