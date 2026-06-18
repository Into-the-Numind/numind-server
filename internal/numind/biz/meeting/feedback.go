package meeting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// noFeedbackSentinel 是判官「此刻无需反馈」的哨兵标记（SPEC §3.1）。
// auto 触发时模型若以此开头 → 发 skip 事件、不落库。
const noFeedbackSentinel = "NO_FEEDBACK"

// feedbackRecentWindow 反馈判官看到的「最近逐字对话」时间窗口（FEEDBACK_V2_SPEC §2.3：
// 最近 5 分钟，按 segment.created_at 取）。
const feedbackRecentWindow = 5 * time.Minute

// feedbackRecentMaxRunes 最近 5 分钟窗口的安全字数上限（FEEDBACK_V2_SPEC §2.3：~8000 字
// 兜底；超长只保留尾部——最新的对话最相关，全局脉络已由滚动摘要覆盖）。
const feedbackRecentMaxRunes = 8000

// feedbackRecentFeedbackLimit 注入 prompt 的「已给过的反馈」最大条数（FEEDBACK_V2_SPEC §2.3：
// 最近 ~10 条，提示判官避免重复）。
const feedbackRecentFeedbackLimit = 10

// feedbackMaxOutputTokens 反馈正文最大输出 token（反馈应简洁可立即使用）。
const feedbackMaxOutputTokens = 800

// chatStreamFn 是流式 LLM 调用注入点（生产用 aiservice.ChatStream；单测可替换）。
var chatStreamFn = aiservice.ChatStream

