package skill

import (
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/pkg/model"
)

// Build 把 agent_definition 的 questionnaire_answers JSON + 直接字段（name/description）
// 组装为 SKILL.md。
//
// T1（2026-06-17）起 Build 不再服务用户建 Agent 流程（问卷模式已删，用户直接写
// SystemPrompt）。现仅作内部「模板编译器」：artifact.ImportTemplate 把 seed 模板的
// questionnaire_answers 编译为技能 body。属 T4 技能/市场重设计范畴。
//
// Build 是纯 transformer——Q6/Q7/Q12 等字段缺失时对应段落省略不渲染，不返回必填
// 校验错误（参见 TestBuild_MissingQ6/Q7/Q12_succeedsAndOmitsSection 与
// TestBuild_NilQuestionnaireAnswers_succeeds）。
//
// 错误：
//   - questionnaire_answers JSON parse fail → wrapped error
func Build(ad *model.AgentDefinition) (string, error) {
	var qa QuestionnaireAnswers
	if len(ad.QuestionnaireAnswers) > 0 {
		if err := json.Unmarshal(ad.QuestionnaireAnswers, &qa); err != nil {
			return "", fmt.Errorf("Build: parse questionnaire: %w", err)
		}
	}

	var b strings.Builder

	b.WriteString("## 角色定义\n你的名字是「")
	b.WriteString(ad.Name)
	b.WriteString("」。\n你的核心职责：")
	b.WriteString(ad.Description)
	b.WriteString("\n\n")

	if len(qa.Q6) > 0 {
		b.WriteString("## 任务类型\n")
		for _, t := range qa.Q6 {
			b.WriteString("- ")
			b.WriteString(taskTypeDisplay(t))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(qa.Q7) > 0 {
		b.WriteString("## 输入材料类型\n")
		for _, m := range qa.Q7 {
			b.WriteString("- ")
			b.WriteString(materialTypeDisplay(m))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if qa.Q12 != "" {
		b.WriteString("## 语气风格\n")
		b.WriteString(styleDisplay(qa.Q12))
		b.WriteString("\n\n")
	}

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
