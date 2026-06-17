package meeting

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

// summaryMaxTranscriptRunes 纪要生成读取的转写上限（字）。超过则按 SPEC §3 end
// 「转写过长时自行 window/分块」做尾部截断 + 头部摘要保留（这里取「全文截断保留首尾」简单策略）。
const summaryMaxTranscriptRunes = 12000

// summaryMaxOutputTokens 纪要最大输出 token（结构化要点/决议/待办，给足空间）。
const summaryMaxOutputTokens = 2000

// rollingSummaryMaxOutputTokens 滚动摘要最大输出 token（结构化 running memory，FEEDBACK_V2_SPEC §2.4）。
const rollingSummaryMaxOutputTokens = 1500

// rollingDeltaMaxRunes 单次折叠喂给模型的「新增转写」上限（字）。节流游标到点时积累的增量通常
// 在 ~1500 字附近，留余量兜底超长（极端情况下取尾部）。
const rollingDeltaMaxRunes = 6000

// summaryChatFn 是非流式 LLM 调用注入点（生产用 aiservice.Chat；单测可替换）。
var summaryChatFn = aiservice.Chat

// rollingSummarySystemPrompt 指示模型把「已有滚动摘要 + 新增转写」折叠成更新后的结构化摘要
// （FEEDBACK_V2_SPEC §2.4）。结构化 markdown 抗漂移：四个固定二级标题。
const rollingSummarySystemPrompt = `你是会议实时记录助理，负责维护一份「滚动摘要」(running memory)，随会议推进持续更新它，让阅读者无需看全部逐字稿就能掌握整场脉络。

我会给你「已有滚动摘要」和「自上次更新以来的新增对话」。请把新增信息合并进已有摘要，输出**更新后**的完整滚动摘要（不是只输出增量），使用 markdown 格式，严格保留以下四个二级标题章节（即使某章节暂无内容也保留标题并写「（暂无）」）：

## 会议主题/目标
本次会议要讨论或达成的核心目标。

## 已确立的事实与决议
已经形成共识、确认的事实或做出的决定。

## 各方立场/诉求
不同参与方的观点、立场、关切与诉求。

## 未决问题/待办
尚未解决的分歧、悬而未决的问题、后续行动项。

要求：合并而非堆砌——把同主题信息整合、去重、提炼；保留关键事实与决议不丢失；保持简洁。只输出更新后的滚动摘要 markdown 本身，不要任何前后缀、解释或代码块围栏。`

// updateRunningSummary 把「已有 running_summary + 新增转写」折叠成更新后的结构化滚动摘要
// （FEEDBACK_V2_SPEC §2.2/§2.4）。这是一次 cheap 非流式 LLM 调用，复用已注册的 chatbot.stream
// profile（禁新注册），走 internalCallCtx 剥离扣费 + 会员门 + Langfuse 观测。
//
// 入参：
//   - prev：已有滚动摘要（首次为空）；
//   - deltaTranscript：自上次折叠以来的新增转写（已拼好的文本）。
//
// 返回更新后的摘要 markdown。deltaTranscript 为空白时直接返回 prev（无新内容，不调 LLM）。
// 由调用方（realtime 后台 goroutine）负责持久化与并发控制；本函数纯粹是 LLM 折叠逻辑，无副作用。
func (b *meetingBiz) updateRunningSummary(ctx context.Context, userID uint, sessionID uint64, prev, deltaTranscript string) (string, error) {
	delta := strings.TrimSpace(deltaTranscript)
	if delta == "" {
		return prev, nil
	}
	// 兜底超长增量：只保留尾部（最近的内容最相关，旧内容多已折进 prev）。
	if dr := []rune(delta); len(dr) > rollingDeltaMaxRunes {
		delta = string(dr[len(dr)-rollingDeltaMaxRunes:])
	}

	callCtx := internalCallCtx(ctx, "meeting.rolling_summary")

	// Langfuse trace（同 generateSummary 约定，优雅降级）。
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "meeting-rolling-summary",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("meeting"),
	)
	callCtx = langfuse.WithTrace(callCtx, traceID)

	tc := langfuse.FromContext(callCtx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("meeting-rolling-summary"),
			langfuse.WithGenInput(map[string]interface{}{
				"session_id": sessionID,
				"prev_size":  len([]rune(prev)),
				"delta_size": len([]rune(delta)),
				"has_prev":   strings.TrimSpace(prev) != "",
			}),
		)
	}

	prevForPrompt := strings.TrimSpace(prev)
	if prevForPrompt == "" {
		prevForPrompt = "（暂无，这是本次会议的第一段摘要，请基于新增对话直接建立结构化摘要。）"
	}
	userMsg := fmt.Sprintf("【已有滚动摘要】\n%s\n\n【自上次更新以来的新增对话】\n%s", prevForPrompt, delta)

	// 复用 profile.ChatbotStream 做非流式 Chat（同 generateSummary 的理由，见下方注释）。
	resp, err := summaryChatFn(callCtx, profile.ChatbotStream, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: rollingSummarySystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg}},
		},
		MaxTokens:   rollingSummaryMaxOutputTokens,
		Temperature: 0.3,
	})
	if err != nil {
		endGenError(tc, genID, err)
		return "", fmt.Errorf("updateRunningSummary: LLM call failed: %w", err)
	}

	updated := strings.TrimSpace(resp.Content)
	if updated == "" {
		endGenError(tc, genID, fmt.Errorf("empty rolling summary content"))
		return "", fmt.Errorf("updateRunningSummary: empty summary")
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenOutput(updated),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
	}
	return updated, nil
}

