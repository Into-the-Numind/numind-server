package skill

import (
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// Build 把 agent_definition 的 questionnaire_answers JSON + 直接字段（name/description）
// 组装为 SKILL.md（generated_skill_body）。
//
// 错误：
//   - questionnaire_answers JSON parse fail → wrapped error
//   - q6/q7/q12 必填字段缺失 → errno.ErrSkillBuilderFailed.SetMessage("...")
func Build(ad *model.AgentDefinition) (string, error) {
	var qa QuestionnaireAnswers
	if len(ad.QuestionnaireAnswers) > 0 {
		if err := json.Unmarshal(ad.QuestionnaireAnswers, &qa); err != nil {
			return "", fmt.Errorf("Build: parse questionnaire: %w", err)
		}
	}

	// 必填校验 — 比 unmarshal 后做（让 nil/empty 都走同一路径）
	if len(qa.Q6) == 0 {
		return "", errno.ErrSkillBuilderFailed.SetMessage("questionnaire.q6 required")
	}
	if len(qa.Q7) == 0 {
		return "", errno.ErrSkillBuilderFailed.SetMessage("questionnaire.q7 required")
	}
	if qa.Q12 == "" {
		return "", errno.ErrSkillBuilderFailed.SetMessage("questionnaire.q12 required")
	}

	var b strings.Builder

	b.WriteString("## 角色定义\n你的名字是「")
	b.WriteString(ad.Name)
	b.WriteString("」。\n你的核心职责：")
	b.WriteString(ad.Description)
	b.WriteString("\n\n")

	b.WriteString("## 任务类型\n")
	for _, t := range qa.Q6 {
		b.WriteString("- ")
		b.WriteString(taskTypeDisplay(t))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 输入材料类型\n")
	for _, m := range qa.Q7 {
		b.WriteString("- ")
		b.WriteString(materialTypeDisplay(m))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 语气风格\n")
	b.WriteString(styleDisplay(qa.Q12))
	b.WriteString("\n\n")

	if qa.Q10 != "" {
		b.WriteString("## 禁区（软规则）\n")
		b.WriteString(qa.Q10)
		b.WriteString("\n\n")
	}

	if qa.Q11 != "" {
		b.WriteString("## 越界处理策略\n")
		b.WriteString("当学员的问题超出范围时，请回复：")
		b.WriteString(qa.Q11)
		b.WriteString("\n")
	}

	return b.String(), nil
}
