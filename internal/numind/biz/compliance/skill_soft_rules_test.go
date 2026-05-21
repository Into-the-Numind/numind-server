package compliance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

func TestExtractFromAgentDef_Nil(t *testing.T) {
	result := ExtractFromAgentDef(nil)
	assert.Equal(t, SkillSoftRules{}, result)
	assert.Empty(t, result.CautionTopics)
	assert.Empty(t, result.OutOfScopeReply)
}

func TestExtractFromAgentDef_EmptyQuestionnaire(t *testing.T) {
	ad := &model.AgentDefinition{
		QuestionnaireAnswers: nil,
	}
	result := ExtractFromAgentDef(ad)
	assert.Equal(t, SkillSoftRules{}, result)

	// Also test with empty byte slice
	ad2 := &model.AgentDefinition{
		QuestionnaireAnswers: datatypes.JSON([]byte{}),
	}
	result2 := ExtractFromAgentDef(ad2)
	assert.Equal(t, SkillSoftRules{}, result2)
}

func TestExtractFromAgentDef_WithQ10Q11(t *testing.T) {
	ad := &model.AgentDefinition{
		QuestionnaireAnswers: datatypes.JSON([]byte(`{"q10":"注意X","q11":"越界Y"}`)),
	}
	result := ExtractFromAgentDef(ad)
	assert.Equal(t, "注意X", result.CautionTopics)
	assert.Equal(t, "越界Y", result.OutOfScopeReply)
}

func TestExtractFromAgentDef_JSONParseError(t *testing.T) {
	ad := &model.AgentDefinition{
		QuestionnaireAnswers: datatypes.JSON([]byte("not json{")),
	}
	// Must not panic and must return zero value
	result := ExtractFromAgentDef(ad)
	assert.Equal(t, SkillSoftRules{}, result)
	assert.Empty(t, result.CautionTopics)
	assert.Empty(t, result.OutOfScopeReply)
}

func TestSkillSoftRules_NarrationOrDefault_EmptyQ11(t *testing.T) {
	r := SkillSoftRules{
		CautionTopics:   "一些注意话题",
		OutOfScopeReply: "",
	}
	msg := r.NarrationOrDefault()
	assert.Equal(t, DefaultOutOfScopeNarration, msg)
	assert.NotEmpty(t, msg)
}

func TestSkillSoftRules_NarrationOrDefault_NonEmptyQ11(t *testing.T) {
	r := SkillSoftRules{
		CautionTopics:   "一些注意话题",
		OutOfScopeReply: "自定义越界话术",
	}
	msg := r.NarrationOrDefault()
	assert.Equal(t, "自定义越界话术", msg)
	assert.NotEqual(t, DefaultOutOfScopeNarration, msg)
}
