package skill

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// makeAD is a helper to build a minimal AgentDefinition with QuestionnaireAnswers JSON.
func makeAD(name, description string, qa *QuestionnaireAnswers) *model.AgentDefinition {
	ad := &model.AgentDefinition{
		Name:        name,
		Description: description,
	}
	if qa != nil {
		data, _ := json.Marshal(qa)
		ad.QuestionnaireAnswers = data
	}
	return ad
}

// TestBuild_MinimalRequiredFields_succeeds verifies that a minimal valid input
// (Q6, Q7, Q12 all filled) produces output without error.
func TestBuild_MinimalRequiredFields_succeeds(t *testing.T) {
	ad := makeAD("测试助手", "帮助用户分析数据", &QuestionnaireAnswers{
		Q6:  []string{"analyze_data"},
		Q7:  []string{"text"},
		Q12: "friendly",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotEmpty(t, body)
}

// TestBuild_MissingQ6_succeedsAndOmitsSection verifies that empty Q6 succeeds and
// does not render the 任务类型 section.
func TestBuild_MissingQ6_succeedsAndOmitsSection(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{}, // empty
		Q7:  []string{"text"},
		Q12: "professional",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotContains(t, body, "## 任务类型")
}

// TestBuild_MissingQ7_succeedsAndOmitsSection verifies that empty Q7 succeeds and
// does not render the 输入材料类型 section.
func TestBuild_MissingQ7_succeedsAndOmitsSection(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"answer_questions"},
		Q7:  []string{}, // empty
		Q12: "encouraging",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotContains(t, body, "## 输入材料类型")
}

// TestBuild_MissingQ12_succeedsAndOmitsSection verifies that empty Q12 succeeds and
// does not render the 语气风格 section.
func TestBuild_MissingQ12_succeedsAndOmitsSection(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"make_plan"},
		Q7:  []string{"csv"},
		Q12: "", // empty
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotContains(t, body, "## 语气风格")
}

// TestBuild_NilQuestionnaireAnswers_succeeds verifies that
// ad.QuestionnaireAnswers = nil results in a successful build with basic role definition only.
func TestBuild_NilQuestionnaireAnswers_succeeds(t *testing.T) {
	ad := &model.AgentDefinition{
		Name:                 "无问卷助手",
		Description:          "描述",
		QuestionnaireAnswers: nil, // not set
	}

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "## 角色定义")
	assert.NotContains(t, body, "## 任务类型")
	assert.NotContains(t, body, "## 输入材料类型")
	assert.NotContains(t, body, "## 语气风格")
}

// TestBuild_InvalidJSON_returnsParseError verifies that malformed JSON in
// questionnaire_answers produces a wrapped parse error (not ErrSkillBuilderFailed).
func TestBuild_InvalidJSON_returnsParseError(t *testing.T) {
	ad := &model.AgentDefinition{
		Name:                 "坏JSON助手",
		Description:          "描述",
		QuestionnaireAnswers: []byte(`{invalid json`),
	}

	_, err := Build(ad)
	require.Error(t, err)
	assert.False(t, errors.Is(err, errno.ErrSkillBuilderFailed), "parse error should not be ErrSkillBuilderFailed")
	assert.Contains(t, err.Error(), "Build: parse questionnaire")
}

// TestBuild_OptionalQ10_includedWhenSet verifies that a non-empty Q10 (禁区 / soft rules)
// is included in the output body.
func TestBuild_OptionalQ10_includedWhenSet(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"answer_questions"},
		Q7:  []string{"text"},
		Q12: "professional",
		Q10: "不讨论竞品",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "## 禁区（软规则）")
	assert.Contains(t, body, "不讨论竞品")
}

// TestBuild_OptionalQ10_skippedWhenEmpty verifies that an empty Q10 does not produce
// the 禁区 section in the output body.
func TestBuild_OptionalQ10_skippedWhenEmpty(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"answer_questions"},
		Q7:  []string{"text"},
		Q12: "professional",
		Q10: "", // not set
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotContains(t, body, "## 禁区（软规则）")
}

