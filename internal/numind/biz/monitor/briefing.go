package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

const briefingSystemPromptTemplate = `你是一个内容运营分析师。基于以下小红书竞品监控数据，生成一份%s简报。
要求：
1. 概览：监控了N个博主，发现M条新内容
2. 热门内容 TOP 3（按互动量排序）
3. 选题趋势洞察
4. 行动建议
格式：Markdown`

// GenerateUserBriefing 生成用户简报
// briefingType: "daily" (日报, 最近 24h) 或 "weekly" (周报, 最近 7 天)
func (mb *MonitorBiz) GenerateUserBriefing(ctx context.Context, userID uint, briefingType string) (*model.MonitorBriefing, error) {
	// Langfuse trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "monitor-briefing",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("monitor"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 1. 计算时间范围
	now := time.Now()
	var from time.Time
	var typeLabel string
	switch briefingType {
	case model.BriefingTypeWeekly:
		from = now.AddDate(0, 0, -7)
		typeLabel = "周报"
	default:
		briefingType = model.BriefingTypeDaily
		from = now.Add(-24 * time.Hour)
		typeLabel = "日报"
	}

	// 2. 查询笔记
	notes, err := mb.store.Monitor().ListNotesByPeriod(ctx, userID, from, now)
	if err != nil {
		return nil, fmt.Errorf("GenerateUserBriefing: list notes: %w", err)
	}
	if len(notes) == 0 {
		return nil, nil
	}

	// 3. 聚合数据
	bloggerSet := make(map[uint]bool)
	categoryCount := make(map[string]int)
	for i := range notes {
		bloggerSet[notes[i].BloggerID] = true
		if notes[i].AICategory != "" {
			categoryCount[notes[i].AICategory]++
		}
	}

	// 按互动量排序（likes + comments + collects + shares）
	sort.Slice(notes, func(i, j int) bool {
		engI := notes[i].Likes + notes[i].Comments + notes[i].Collects + notes[i].Shares
		engJ := notes[j].Likes + notes[j].Comments + notes[j].Collects + notes[j].Shares
		return engI > engJ
	})

	// 取 top notes（最多 10 条用于 prompt）
	topN := 10
	if len(notes) < topN {
		topN = len(notes)
	}
	topNotes := notes[:topN]

	// 4. 构建 prompt
	var userMsg strings.Builder
	userMsg.WriteString(fmt.Sprintf("监控博主数：%d\n", len(bloggerSet)))
	userMsg.WriteString(fmt.Sprintf("新增笔记数：%d\n", len(notes)))
	userMsg.WriteString(fmt.Sprintf("时间范围：%s ~ %s\n\n", from.Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04")))

	// 分类统计
	if len(categoryCount) > 0 {
		userMsg.WriteString("分类分布：\n")
		for cat, cnt := range categoryCount {
			userMsg.WriteString(fmt.Sprintf("- %s: %d 条\n", cat, cnt))
		}
		userMsg.WriteString("\n")
	}

	// Top 笔记详情
	userMsg.WriteString("热门笔记：\n")
	for i, n := range topNotes {
		engagement := n.Likes + n.Comments + n.Collects + n.Shares
		userMsg.WriteString(fmt.Sprintf("%d. 【%s】互动量: %d (赞%d 评%d 藏%d 转%d)",
			i+1, n.Title, engagement, n.Likes, n.Comments, n.Collects, n.Shares))
		if n.AISummary != "" {
			userMsg.WriteString(fmt.Sprintf(" | 摘要: %s", n.AISummary))
		}
		userMsg.WriteString("\n")
	}

	systemPrompt := fmt.Sprintf(briefingSystemPromptTemplate, typeLabel)
	aiMessages := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: systemPrompt}},
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg.String()}},
	}

	// 5. 注入计费上下文 + Gateway 中间件上下文
	ctx = billing.WithBilling(ctx, userID, "monitor_briefing")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// 6. 调用 AI Gateway
	resp, err := aiservice.Chat(ctx, profile.MonitorBriefing, aiservice.ChatRequest{
		Messages:    aiMessages,
		Temperature: 0.5,
		MaxTokens:   2000,
	})
	if err != nil {
		return nil, fmt.Errorf("GenerateUserBriefing: llm call: %w", err)
	}
	content := resp.Content

	// 7. 保存简报
	periodStart := from
	periodEnd := now
	briefing := &model.MonitorBriefing{
		UserID:      userID,
		Type:        briefingType,
		Title:       fmt.Sprintf("%s - %s", typeLabel, now.Format("2006-01-02")),
		Content:     content,
		NoteCount:   uint(len(notes)),
		PeriodStart: &periodStart,
		PeriodEnd:   &periodEnd,
	}

	if err := mb.store.Monitor().CreateBriefing(ctx, briefing); err != nil {
		return nil, fmt.Errorf("GenerateUserBriefing: save briefing: %w", err)
	}

	// 8. 尝试发送飞书通知
	cfg, cfgErr := mb.store.Monitor().GetConfig(ctx, userID)
	if cfgErr == nil && cfg != nil && cfg.FeishuWebhook != "" {
		SendBriefing(cfg.FeishuWebhook, briefing)
		// 更新发送状态
		_ = mb.store.Monitor().UpdateBriefingSent(ctx, briefing.ID, true)
	}

	log.Infow("GenerateUserBriefing: completed",
		"userID", userID,
		"type", briefingType,
		"noteCount", len(notes),
	)

	// Langfuse trace output
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "briefing-generated",
			langfuse.WithSpanParent(tc.ParentObservationID),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}

	return briefing, nil
}