// summarySystemPrompt 指示模型生成结构化 markdown 纪要（要点 / 决议 / 待办，SPEC §0.3 / §3）。
const summarySystemPrompt = `你是专业的会议纪要助理。请阅读下面完整的会议转写，输出一份结构化的中文会议纪要，使用 markdown 格式，严格包含以下三个二级标题章节（即使某章节无内容也保留标题并写「（无）」）：

## 要点
用无序列表概括会议讨论的关键信息与主要观点。

## 决议
用无序列表列出会议达成的结论或决定；没有则写「（无）」。

## 待办
用无序列表列出后续行动项，尽量标注负责人与时限；没有则写「（无）」。

只输出纪要 markdown 本身，不要任何前后缀、解释或代码块围栏。`

// generateSummary 依据会话转写生成结构化纪要 markdown（SPEC §3 end：同步生成）。
//
// 返回 (markdown, error)。无转写内容时返回一段降级占位纪要（不调 LLM），保证 end 流程总有
// 可展示的 summary。调用方（EndSession）负责把结果写回 session.Summary / summary_status。
//
// 计费纪律：走 aiservice.Chat（UsageRecord 自动记录），用 internalCallCtx 剥离扣费 + 会员门；
// 不设 ContextFragments → 网关无 fragment 直通，零三池变动。
func (b *meetingBiz) generateSummary(ctx context.Context, userID uint, s *model.MeetingSession, segs []model.MeetingSegment) (string, error) {
	transcript := joinTranscript(segs, summaryMaxTranscriptRunes)
	if strings.TrimSpace(transcript) == "" {
		// 无有效转写：降级占位纪要，不调 LLM。
		return emptyTranscriptSummary(), nil
	}

	callCtx := internalCallCtx(ctx, "meeting.summary")

	// 创建 Langfuse trace（SPEC §4）：internalCallCtx 不注入 trace，故此处显式创建，
	// 否则 FromContext 恒 nil、generation 永不落库。userID 用于可观测归属（与 billing
	// 的 userID=0 隔离，仅标注 trace owner，不触发会员门/扣费）。优雅降级：Langfuse 关闭时
	// CreateTrace 为 no-op，FromContext 仍返回 nil。
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "meeting-summary",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("meeting"),
	)
	callCtx = langfuse.WithTrace(callCtx, traceID)

	tc := langfuse.FromContext(callCtx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("meeting-summary"),
			langfuse.WithGenInput(map[string]interface{}{
				"session_id":      s.ID,
				"transcript_size": len([]rune(transcript)),
			}),
		)
	}

	// 故意复用 profile.ChatbotStream 做非流式 Chat 调用：SPEC §1 明令「复用现有 task profile，
	// 禁止新注册」，且指明挑「chatbot 用的那个」。chatbot.stream 是唯一已在 DB 注册的通用 chat
	// profile，网关对其路由的模型既支持流式也支持非流式调用（aiservice.Chat 走非流式路径），
	// 因此命名虽含 Stream，用于此处的同步纪要生成是安全的。
	resp, err := summaryChatFn(callCtx, profile.ChatbotStream, aiservice.ChatRequest{
		// 不设 ContextFragments —— 保持网关无 fragment 直通分支，不 Reserve/Reconcile。
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: summarySystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: fmt.Sprintf("会议标题：%s\n\n完整转写：\n\n%s", s.Title, transcript)}},
		},
		MaxTokens:   summaryMaxOutputTokens,
		Temperature: 0.3,
	})
	if err != nil {
		endGenError(tc, genID, err)
		return "", fmt.Errorf("generateSummary: LLM call failed: %w", err)
	}

	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		endGenError(tc, genID, fmt.Errorf("empty summary content"))
		return "", fmt.Errorf("generateSummary: empty summary")
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenOutput(summary),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
	}
	return summary, nil
}

