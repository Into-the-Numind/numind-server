package skill

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQuestionnaireAnswers_unmarshalOmitsEmpty verifies that fields with zero
// values are omitted when marshaling and zero values don't cause errors when
// the JSON is missing those keys.
func TestQuestionnaireAnswers_unmarshalOmitsEmpty(t *testing.T) {
	// Only Q6 and Q12 filled — other fields should marshal with omitempty (absent)
	qa := QuestionnaireAnswers{
		Q6:  []string{"analyze_data"},
		Q12: "friendly",
	}

	data, err := json.Marshal(qa)
	require.NoError(t, err)

	// Q7/Q8/Q9/Q10/Q11 should be absent (omitempty)
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "q6")
	assert.Contains(t, raw, "q12")
	assert.NotContains(t, raw, "q7", "q7 should be omitted when empty")
	assert.NotContains(t, raw, "q8", "q8 should be omitted when zero")
	assert.NotContains(t, raw, "q9", "q9 should be omitted when empty string")
	assert.NotContains(t, raw, "q10")
	assert.NotContains(t, raw, "q11")

	// Unmarshal back should give same struct
	var qa2 QuestionnaireAnswers
	require.NoError(t, json.Unmarshal(data, &qa2))
	assert.Equal(t, qa.Q6, qa2.Q6)
	assert.Equal(t, qa.Q12, qa2.Q12)
	assert.Empty(t, qa2.Q7)
	assert.Zero(t, qa2.Q8)
}

// TestQuestionnaireAnswers_unmarshalIgnoresUnknownField verifies schema evolution
// compatibility: old snapshots containing unknown fields do not cause errors.
func TestQuestionnaireAnswers_unmarshalIgnoresUnknownField(t *testing.T) {
	// Simulate an old snapshot with a future field "q13_future_feature"
	oldSnapshot := `{
		"q6": ["analyze_data"],
		"q7": ["text"],
		"q12": "professional",
		"q13_future_feature": "some_value_from_future_version"
	}`

	var qa QuestionnaireAnswers
	// Default json.Unmarshal ignores unknown fields — this must not return an error
	err := json.Unmarshal([]byte(oldSnapshot), &qa)
	require.NoError(t, err, "unknown field from future schema should be silently ignored")

	assert.Equal(t, []string{"analyze_data"}, qa.Q6)
	assert.Equal(t, []string{"text"}, qa.Q7)
	assert.Equal(t, "professional", qa.Q12)
}

// TestTaskTypeDisplay verifies Q6 task type code → Chinese display mapping.
func TestTaskTypeDisplay(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"analyze_data", "分析数据 / 报表"},
		{"generate_content", "生成文字内容"},
		{"answer_questions", "回答问题 / 答疑"},
		{"make_plan", "帮助制定计划"},
		{"grade_assignment", "批改 / 评分学员作业"},
		{"unknown_code", "unknown_code"}, // fallback: return as-is
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			assert.Equal(t, tc.want, taskTypeDisplay(tc.code))
		})
	}
}

// TestMaterialTypeDisplay verifies Q7 material type code → Chinese display mapping.
func TestMaterialTypeDisplay(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"text", "文字（笔记、日报、复盘）"},
		{"csv", "Excel / CSV 数据表格"},
		{"image", "图片（截图、海报）"},
		{"none", "不需要上传"},
		{"audio", "audio"}, // fallback: return as-is
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			assert.Equal(t, tc.want, materialTypeDisplay(tc.code))
		})
	}
}

// TestStyleDisplay verifies Q12 style code → Chinese display mapping.
func TestStyleDisplay(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"friendly", "亲切活泼的风格"},
		{"professional", "专业严谨的风格"},
		{"encouraging", "鼓励陪伴的风格"},
		{"formal", "formal"}, // fallback: return as-is
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			assert.Equal(t, tc.want, styleDisplay(tc.code))
		})
	}
}
