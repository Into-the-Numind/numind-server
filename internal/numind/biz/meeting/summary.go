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

// summaryChatFn 是非流式 LLM 调用注入点（生产用 aiservice.Chat；单测可替换）。
var summaryChatFn = aiservice.Chat

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
