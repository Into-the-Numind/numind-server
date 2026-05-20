package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// WriteHistorySnapshot marshals the current agent_definition row to JSON and
// inserts a new agent_definition_history row. Caller should run inside a
// transaction together with the agent_definition mutation.
//
// createdBy is the user performing this operation (may differ from
// ad.CreatedBy which records the original agent creator).
func WriteHistorySnapshot(ctx context.Context, tx *gorm.DB, ad *model.AgentDefinition, createdBy uint, summary string) error {
	snapshot, err := json.Marshal(ad)
	if err != nil {
		return fmt.Errorf("WriteHistorySnapshot marshal: %w", err)
	}
	h := &model.AgentDefinitionHistory{
		AgentID:        ad.ID,
		Version:        ad.Version,
		Snapshot:       datatypes.JSON(snapshot),
		ChangesSummary: summary,
		CreatedBy:      createdBy,
	}
	if err := tx.WithContext(ctx).Create(h).Error; err != nil {
		return fmt.Errorf("WriteHistorySnapshot create: %w", err)
	}
	return nil
}

// ComputeChangesSummary compares old and new AgentDefinition to produce a
// human-readable change summary (≤200 characters).
//
// Priority rules (first match wins):
//   - restoreSourceVersion > 0 → "从 v{N} 恢复"
//   - prev == nil              → "首次发布"
//   - AdvancedMode toggled on  → "切换到高级模式"
//   - IsActive toggled to false → "软删除"
//   - otherwise                → list changed Q field labels, or "更新"
func ComputeChangesSummary(prev, curr *model.AgentDefinition, restoreSourceVersion uint) string {
	if restoreSourceVersion > 0 {
		return fmt.Sprintf("从 v%d 恢复", restoreSourceVersion)
	}
	if prev == nil {
		return "首次发布"
	}
	if !prev.AdvancedMode && curr.AdvancedMode {
		return "切换到高级模式"
	}
	if prev.IsActive && !curr.IsActive {
		return "软删除"
	}

	// General diff: check direct fields and questionnaire answers.
	var changes []string

	if prev.Name != curr.Name {
		changes = append(changes, "Q1（名字）")
	}
	if prev.Description != curr.Description {
		changes = append(changes, "Q3（描述）")
	}
	if prev.WelcomeMessage != curr.WelcomeMessage {
		changes = append(changes, "Q4（欢迎语）")
	}
	if !jsonEqual(prev.Starters, curr.Starters) {
		changes = append(changes, "Q5（快速开始按钮）")
	}
	if !jsonEqual(prev.QuestionnaireAnswers, curr.QuestionnaireAnswers) {
		diff := diffQuestionnaire(prev.QuestionnaireAnswers, curr.QuestionnaireAnswers)
		changes = append(changes, diff...)
	}

	if len(changes) == 0 {
		return "更新"
	}

	summary := "修改了 " + strings.Join(changes, ", ")
	if len(summary) > 200 {
		summary = summary[:197] + "..."
	}
	return summary
}

// jsonEqual compares two datatypes.JSON values by their string representation.
// Both nil and empty JSON are treated as equal to an empty byte slice.
func jsonEqual(a, b datatypes.JSON) bool {
	return string(a) == string(b)
}

// diffQuestionnaire parses both QuestionnaireAnswers JSON blobs and returns a
// list of human-readable Q-label strings for fields that changed.
func diffQuestionnaire(a, b datatypes.JSON) []string {
	var qa, qb QuestionnaireAnswers
	_ = json.Unmarshal(a, &qa)
	_ = json.Unmarshal(b, &qb)

	var diffs []string
	if !stringSliceEqual(qa.Q6, qb.Q6) {
		diffs = append(diffs, "Q6（任务类型）")
	}
	if !stringSliceEqual(qa.Q7, qb.Q7) {
		diffs = append(diffs, "Q7（材料类型）")
	}
	if qa.Q8 != qb.Q8 {
		diffs = append(diffs, "Q8（积分上限）")
	}
	if qa.Q9 != qb.Q9 {
		diffs = append(diffs, "Q9（网络搜索）")
	}
	if qa.Q10 != qb.Q10 {
		diffs = append(diffs, "Q10（禁区）")
	}
	if qa.Q11 != qb.Q11 {
		diffs = append(diffs, "Q11（越界话术）")
	}
	if qa.Q12 != qb.Q12 {
		diffs = append(diffs, "Q12（说话风格）")
	}
	return diffs
}

// stringSliceEqual returns true if both slices have identical length and elements.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
