package compliance

import (
	"encoding/json"

	"numind-server/internal/pkg/model"
)

// SkillSoftRules — L2 从 agent_definition.questionnaire_answers Q10/Q11 提取
type SkillSoftRules struct {
	CautionTopics   string // Q10 注意话题（自然语言）
	OutOfScopeReply string // Q11 越界话术（用作 deny 时 NarrationMsg）
}

// ExtractFromAgentDef — 从 agent_definition 读 Q10/Q11
// fail-soft：ad nil / 缺失字段 / JSON 解析失败 → 零值 SkillSoftRules
func ExtractFromAgentDef(ad *model.AgentDefinition) SkillSoftRules {
	if ad == nil || len(ad.QuestionnaireAnswers) == 0 {
		return SkillSoftRules{}
	}
	var qa struct {
		Q10 string `json:"q10"`
		Q11 string `json:"q11"`
	}
	if err := json.Unmarshal(ad.QuestionnaireAnswers, &qa); err != nil {
		return SkillSoftRules{}
	}
	return SkillSoftRules{
		CautionTopics:   qa.Q10,
		OutOfScopeReply: qa.Q11,
	}
}

// NarrationOrDefault — Q11 非空走 Q11，否则 DefaultOutOfScopeNarration
func (r SkillSoftRules) NarrationOrDefault() string {
	if r.OutOfScopeReply != "" {
		return r.OutOfScopeReply
	}
	return DefaultOutOfScopeNarration
}