// GenerateFeedback 生成一次反馈（SPEC §3.1，单次 LLM 调用兼判官+生成）：
//
//   - trigger=auto：系统提示给「可选 NO_FEEDBACK」；用 sentinel 缓冲首段判断。模型若输出
//     以 NO_FEEDBACK 开头 → 发 skip，不落库；否则流式 token → 落库 → done。
//   - trigger=manual：系统提示要求「必须给出反馈」（不提供 NO_FEEDBACK 选项），总是生成 →
//     落库 → done。
//
// 计费纪律：走 aiservice.ChatStream（UsageRecord 自动记录），用 internalCallCtx 剥离扣费 +
// 会员门；不设 ContextFragments → 网关无 fragment 直通，零三池变动。
//
// SSE 事件通过 h(eventType, data) 推送（token/skip/done/error）。h 返回 error 表示客户端
// 已断开，应尽快停止。
func (b *meetingBiz) GenerateFeedback(ctx context.Context, userID uint, sessionID uint64, req *FeedbackReq, h SSEHandler) error {
	trigger := req.Trigger
	if trigger != model.MeetingFeedbackTriggerAuto && trigger != model.MeetingFeedbackTriggerManual {
		return errno.ErrInvalidParameter.SetMessage("trigger 必须是 auto 或 manual")
	}

	s, err := b.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return err
	}

	// 取最近 5 分钟逐字窗口 + 当前锚点 seq（FEEDBACK_V2_SPEC §2.3）。
	segs, err := b.ds.Meetings().ListSegmentsBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("GenerateFeedback: list segments: %w", err)
	}
	recentTranscript, anchorSeq := buildRecentTranscriptWindow(segs, feedbackRecentWindow, feedbackRecentMaxRunes, time.Now())

	// 已给过的反馈（FEEDBACK_V2_SPEC §2.3：注入 prompt 让判官避免重复，最近 ~10 条）。
	priorFeedbacks, err := b.ds.Meetings().ListFeedbacksBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("GenerateFeedback: list feedbacks: %w", err)
	}

	// 三段上下文（FEEDBACK_V2_SPEC §2.3）：滚动摘要 + 最近 5 分钟逐字 + 已给反馈清单。
	userMsg := buildFeedbackUserMessage(s.RunningSummary, recentTranscript, priorFeedbacks, feedbackRecentFeedbackLimit)

	sysPrompt := buildFeedbackSystemPrompt(s.RolePrompt, trigger)
	callCtx := internalCallCtx(ctx, "meeting.feedback")

	// 创建 Langfuse trace（SPEC §4）：internalCallCtx 不注入 trace，故此处显式创建，
	// 否则 FromContext 恒 nil、generation 永不落库。userID 用于可观测归属（与 billing
	// 的 userID=0 隔离，仅标注 trace owner，不触发会员门/扣费）。优雅降级：Langfuse 关闭时
	// CreateTrace 为 no-op，FromContext 仍返回 nil。
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "meeting-feedback",
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
			langfuse.WithGenName("meeting-feedback"),
			langfuse.WithGenInput(map[string]interface{}{
				"trigger":              trigger,
				"anchor_seq":           anchorSeq,
				"recent_transcript":    recentTranscript,
				"has_running_summary":  strings.TrimSpace(s.RunningSummary) != "",
				"prior_feedback_count": len(priorFeedbacks),
			}),
		)
	}

	gatewayReq := aiservice.ChatRequest{
		// 不设 ContextFragments —— 保持网关无 fragment 直通分支，不 Reserve/Reconcile。
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: sysPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg}},
		},
		MaxTokens:   feedbackMaxOutputTokens,
		Temperature: 0.4,
		// 实时反馈要低延迟：显式关思考。配合 profile.MeetingFeedback 路由到的
		// deepseek-v4-flash(thinking_only=0) → 非思考、秒出。(pro 是 thinking_only 强制思考太慢)
		Thinking: false,
	}

	// profile.MeetingFeedback → deepseek-v4-flash(非思考)，独立于 chatbot.stream(pro)，
	// 切它不影响 chatbot 产品。滚动摘要/会后纪要仍用 chatbot.stream(见 summary.go)。
	ch, llmErr := chatStreamFn(callCtx, profile.MeetingFeedback, gatewayReq)
	if llmErr != nil {
		endGenError(tc, genID, llmErr)
		_ = h("error", map[string]string{"message": "反馈生成失败"})
		return fmt.Errorf("GenerateFeedback: LLM call failed: %w", llmErr)
	}

	content, skipped, streamErr := b.consumeFeedbackStream(ctx, ch, trigger, h)
	if streamErr != nil {
		endGenError(tc, genID, streamErr)
		_ = h("error", map[string]string{"message": "反馈生成中断"})
		return fmt.Errorf("GenerateFeedback: stream error: %w", streamErr)
	}

	// auto + 判官 skip：不落库，发 skip。
	if skipped {
		if tc != nil {
			langfuse.EndGeneration(tc.TraceID, genID,
				langfuse.WithGenOutput(map[string]string{"decision": "skip"}),
			)
		}
		return h("skip", map[string]string{"reason": "judge_no_feedback"})
	}

	// 落库 + done。
	fb := &model.MeetingFeedback{
		SessionID: sessionID,
		Trigger:   trigger,
		AnchorSeq: anchorSeq,
		Content:   content,
	}
	if err := b.ds.Meetings().CreateFeedback(ctx, fb); err != nil {
		endGenError(tc, genID, err)
		_ = h("error", map[string]string{"message": "反馈保存失败"})
		return fmt.Errorf("GenerateFeedback: create feedback: %w", err)
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenOutput(content),
		)
	}

	dto := toFeedbackDTO(fb)
	return h("done", dto)
}

