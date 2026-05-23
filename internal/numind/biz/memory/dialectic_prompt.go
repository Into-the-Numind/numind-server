package memory

import (
	"strings"

	"numind-server/internal/pkg/model"
)

// buildDialecticPrompt renders the Layer A dialectic input for agent.dialectic.
//
// The output is one user-role message — the dialectic LLM (qwen-plus per D4)
// reads it and produces a 100–800 rune Chinese narrative describing the
// **agent user themselves** (the sales rep / SOP operator / data analyst / PPT
// clerk / etc. who uses the agent). The output is NOT about whatever
// customer / dataset / document / production-line the user happens to be
// discussing — that is V2 Layer B scope, schema-reserved via subject_id but
// not implemented in V1.5 (see context.md §13 + task-07-dialectic.md §⚠️ 范围声明).
//
// Prompt design notes:
//   - Wording everywhere reads "使用者本人" (the user themselves) / "该使用者..."
//     to push the LLM away from confusing the picture with conversation subjects.
//   - Multi-scenario examples (sales / data analyst / SOP operator / PPT clerk)
//     in the few-shot guard against "all users must be sales reps" drift.
//   - No markdown headers in output (`【使用者画像】` prefix is added by
//     BuildInsightSection at injection time, not by the LLM).
//   - Output budget: ≤500 字 Chinese (validInsight clamps 100–800 runes for
//     defence-in-depth against runaway / truncated responses).
//   - Third person ("该使用者...") avoids first/second-person leakage that would
//     confuse the system prompt readers (Layer A is a description, not a reply).
//
// facts is the top-N (≤20) candidate fact set, expected ordered by importance
// DESC then recency DESC then confidence DESC (caller's responsibility — see
// dialecticService.recomputeInsightSafe). Each fact is rendered as a numbered
// bullet with [category, conf=0.XX] prefix so the LLM can weigh signals.
func buildDialecticPrompt(facts []model.UserMemoryFact) string {
	var b strings.Builder
	b.Grow(2048 + len(facts)*128)

	b.WriteString("你是使用者画像分析师。基于该 user 的 facts（**关于使用者本人的画像，不是使用者关注对象**），推理：\n")
	b.WriteString("1. 该使用者是谁（工作角色 / 行业背景 / 专业领域）\n")
	b.WriteString("2. 个人化对待该使用者的方式（沟通风格偏好 / 回答详略 / 格式偏好 / 节奏快慢）\n")
	b.WriteString("3. 当前会话基于该使用者风格的最佳建议\n\n")

	b.WriteString("**注意：不要描述使用者关注的客户 / 数据集 / 文档 / 产线等对象**——那是 V2 Layer B 的事，本 task 不做。\n\n")

	b.WriteString("**多场景画像参考**（你的输出应当贴近以下风格，但不要照抄）：\n")
	b.WriteString("- 销售员场景：「该使用者是中级医疗销售，对效率敏感。当前对话节奏快，建议主动给出可执行方案而非选项陈列。该使用者偏好简洁话术，不需要长 hand-holding。」\n")
	b.WriteString("- 数据分析师场景：「该使用者是金融数据分析师，主语言 Python + Pandas，习惯 Jupyter 风格工作流。常处理时序数据。建议直接给可运行代码片段，不要先讨论算法选型。该使用者讨厌冗长解释。」\n")
	b.WriteString("- SOP 操作员场景：「该使用者负责制造业质检流程，每天处理 30+ SOP 任务。偏好步骤化 + 图表辅助。对导出格式敏感（Excel 不是 CSV）。建议主动确认产线 ID 后再执行具体步骤。」\n")
	b.WriteString("- PPT 文员场景：「该使用者是投资关系部文员，常做 pitch deck。偏极简风格（不要 emoji / 不要 marketing 套话）。习惯 16:9 + 中英对照。建议主动询问目标观众（投资人 / 内部 / 客户）以调整语调。」\n\n")

	b.WriteString("要求：\n")
	b.WriteString("1. 不要复述 facts——在 facts 之上做推理\n")
	b.WriteString("2. 输出 2-3 段：\n")
	b.WriteString("   - 第 1 段：该使用者的核心特征（工作角色 + 行业 + 偏好概要，30-80 字）\n")
	b.WriteString("   - 第 2 段：与该使用者交互的最佳风格（沟通方式 / 回答格式 / 节奏，50-100 字）\n")
	b.WriteString("   - 第 3 段：当前重点 / 个人化建议（如适用，比如\"该使用者讨厌冗长解释，建议直接给可执行方案\"）\n")
	b.WriteString("3. 严格 ≤ 500 字\n")
	b.WriteString("4. 用第三人称（\"该使用者...\"）\n")
	b.WriteString("5. 不要标\"画像\"等元词，不要使用 markdown 标题\n\n")

	b.WriteString("使用者事实（按 importance + 时效 + confidence 排序，皆为关于该使用者本人的事实）：\n")
	for i, f := range facts {
		// "[category, conf=0.NN] content" — keep one fact per line, numbered.
		// The Content was EscapeForStorage'd at write time; the LLM will see
		// `&lt;` / `&gt;` literals. That's acceptable noise — the dialectic
		// output is plain narrative, never re-parsed as HTML.
		_, _ = b.WriteString(formatDialecticFactLine(i+1, f))
	}

	b.WriteString("\n输出（无前缀、无标题，纯文本）：")
	return b.String()
}

// formatDialecticFactLine renders one fact bullet for the dialectic prompt.
//
// Format: "<N>. [<category>, conf=<0.NN>] <content>\n"
// Example: "1. [context, conf=0.95] 用户是医疗销售\n"
//
// Note: confidence is formatted with 2 decimals to match the spec template
// and to give the LLM consistent signal granularity across facts.
func formatDialecticFactLine(n int, f model.UserMemoryFact) string {
	var b strings.Builder
	b.Grow(len(f.Content) + 32)
	_, _ = b.WriteString(itoaSmall(n))
	b.WriteString(". [")
	b.WriteString(f.Category)
	b.WriteString(", conf=")
	b.WriteString(formatConfidence(f.Confidence))
	b.WriteString("] ")
	b.WriteString(f.Content)
	b.WriteString("\n")
	return b.String()
}

// itoaSmall is a tiny stack-only integer formatter for the 1..20 range we hit
// in dialectic prompts. Avoids strconv import + allocation for a hot path
// that runs once per dialectic recompute.
func itoaSmall(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	// 10..99 covers TopFactsLimit default 20.
	tens := n / 10
	ones := n % 10
	return string([]rune{rune('0' + tens), rune('0' + ones)})
}

// formatConfidence renders a confidence value as "0.NN" (always 2 decimals).
//
// Manual format avoids fmt import + reflection overhead on the prompt build
// hot path. Confidence is in [0.00, 1.00] (validateExtractedFact enforces
// ≥ 0.70 at extract time, so practical range is [0.70, 1.00]).
func formatConfidence(c float64) string {
	// Clamp defensively: rounded to 2 dp.
	if c < 0 {
		c = 0
	}
	if c > 1 {
		c = 1
	}
	// Round to nearest 0.01.
	hundredths := int(c*100 + 0.5)
	whole := hundredths / 100
	frac := hundredths % 100
	tens := frac / 10
	ones := frac % 10
	return string([]rune{rune('0' + whole), '.', rune('0' + tens), rune('0' + ones)})
}
