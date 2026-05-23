package memory

import (
	"fmt"
	"strings"
)

// ExtractorMaxMessages is the cap on conversation turns sent to the LLM.
// Spec §"抽取后处理": lastN(msgs, 10) — control token cost.
const ExtractorMaxMessages = 10

// extractorMaxContentRunes truncates each individual message to ~200 chinese chars
// to defend against pathological long tool outputs blowing up the prompt.
// Counted in runes, not bytes, to handle UTF-8 properly.
const extractorMaxContentRunes = 200

// ExtractionPromptSystem drives the per-turn async extraction LLM call.
//
// Layer A scope: facts here are about the **agent user themselves** — a sales
// rep, a data analyst, an SOP operator on a factory floor, a clerk authoring
// investor-relations slides, a customer support lead, etc. They are never
// about the customer/subject the user discusses (that's V2 Layer B).
//
// Example cohort (intentionally diverse — do NOT bias the model toward sales):
//   - "用户是数据分析师，主用 Python 做指标可视化"  → context
//   - "用户是制造业 SOP 操作员，偏好步骤化指令"      → behavior
//   - "用户做投资关系 PPT，偏极简风格，禁用 emoji"   → preference
//   - "用户是客服主管，希望先给结论再列原因"          → preference
//   - "用户的客户群在 B2B SaaS"                       → context
//   - "用户上次被领导提醒少用感叹号"                   → correction
//
// 6 categories — keep this list aligned with model.MemoryFactCategory* enums:
//
//	preference  — 用户偏好/喜好/风格（"我喜欢..."/"我讨厌..."）
//	knowledge   — 用户具备的领域知识（"我懂 Python"/"我做了10年销售"）
//	context     — 用户的工作背景/角色（"我是XX分析师"/"我们公司在做XX"）
//	behavior    — 用户的常见行为模式（"我通常先做XX再做YY"）
//	goal        — 用户的长期目标/在追求的事（"我想转型做产品"）
//	correction  — 用户明确指出"以后别再XX"的反馈（一次说不够，下次不能再犯）
//
// Confidence scale (4-tier, mapped to threshold ≥ 0.70):
//
//	0.95 - 1.00  → 用户明确陈述（"我是数据分析师"）
//	0.80 - 0.94  → 强推断（多轮一致信号 / 上下文极强）
//	0.70 - 0.79  → 中等推断（单次信号 / 一定 context）
//	< 0.70       → 不输出（噪声 / 暂时性话题 / 工具调用结果）
//
// Output: strict JSON array, NO surrounding text, NO markdown fence.
const ExtractionPromptSystem = `你是一个对话事实观察员。读以下用户/助手对话，识别**对未来对话长期有用**的、关于"使用者本人"的事实。

【画像对象】
- 抽取范围 = **正在使用 agent 的真实用户本人**（如销售员/数据分析师/SOP 操作员/PPT 文员/客服主管等任意场景）
- ❌ 不抽取关于用户**讨论或处理的对象**（客户/数据集/产线/PPT 受众）—— 那是另一个系统的工作

【6 类合法 category】
1. preference — 用户偏好/喜好/风格（"我喜欢..."/"我讨厌..."）
   例："用户偏极简风格，PPT 禁用 emoji"
2. knowledge — 用户具备的领域知识/技能
   例："用户主用 Python 做数据可视化"
3. context — 用户的工作背景/角色/所属团队
   例："用户是制造业质检流程 SOP 操作员"
4. behavior — 用户的稳定行为模式
   例："用户通常先列大纲再展开正文"
5. goal — 用户在追求的长期目标
   例："用户在准备转型做产品经理"
6. correction — 用户明确指出"以后别再 XX"的回退反馈
   例："用户已明确要求减少使用感叹号"

【confidence 评分】
- 0.95-1.00 = 明确陈述（"我是 XX"）
- 0.80-0.94 = 强推断（多轮一致信号）
- 0.70-0.79 = 中等推断（单次信号 + 上下文）
- 低于 0.70 = 不输出（噪声）

【排除项 — 一律不抽】
- 临时问题、一次性请求、闲聊
- 系统行为、工具调用结果、错误码
- 用户讨论的客户/数据集/产线等"对象"（不是用户本人）

【输出格式 — 严格 JSON 数组】
[
  {"content": "<≤80字>", "category": "<6 类之一>", "confidence": <0.70-1.00>},
  ...
]

无 facts 时输出 []。不要有任何其他文字、markdown 围栏、注释。`