// consumeFeedbackStream 消费 ChatStream 通道，按 trigger 应用 NO_FEEDBACK sentinel 判断。
//
// 返回 (content, skipped, err)：
//   - auto：缓冲流式增量直到能判断是否以 NO_FEEDBACK 开头。判定 skip → (空, true, nil)，
//     **不**向客户端发任何 token。判定非 skip → 把已缓冲内容连同后续增量作为 token 逐帧
//     推给客户端，返回完整正文。
//   - manual：不做 sentinel 判断，所有增量直接作为 token 流式推送。
func (b *meetingBiz) consumeFeedbackStream(
	ctx context.Context,
	ch <-chan aiservice.ChatChunk,
	trigger string,
	h SSEHandler,
) (content string, skipped bool, err error) {
	var buf strings.Builder // 完整正文累积
	var pending strings.Builder
	decided := trigger == model.MeetingFeedbackTriggerManual // manual 无需判定，直接流
	isAuto := trigger == model.MeetingFeedbackTriggerAuto

	// flushToken 把一段文本作为 token 帧推给客户端并累积到 buf。
	flushToken := func(text string) error {
		if text == "" {
			return nil
		}
		buf.WriteString(text)
		return h("token", text)
	}

	for chunk := range ch {
		if chunk.Delta != "" {
			if decided {
				if hErr := flushToken(chunk.Delta); hErr != nil {
					return "", false, hErr
				}
			} else {
				// auto 未判定：累积到 pending 直到能判断 sentinel。
				pending.WriteString(chunk.Delta)
				cur := pending.String()
				if d, isSkip := evalSentinel(cur, false); d {
					decided = true
					if isSkip {
						// 排空通道剩余 chunk（避免上游 goroutine 泄漏），返回 skip。
						drainChunks(ch)
						return "", true, nil
					}
					// 非 skip：把已缓冲的全部内容作为首个 token 帧推出。
					if hErr := flushToken(pending.String()); hErr != nil {
						return "", false, hErr
					}
					pending.Reset()
				}
			}
		}

		if chunk.IsFinal {
			if chunk.Err != nil {
				return "", false, chunk.Err
			}
			break
		}
	}

	// 流自然结束但 auto 仍未判定（输出过短，未触发提前判定）：用最终累积内容判定。
	if isAuto && !decided {
		cur := pending.String()
		_, isSkip := evalSentinel(cur, true)
		if isSkip {
			return "", true, nil
		}
		if hErr := flushToken(pending.String()); hErr != nil {
			return "", false, hErr
		}
	}

	final := strings.TrimSpace(buf.String())
	if final == "" {
		// 兜底：模型空输出且非 skip。manual 必须有反馈——返回错误让上层发 error。
		log.C(ctx).Warnw("meeting: feedback empty content", "trigger", trigger)
		return "", false, fmt.Errorf("empty feedback content")
	}
	return final, false, nil
}

// evalSentinel 判断当前累积文本 cur 是否足以判定 NO_FEEDBACK（SPEC §3.1：「以 NO_FEEDBACK
// 开头」）。
//
// 返回 (decided, isSkip)：
//   - 去掉前导空白后，若已确定以 noFeedbackSentinel 开头 → (true, true)。
//   - 若已积累足够字符确定**不**以 sentinel 开头（前缀不匹配）→ (true, false)。
//   - 否则信息不足，(false, false)，调用方继续缓冲。
//
// final=true 表示流已结束，必须立即给出判定（剩余不确定按非 skip 处理——宁可多给反馈）。
func evalSentinel(cur string, final bool) (decided bool, isSkip bool) {
	trimmed := strings.TrimLeft(cur, " \t\r\n")
	if trimmed == "" {
		// 全是空白，尚无实质内容。
		return final, false
	}

	// 已达到 sentinel 长度：精确判断是否以其开头。
	if len(trimmed) >= len(noFeedbackSentinel) {
		if strings.HasPrefix(trimmed, noFeedbackSentinel) {
			return true, true
		}
		return true, false
	}

	// 尚不足 sentinel 长度：检查现有部分是否仍是 sentinel 的前缀。
	if strings.HasPrefix(noFeedbackSentinel, trimmed) {
		// 仍可能是 NO_FEEDBACK 的前缀，需更多字符；流结束则按非 skip（内容太短不是有效哨兵）。
		return final, false
	}
	// 前缀不匹配，确定不是 NO_FEEDBACK。
	return true, false
}

// drainChunks 排空通道剩余 chunk（防止上游 billing/adapter goroutine 因无人消费而阻塞泄漏）。
func drainChunks(ch <-chan aiservice.ChatChunk) {
	for range ch {
	}
}

