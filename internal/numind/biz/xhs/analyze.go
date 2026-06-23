package xhs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// chatFn 是包级 seam（默认指向 aiservice.Chat），便于单测在不接真实 Gateway 的情况下
// mock LLM 响应。镜像 biz/agent 包的 chatFn 约定。禁止业务代码直接 import provider 包 /
// 裸 HTTP —— 所有 LLM 调用经此入口走 aiservice，保证 Langfuse 追踪 + billing 计费 + 路由降级。
var chatFn = aiservice.Chat

// AI 分析 prompt 输入截断保护（design §4.2）：避免超长正文/评论撑爆 token 预算。
const (
	maxAnalyzeContentRunes  = 4000 // 正文截断上限（字符数）
	maxAnalyzeCommentsRunes = 2000 // 合并评论截断上限（字符数）
	maxAnalyzeOneLineRunes  = 500  // ai_one_line 落库上限（对齐 DB size:500）
	analyzeMaxTokens        = 800  // LLM 输出上限
)

const analyzeSystemPrompt = `你是小红书选题分析师。请分析下面这条小红书笔记，输出严格的 JSON（不要任何额外说明文字、不要 markdown 代码块），字段如下：
{
  "topic_angle": "选题角度：这篇笔记切入话题的独特视角",
  "viral_reason": "爆款原因：为什么这篇内容容易获得高互动",
  "borrowable": "可借鉴点：创作者可直接复用的手法/结构/钩子",
  "target_audience": "目标受众：这篇内容主要打动的人群画像",
  "title_formula": "标题公式：可套用的标题模板/句式",
  "one_line": "一句话总结：用一句话概括这篇笔记的选题价值"
}
所有字段都用简体中文填写，内容务实具体，不要空泛套话。`

// analyzeResult 是 AI 分析返回的 6 字段 JSON 结构。
type analyzeResult struct {
	TopicAngle     string `json:"topic_angle"`
	ViralReason    string `json:"viral_reason"`
	Borrowable     string `json:"borrowable"`
	TargetAudience string `json:"target_audience"`
	TitleFormula   string `json:"title_formula"`
	OneLine        string `json:"one_line"`
}

// analyzeNote 用便宜的非思考模型（deepseek-v4-flash，经 profile.XhsNoteAnalyze 路由）对单条
// 笔记做选题分析，把 6 个 AI 字段写回 note（不落库——落库由 enrichOne 经 UpdateEnrichResult
// 统一完成，避免重复写）。
//
// 计费（design §4.2）：ctx 包 billing.WithBilling(userID, "xhs_note_analyze") → 经 aiservice
// Gateway 普通 Reserve/Reconcile 扣分（deepseek-v4-flash 有非零 pricing_rule，非 IsFreeModel
// 豁免）。Langfuse：在此建 trace（tags ["xhs-collector"]、userID、metadata note_id），Chat 的
// generation 由 Tracing 中间件自动挂在该 trace 下。
//
// JSON 解析失败时降级：把原始响应截断后写入 ai_one_line，其余 5 字段留空，**不返回 error**
// （AI 分析尽力而为，解析失败不应让整条富化 job 失败）。
func (e *Enricher) analyzeNote(ctx context.Context, userID uint, note *model.XhsTopicNote) error {
	// Langfuse trace（优雅降级：langfuse 禁用时 CreateTrace 为 no-op，不影响业务）。
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "xhs-note-analyze",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("xhs-collector"),
		langfuse.WithTraceInput(map[string]interface{}{"note_id": note.ID}),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 计费上下文 + Gateway 中间件上下文。
	ctx = billing.WithBilling(ctx, userID, "xhs_note_analyze")
	ctx = aismw.WithUserID(ctx, userID)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	aiMessages := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: analyzeSystemPrompt}},
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: buildAnalyzeUserPrompt(note)}},
	}

	resp, err := chatFn(ctx, profile.XhsNoteAnalyze, aiservice.ChatRequest{
		Messages:    aiMessages,
		Temperature: 0.3,
		MaxTokens:   analyzeMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("analyzeNote: llm call: %w", err)
	}

	raw := resp.Content
	parsed, ok := parseAnalyzeResult(raw)
	if !ok {
		// 降级：解析失败保留原始响应截断作为一句话总结，不报错。
		log.C(ctx).Warnw("xhs analyzeNote: parse JSON failed, degrading to raw one_line",
			"note_id", note.ID, "raw", raw)
		note.AIOneLine = truncateRunes(strings.TrimSpace(raw), maxAnalyzeOneLineRunes)
		return nil
	}

	note.AITopicAngle = parsed.TopicAngle
	note.AIViralReason = parsed.ViralReason
	note.AIBorrowable = parsed.Borrowable
	note.AITargetAudience = parsed.TargetAudience
	note.AITitleFormula = parsed.TitleFormula
	note.AIOneLine = truncateRunes(parsed.OneLine, maxAnalyzeOneLineRunes)
	return nil
}

