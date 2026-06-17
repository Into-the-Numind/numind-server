package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// WriteHistorySnapshot — database tests
// ---------------------------------------------------------------------------

func TestWriteHistorySnapshot_persistsCompleteRow(t *testing.T) {
	db := newTestDB(t)

	// Seed an AgentDefinition so FK-style references are realistic.
	ad := &model.AgentDefinition{
		ParentUserID: 1,
		Name:         "测试助手",
		Description:  "单测",
		Version:      1,
		IsActive:     true,
		CreatedBy:    1,
	}
	require.NoError(t, db.Create(ad).Error)

	summary := "首次发布"
	err := WriteHistorySnapshot(context.Background(), db, ad, 1, summary)
	require.NoError(t, err)

	// Verify the persisted row.
	var hist model.AgentDefinitionHistory
	require.NoError(t, db.First(&hist, "agent_id = ? AND version = ?", ad.ID, ad.Version).Error)

	assert.Equal(t, ad.ID, hist.AgentID)
	assert.Equal(t, ad.Version, hist.Version)
	assert.Equal(t, summary, hist.ChangesSummary)
	assert.Equal(t, uint(1), hist.CreatedBy)
	// Snapshot should be non-empty JSON containing the agent name.
	assert.Contains(t, string(hist.Snapshot), "测试助手")
}

func TestWriteHistorySnapshot_uniqueConstraintEnforced(t *testing.T) {
	db := newTestDB(t)

	ad := &model.AgentDefinition{
		ParentUserID: 2,
		Name:         "重复版本",
		Version:      1,
		IsActive:     true,
		CreatedBy:    2,
	}
	require.NoError(t, db.Create(ad).Error)

	// First write must succeed.
	err := WriteHistorySnapshot(context.Background(), db, ad, 2, "首次发布")
	require.NoError(t, err)

	// Second write with the same (agent_id, version) must fail due to UNIQUE constraint.
	err = WriteHistorySnapshot(context.Background(), db, ad, 2, "重复写入")
	require.Error(t, err, "second snapshot for same version should violate unique constraint")
}

// ---------------------------------------------------------------------------
// ComputeChangesSummary — pure logic tests (no DB needed)
// ---------------------------------------------------------------------------

func TestComputeChangesSummary_firstPublish(t *testing.T) {
	curr := &model.AgentDefinition{Name: "新助手"}
	got := ComputeChangesSummary(nil, curr, 0)
	assert.Equal(t, "首次发布", got)
}

func TestComputeChangesSummary_restoreFromVersion(t *testing.T) {
	prev := &model.AgentDefinition{Version: 3, IsActive: true}
	curr := &model.AgentDefinition{Version: 4, IsActive: true}
	got := ComputeChangesSummary(prev, curr, 3)
	assert.Equal(t, "从 v3 恢复", got)
}

func TestComputeChangesSummary_systemPromptChange(t *testing.T) {
	prev := &model.AgentDefinition{SystemPrompt: "old prompt", IsActive: true}
	curr := &model.AgentDefinition{SystemPrompt: "new prompt", IsActive: true}
	got := ComputeChangesSummary(prev, curr, 0)
	assert.Equal(t, "修改了 行为指引", got)
}

func TestComputeChangesSummary_softDelete(t *testing.T) {
	prev := &model.AgentDefinition{IsActive: true}
	curr := &model.AgentDefinition{IsActive: false}
	got := ComputeChangesSummary(prev, curr, 0)
	assert.Equal(t, "软删除", got)
}

func TestComputeChangesSummary_questionnaireQ12Change(t *testing.T) {
	qaOld := QuestionnaireAnswers{Q6: []string{"analyze_data"}, Q7: []string{"text"}, Q12: "friendly"}
	qaNew := QuestionnaireAnswers{Q6: []string{"analyze_data"}, Q7: []string{"text"}, Q12: "professional"}

	rawOld, _ := json.Marshal(qaOld)
	rawNew, _ := json.Marshal(qaNew)

	prev := &model.AgentDefinition{IsActive: true, QuestionnaireAnswers: datatypes.JSON(rawOld)}
	curr := &model.AgentDefinition{IsActive: true, QuestionnaireAnswers: datatypes.JSON(rawNew)}

	got := ComputeChangesSummary(prev, curr, 0)
	assert.Equal(t, "修改了 Q12（说话风格）", got)
}