// buildFeedbackUserMessage 拼装发给判官的三段上下文（FEEDBACK_V2_SPEC §2.3）：
//
//	[会议滚动摘要]   —— running_summary（让模型据此了解整场脉络，避免幻觉）；无则「（暂无）」。
//	[最近 5 分钟对话] —— recentTranscript（最近 5 分钟逐字转写）；空则明确占位。
//	[你已经给过的反馈（不要重复）] —— 本会话最近 ~feedbackLimit 条反馈正文；无则「（暂无）」。
//
// priorFeedbacks 按 created_at ASC（store 保证），取尾部 feedbackLimit 条（最新的最相关）。
func buildFeedbackUserMessage(runningSummary, recentTranscript string, priorFeedbacks []model.MeetingFeedback, feedbackLimit int) string {
	summary := strings.TrimSpace(runningSummary)
	if summary == "" {
		summary = "（暂无）"
	}

	transcript := strings.TrimSpace(recentTranscript)
	if transcript == "" {
		transcript = "（目前还没有可用的会议转写内容。）"
	}

	priorBlock := formatPriorFeedbacks(priorFeedbacks, feedbackLimit)

	var sb strings.Builder
	sb.WriteString("[会议滚动摘要]\n")
	sb.WriteString(summary)
	sb.WriteString("\n\n[最近 5 分钟对话]\n")
	sb.WriteString(transcript)
	sb.WriteString("\n\n[你已经给过的反馈（不要重复）]\n")
	sb.WriteString(priorBlock)
	return sb.String()
}

// formatPriorFeedbacks 把已给反馈拼成清单（取尾部 limit 条，按时间顺序）。空时返回「（暂无）」。
func formatPriorFeedbacks(fbs []model.MeetingFeedback, limit int) string {
	// 取尾部 limit 条（store 已按 created_at ASC，尾部=最新）。
	start := 0
	if limit > 0 && len(fbs) > limit {
		start = len(fbs) - limit
	}
	var items []string
	for i := start; i < len(fbs); i++ {
		c := strings.TrimSpace(fbs[i].Content)
		if c == "" {
			continue
		}
		items = append(items, fmt.Sprintf("- %s", c))
	}
	if len(items) == 0 {
		return "（暂无）"
	}
	return strings.Join(items, "\n")
}

// buildFeedbackSystemPrompt 拼装系统提示（SPEC §3.1 + FEEDBACK_V2_SPEC §2.3）。
//   - auto：可输出 NO_FEEDBACK 表示此刻无需反馈。
//   - manual：必须给出反馈，不提供 NO_FEEDBACK 选项。
//
// 两种 trigger 都在 role_prompt 指令后追加「参考滚动摘要了解全局、不要重复已给反馈」一句
// （FEEDBACK_V2_SPEC §2.3：上下文已升级为三段，提示模型善用滚动摘要并去重）。
func buildFeedbackSystemPrompt(rolePrompt, trigger string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(rolePrompt))
	sb.WriteString("\n\n")
	if trigger == model.MeetingFeedbackTriggerManual {
		sb.WriteString("依据上述角色，阅读最近的会议转写，现在必须给出一条反馈。反馈应简洁、可立即使用，直接输出反馈正文本身，不要任何前后缀、解释或标记。")
	} else {
		// 规则化判定（让模型按可核对的条件决定，而非"尽量找"这类模糊指令）：满足任一「给反馈」
		// 条件即输出反馈；仅当两条「沉默」条件同时成立才输出 NO_FEEDBACK；不确定时倾向给反馈。
		sb.WriteString("依据上述角色，阅读最近的会议转写，按以下规则决定：\n\n")
		sb.WriteString("【给出反馈】满足以下任一条，就直接输出一条反馈正文（简洁、可立即使用，不要任何前后缀或解释）：\n")
		sb.WriteString("1. 最近的对话触发了上述角色规则中明确规定的介入时机；\n")
		sb.WriteString("2. 出现了新的观点、问题、需求、异议或信息；\n")
		sb.WriteString("3. 存在事实错误、逻辑漏洞、自相矛盾，或遗漏了关键点；\n")
		sb.WriteString("4. 讨论出现重复、绕圈、跑题或卡住；\n")
		sb.WriteString("5. 形成了一个可被确认、凝练或推进到下一步的结论。\n\n")
		sb.WriteString("【保持沉默】仅当以下两条同时成立时，才只输出 ")
		sb.WriteString(noFeedbackSentinel)
		sb.WriteString(" 这一个标记、不要有其他任何内容：\n")
		sb.WriteString("1. 最近的对话相比你上一条反馈没有任何新的实质内容；\n")
		sb.WriteString("2. 且不满足上面任何一条「给出反馈」的条件。\n\n")
		sb.WriteString("判定不确定时，给出反馈。不要逐字重复你已经给过的反馈，但可以从新角度补充或推进。")
	}
	sb.WriteString("\n\n请参考下面的会议滚动摘要了解整场会议的全局脉络。")
	return sb.String()
}