// TestBuild_OptionalQ11_includedWhenSet verifies that a non-empty Q11 (越界处理策略)
// is included in the output body with the correct prefix.
func TestBuild_OptionalQ11_includedWhenSet(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"grade_assignment"},
		Q7:  []string{"image"},
		Q12: "encouraging",
		Q11: "这超出了我的服务范围，请联系课程顾问。",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "## 越界处理策略")
	assert.Contains(t, body, "当学员的问题超出范围时，请回复：")
	assert.Contains(t, body, "这超出了我的服务范围，请联系课程顾问。")
}

// TestBuild_OptionalQ11_skippedWhenEmpty verifies that an empty Q11 does not produce
// the 越界处理策略 section in the output body.
func TestBuild_OptionalQ11_skippedWhenEmpty(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"grade_assignment"},
		Q7:  []string{"image"},
		Q12: "encouraging",
		Q11: "", // not set
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.NotContains(t, body, "## 越界处理策略")
}

// TestBuild_Q6MultipleSelection_rendersAll verifies that multiple Q6 selections
// are all rendered as list items in the 任务类型 section.
func TestBuild_Q6MultipleSelection_rendersAll(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"analyze_data", "answer_questions"},
		Q7:  []string{"text"},
		Q12: "friendly",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "- 分析数据 / 报表")
	assert.Contains(t, body, "- 回答问题 / 答疑")
}

// TestBuild_Q12_friendly_rendersStyle verifies that Q12="friendly" maps to
// the correct Chinese display string.
func TestBuild_Q12_friendly_rendersStyle(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"make_plan"},
		Q7:  []string{"text"},
		Q12: "friendly",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "亲切活泼的风格")
}

// TestBuild_Q12_professional_rendersStyle verifies that Q12="professional" maps to
// the correct Chinese display string.
func TestBuild_Q12_professional_rendersStyle(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"generate_content"},
		Q7:  []string{"csv"},
		Q12: "professional",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "专业严谨的风格")
}

// TestBuild_Q12_encouraging_rendersStyle verifies that Q12="encouraging" maps to
// the correct Chinese display string.
func TestBuild_Q12_encouraging_rendersStyle(t *testing.T) {
	ad := makeAD("助手", "描述", &QuestionnaireAnswers{
		Q6:  []string{"grade_assignment"},
		Q7:  []string{"image"},
		Q12: "encouraging",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "鼓励陪伴的风格")
}

// TestBuild_BodyContainsRoleAndDescriptionFromAd verifies that ad.Name and ad.Description
// are injected into the 角色定义 section of the output.
func TestBuild_BodyContainsRoleAndDescriptionFromAd(t *testing.T) {
	ad := makeAD("我的专属助手", "协助销售团队完成日报分析", &QuestionnaireAnswers{
		Q6:  []string{"analyze_data"},
		Q7:  []string{"csv"},
		Q12: "professional",
	})

	body, err := Build(ad)
	require.NoError(t, err)
	assert.Contains(t, body, "我的专属助手")
	assert.Contains(t, body, "协助销售团队完成日报分析")
}

// TestBuild_OutputContainsAllRequiredSections verifies that all 4 mandatory section
// headings appear in the output regardless of which optional sections are present.
func TestBuild_OutputContainsAllRequiredSections(t *testing.T) {
	ad := makeAD("综合助手", "负责多项任务", &QuestionnaireAnswers{
		Q6:  []string{"answer_questions", "make_plan"},
		Q7:  []string{"text", "none"},
		Q12: "encouraging",
	})

	body, err := Build(ad)
	require.NoError(t, err)

	requiredSections := []string{
		"## 角色定义",
		"## 任务类型",
		"## 输入材料类型",
		"## 语气风格",
	}
	for _, section := range requiredSections {
		assert.Contains(t, body, section, "required section %q must appear in output", section)
	}
}