func TestComputeChangesSummary_questionnaireMultipleChange(t *testing.T) {
	qaOld := QuestionnaireAnswers{Q6: []string{"analyze_data"}, Q7: []string{"text"}, Q12: "friendly"}
	qaNew := QuestionnaireAnswers{Q6: []string{"generate_content"}, Q7: []string{"text"}, Q12: "professional"}

	rawOld, _ := json.Marshal(qaOld)
	rawNew, _ := json.Marshal(qaNew)

	prev := &model.AgentDefinition{IsActive: true, QuestionnaireAnswers: datatypes.JSON(rawOld)}
	curr := &model.AgentDefinition{IsActive: true, QuestionnaireAnswers: datatypes.JSON(rawNew)}

	got := ComputeChangesSummary(prev, curr, 0)
	// Both Q6 and Q12 changed — both should appear in the summary.
	assert.Contains(t, got, "Q6（任务类型）")
	assert.Contains(t, got, "Q12（说话风格）")
	assert.True(t, strings.HasPrefix(got, "修改了 "))
}

func TestComputeChangesSummary_nameDescriptionChange(t *testing.T) {
	prev := &model.AgentDefinition{Name: "旧名字", Description: "旧描述", IsActive: true}
	curr := &model.AgentDefinition{Name: "新名字", Description: "新描述", IsActive: true}

	got := ComputeChangesSummary(prev, curr, 0)
	assert.Contains(t, got, "Q1（名字）")
	assert.Contains(t, got, "Q3（描述）")
}

func TestComputeChangesSummary_iconURLChange(t *testing.T) {
	prev := &model.AgentDefinition{Name: "助手", IconURL: "old.png", IsActive: true}
	curr := &model.AgentDefinition{Name: "助手", IconURL: "new.png", IsActive: true}

	got := ComputeChangesSummary(prev, curr, 0)
	assert.Contains(t, got, "Q2（头像）")
}

func TestComputeChangesSummary_noChange(t *testing.T) {
	qa := QuestionnaireAnswers{Q6: []string{"analyze_data"}, Q7: []string{"text"}, Q12: "friendly"}
	raw, _ := json.Marshal(qa)

	prev := &model.AgentDefinition{
		Name:                 "助手",
		Description:          "描述",
		IsActive:             true,
		QuestionnaireAnswers: datatypes.JSON(raw),
	}
	curr := &model.AgentDefinition{
		Name:                 "助手",
		Description:          "描述",
		IsActive:             true,
		QuestionnaireAnswers: datatypes.JSON(raw),
	}

	got := ComputeChangesSummary(prev, curr, 0)
	assert.Equal(t, "更新", got)
}

func TestComputeChangesSummary_truncatedTo200Chars(t *testing.T) {
	// Craft a scenario that produces a very long list of changes by changing many
	// fields simultaneously: name, description, welcome_message, starters, and
	// all 7 questionnaire fields.
	qaOld := QuestionnaireAnswers{
		Q6:  []string{"analyze_data"},
		Q7:  []string{"text"},
		Q8:  800,
		Q9:  "no_web_search",
		Q10: "旧禁区",
		Q11: "旧话术",
		Q12: "friendly",
	}
	qaNew := QuestionnaireAnswers{
		Q6:  []string{"generate_content"},
		Q7:  []string{"csv"},
		Q8:  1200,
		Q9:  "allow_search",
		Q10: "新禁区",
		Q11: "新话术",
		Q12: "professional",
	}
	rawOld, _ := json.Marshal(qaOld)
	rawNew, _ := json.Marshal(qaNew)

	startersOld, _ := json.Marshal([]string{"问题A"})
	startersNew, _ := json.Marshal([]string{"问题B", "问题C"})

	prev := &model.AgentDefinition{
		Name:                 "旧名",
		Description:          "旧描述",
		WelcomeMessage:       "旧欢迎语",
		IsActive:             true,
		Starters:             datatypes.JSON(startersOld),
		QuestionnaireAnswers: datatypes.JSON(rawOld),
	}
	curr := &model.AgentDefinition{
		Name:                 "新名",
		Description:          "新描述",
		WelcomeMessage:       "新欢迎语",
		IsActive:             true,
		Starters:             datatypes.JSON(startersNew),
		QuestionnaireAnswers: datatypes.JSON(rawNew),
	}

	got := ComputeChangesSummary(prev, curr, 0)
	assert.LessOrEqual(t, len(got), 200, "summary must not exceed 200 characters")
	if len(got) == 200 {
		assert.True(t, strings.HasSuffix(got, "..."), "truncated summary should end with ...")
	}
}