// buildTranscriptWindow 把分段拼成「最近 maxRunes 字」的转写窗口，返回 (transcript, anchorSeq)。
//
// anchorSeq 是最后一段的 seq（生成时的转写进度锚点，SPEC §2.3）；无分段时为 0。
// 窗口从尾部往前取，保留时间顺序；空文本段（静音）跳过拼接但仍参与 anchor 计算。
func buildTranscriptWindow(segs []model.MeetingSegment, maxRunes int) (string, int) {
	anchorSeq := 0
	if len(segs) > 0 {
		anchorSeq = segs[len(segs)-1].Seq
	}

	// 从尾部往前累积非空文本，直到达到 maxRunes。
	var picked []string
	total := 0
	for i := len(segs) - 1; i >= 0; i-- {
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			continue
		}
		r := []rune(t)
		if total+len(r) > maxRunes && len(picked) > 0 {
			break
		}
		picked = append(picked, t)
		total += len(r)
		if total >= maxRunes {
			break
		}
	}
	// picked 是逆序（从尾往前），反转回时间顺序。
	for l, r := 0, len(picked)-1; l < r; l, r = l+1, r-1 {
		picked[l], picked[r] = picked[r], picked[l]
	}
	return strings.Join(picked, "\n"), anchorSeq
}

// buildRecentTranscriptWindow 取「最近 window 时间窗口内」的 final 段拼成逐字转写
// （FEEDBACK_V2_SPEC §2.3），返回 (transcript, anchorSeq)。
//
//   - 按 segment.created_at 截取：保留 created_at >= now-window 的段（边界用 now 注入便于测试，
//     生产传 time.Now()）。
//   - anchorSeq 仍取全部分段中最后一段的 seq（转写进度锚点，与 buildTranscriptWindow 语义一致），
//     不受时间窗口影响——锚点表示「反馈生成时转写已推进到哪」。无分段时为 0。
//   - 空文本段（静音）跳过拼接但参与 anchor 计算。
//   - 安全字数上限 maxRunes 兜底：窗口内内容超长时只保留尾部（最新最相关，全局脉络由滚动摘要覆盖）。
//
// store 返回的 segs 已按 seq ASC（≈时间顺序），故顺序遍历即时间顺序。
func buildRecentTranscriptWindow(segs []model.MeetingSegment, window time.Duration, maxRunes int, now time.Time) (string, int) {
	anchorSeq := 0
	if len(segs) > 0 {
		anchorSeq = segs[len(segs)-1].Seq
	}

	cutoff := now.Add(-window)
	var picked []string
	for i := range segs {
		// 仅保留窗口内的段。CreatedAt 零值（未持久化/测试未设）视为「在窗口内」，避免误丢。
		if !segs[i].CreatedAt.IsZero() && segs[i].CreatedAt.Before(cutoff) {
			continue
		}
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			continue
		}
		picked = append(picked, t)
	}

	transcript := strings.Join(picked, "\n")
	// 安全字数上限：超长只保留尾部（rune-aware，避免截断多字节字符）。
	if r := []rune(transcript); maxRunes > 0 && len(r) > maxRunes {
		transcript = string(r[len(r)-maxRunes:])
	}
	return transcript, anchorSeq
}

// endGenError 是 Langfuse generation 错误收尾的小工具（优雅降级 if tc != nil）。
func endGenError(tc *langfuse.TraceCtx, genID string, err error) {
	if tc == nil {
		return
	}
	langfuse.EndGeneration(tc.TraceID, genID, langfuse.WithGenError(err.Error()))
}