// buildAnalyzeUserPrompt 拼装喂给 LLM 的用户消息：title + content(≤4000) + transcript +
// tags + comments(合并 ≤2000)。带截断保护避免超长输入。
func buildAnalyzeUserPrompt(note *model.XhsTopicNote) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("标题：%s\n", note.Title))
	if note.Content != "" {
		b.WriteString(fmt.Sprintf("正文：%s\n", truncateRunes(note.Content, maxAnalyzeContentRunes)))
	}
	if note.VideoTranscript != nil && *note.VideoTranscript != "" {
		b.WriteString(fmt.Sprintf("视频转写：%s\n", truncateRunes(*note.VideoTranscript, maxAnalyzeContentRunes)))
	}
	if tags := tagsToString(note.Tags); tags != "" {
		b.WriteString(fmt.Sprintf("话题标签：%s\n", tags))
	}
	if comments := commentsToString(note.Comments); comments != "" {
		b.WriteString(fmt.Sprintf("热门评论：%s\n", truncateRunes(comments, maxAnalyzeCommentsRunes)))
	}
	return b.String()
}

// parseAnalyzeResult 解析 LLM 返回的 JSON（容忍被 ```json fenced 包裹）。
// 返回 ok=false 表示解析失败（调用方降级）。空对象（6 字段全空）也视为失败。
func parseAnalyzeResult(raw string) (analyzeResult, bool) {
	clean := stripJSONFence(raw)
	var r analyzeResult
	if err := json.Unmarshal([]byte(clean), &r); err != nil {
		return analyzeResult{}, false
	}
	if r.TopicAngle == "" && r.ViralReason == "" && r.Borrowable == "" &&
		r.TargetAudience == "" && r.TitleFormula == "" && r.OneLine == "" {
		return analyzeResult{}, false
	}
	return r, true
}

// stripJSONFence 去掉 markdown 代码块包裹（```json ... ``` 或 ``` ... ```）。
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// truncateRunes 按字符数（非字节）截断，避免切坏多字节 UTF-8。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// tagsToString 把 tags JSON 数组渲染成逗号分隔字符串（解析失败返回空串）。
func tagsToString(tags []byte) string {
	if len(tags) == 0 {
		return ""
	}
	var arr []string
	if err := json.Unmarshal(tags, &arr); err != nil {
		return ""
	}
	return strings.Join(arr, "、")
}

// xhsComment 是评论 JSON 的最小解析结构（仅取昵称 + 文本用于 prompt）。
type xhsComment struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// commentsToString 把 comments JSON 数组渲染成可读文本（解析失败返回空串）。
func commentsToString(comments []byte) string {
	if len(comments) == 0 {
		return ""
	}
	var arr []xhsComment
	if err := json.Unmarshal(comments, &arr); err != nil {
		return ""
	}
	var parts []string
	for _, c := range arr {
		if c.Text == "" {
			continue
		}
		if c.Author != "" {
			parts = append(parts, fmt.Sprintf("%s：%s", c.Author, c.Text))
		} else {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, " / ")
}