// ExtractedFact is the per-item DTO emitted by the LLM.
// Matches the JSON shape above plus optional source tracking added by the worker.
type ExtractedFact struct {
	Content           string  `json:"content"`
	Category          string  `json:"category"`
	Confidence        float64 `json:"confidence"`
	SourceMessageUUID string  `json:"source_message_uuid,omitempty"`
}

// buildExtractionPrompt serialises the last ExtractorMaxMessages messages into
// a single user-side prompt string for the LLM call.
//
// Behaviour:
//   - Take the last min(len(msgs), ExtractorMaxMessages) entries
//   - For each: prefix [<role>] then content, truncated to extractorMaxContentRunes
//   - Join with "\n\n"
//   - Empty input → returns "(no conversation)" to defend against degenerate input
func buildExtractionPrompt(msgs []ChatMessage) string {
	if len(msgs) == 0 {
		return "(no conversation)"
	}
	start := 0
	if len(msgs) > ExtractorMaxMessages {
		start = len(msgs) - ExtractorMaxMessages
	}
	tail := msgs[start:]

	var b strings.Builder
	b.Grow(len(tail) * 256)
	for i, m := range tail {
		if i > 0 {
			b.WriteString("\n\n")
		}
		// Normalise unknown roles to "user" to avoid leaking internal state names.
		role := m.Role
		if role == "" {
			role = "user"
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("] ")
		b.WriteString(truncateRunes(m.Content, extractorMaxContentRunes))
	}
	return b.String()
}

// truncateRunes truncates s to at most maxRunes runes; if truncated, appends "..".
// Properly handles UTF-8 / Chinese characters (one CJK char = one rune).
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + ".."
}

// ChatMessage is the minimal {role, content} pair the extractor consumes.
//
// Distinct from the existing memory.Message type (which is used for SyncTurn);
// keeping a separate name avoids ambiguity when call sites convert
// agent_run.messages JSON into the extractor's input. Callers may construct
// either: `[]ChatMessage{{Role: "user", Content: "..."}}` or convert from
// `memory.Message` directly (identical layout).
//
// NOTE: This type intentionally lives in extractor_prompt.go (not types.go)
// because it is part of the extractor's input contract, not the broader
// memory provider surface area.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// validateExtractedFact applies the threshold + category + content checks.
// Returns the canonical fact or a non-nil error explaining why it was dropped.
func validateExtractedFact(f ExtractedFact, minConfidence float64) error {
	if strings.TrimSpace(f.Content) == "" {
		return fmt.Errorf("empty content")
	}
	if f.Confidence < minConfidence {
		return fmt.Errorf("confidence %.2f below threshold %.2f", f.Confidence, minConfidence)
	}
	if f.Confidence > 1.0 {
		return fmt.Errorf("confidence %.2f exceeds 1.00", f.Confidence)
	}
	if !isValidCategory(f.Category) {
		return fmt.Errorf("invalid category %q (allowed: preference/knowledge/context/behavior/goal/correction)", f.Category)
	}
	return nil
}

// isValidCategory checks against the 6 allowed memory fact categories.
//
// Kept in-line (not imported from model package) to avoid an import cycle —
// model.AllMemoryFactCategories already enumerates these and the values
// must stay byte-identical with the DB CHECK constraint
// chk_umf_category (see 20260523_140000_memory_schema.sql).
func isValidCategory(c string) bool {
	switch c {
	case "preference", "knowledge", "context", "behavior", "goal", "correction":
		return true
	}
	return false
}