// finalSummaryFromRollingSystemPrompt 指示模型基于滚动摘要 + 尾部增量转写生成最终结构化纪要
// （FEEDBACK_V2_SPEC §3.1）。复用与一次性纪要相同的三章节结构（要点/决议/待办），只是输入是
// 已折叠的滚动摘要而非全稿——近乎瞬时。
const finalSummaryFromRollingSystemPrompt = `你是专业的会议纪要助理。我会给你一份「会议滚动摘要」（已结构化记录了整场脉络）以及「会议结尾的最新对话」（可能尚未折进摘要）。请综合两者，输出一份最终的中文会议纪要，使用 markdown 格式，严格包含以下三个二级标题章节（即使某章节无内容也保留标题并写「（无）」）：

## 要点
用无序列表概括会议讨论的关键信息与主要观点。

## 决议
用无序列表列出会议达成的结论或决定；没有则写「（无）」。

## 待办
用无序列表列出后续行动项，尽量标注负责人与时限；没有则写「（无）」。

只输出纪要 markdown 本身，不要任何前后缀、解释或代码块围栏。`

// generateFinalSummary 生成会话的最终结构化纪要（FEEDBACK_V2_SPEC §3.1）。
//
// 优先路径：若 session.RunningSummary 非空，基于「滚动摘要 + 尾部未折叠转写」生成纪要——近乎
// 瞬时（输入小），不读全稿。回退路径：无 running_summary 时退回 generateSummary（读全稿，
// 即原有逻辑），保证旧会话/未触发过滚动摘要的会话仍能出纪要。
//
// 计费/Langfuse 纪律同 generateSummary（internalCallCtx + 显式 trace）。
func (b *meetingBiz) generateFinalSummary(ctx context.Context, userID uint, s *model.MeetingSession, segs []model.MeetingSegment) (string, error) {
	rolling := strings.TrimSpace(s.RunningSummary)
	if rolling == "" {
		// 回退：无滚动摘要，读全稿一次性生成（原 EndSession 同步路径逻辑）。
		return b.generateSummary(ctx, userID, s, segs)
	}

	// 尾部增量：滚动摘要可能落后于最后几句（折叠游标节流），把尾部转写一并喂给模型补齐。
	// 取最近 ~2000 字即可（滚动摘要已覆盖主体脉络）。
	tail, _ := buildTranscriptWindow(segs, 2000)

	callCtx := internalCallCtx(ctx, "meeting.summary")
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "meeting-summary",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("meeting"),
	)
	callCtx = langfuse.WithTrace(callCtx, traceID)

	tc := langfuse.FromContext(callCtx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("meeting-summary-from-rolling"),
			langfuse.WithGenInput(map[string]interface{}{
				"session_id":   s.ID,
				"rolling_size": len([]rune(rolling)),
				"tail_size":    len([]rune(tail)),
			}),
		)
	}

	tailForPrompt := strings.TrimSpace(tail)
	if tailForPrompt == "" {
		tailForPrompt = "（无新增，滚动摘要已是最新。）"
	}
	userMsg := fmt.Sprintf("会议标题：%s\n\n【会议滚动摘要】\n%s\n\n【会议结尾的最新对话】\n%s", s.Title, rolling, tailForPrompt)

	resp, err := summaryChatFn(callCtx, profile.ChatbotStream, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: finalSummaryFromRollingSystemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg}},
		},
		MaxTokens:   summaryMaxOutputTokens,
		Temperature: 0.3,
	})
	if err != nil {
		endGenError(tc, genID, err)
		return "", fmt.Errorf("generateFinalSummary: LLM call failed: %w", err)
	}

	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		endGenError(tc, genID, fmt.Errorf("empty summary content"))
		return "", fmt.Errorf("generateFinalSummary: empty summary")
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenOutput(summary),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
	}
	return summary, nil
}

// joinTranscript 把全部分段文本按时间顺序拼成完整转写；超过 maxRunes 时保留首尾、中间省略
// （SPEC §3「转写过长时实现者自行 window/分块」——首尾摘要法保留开场与结尾上下文）。
func joinTranscript(segs []model.MeetingSegment, maxRunes int) string {
	parts := make([]string, 0, len(segs))
	for i := range segs {
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	full := strings.Join(parts, "\n")
	r := []rune(full)
	if len(r) <= maxRunes {
		return full
	}
	// 首尾截断：前 60% + 省略标记 + 后 40%，保留开场与结尾。
	head := maxRunes * 6 / 10
	tail := maxRunes - head
	return string(r[:head]) + "\n\n……（转写过长，中间部分已省略）……\n\n" + string(r[len(r)-tail:])
}

// emptyTranscriptSummary 无转写时的降级占位纪要（保持结构一致，前端可正常渲染）。
func emptyTranscriptSummary() string {
	return "## 要点\n- （本次会议没有可用的转写内容）\n\n## 决议\n- （无）\n\n## 待办\n- （无）"
}
