package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// RebuildPromptSystem drives the periodic profile narrative rebuild LLM call.
//
// Inputs are pre-bucketed by category (preference / knowledge / context /
// behavior / goal / correction) — see RebuildNarrative for the bucketing
// logic. The LLM's job is to read all confidence ≥ 0.7 facts and produce
// three coherent narrative paragraphs.
//
// Layer A only — the narrative is about the **agent user themselves**, not
// the customer/subject they discuss.
//
// Output: strict JSON object with three string fields, no surrounding text.
const RebuildPromptSystem = `你基于以下事实列表，为该用户写出三段简短的画像叙述。

【画像对象】= 真正使用 agent 的 user 本人（销售员/分析师/SOP 操作员/PPT 文员/客服等任意场景）。

【三段叙述】
1. work_context — 用户的工作角色/背景/团队（≤120 字）
2. personal_context — 用户的偏好/风格/行为模式（≤120 字）
3. top_of_mind — 用户当前在追的目标/在做的事/最近被关注的话题（≤80 字）

【写作要求】
- 直陈式中文，避免"用户应当...""建议..."的祈使语气
- 不要堆砌孤立 fact，要织成可读叙述
- facts 不足时该段为空字符串 ""，不要编造

【输出格式 — 严格 JSON 对象】
{"work_context":"...","personal_context":"...","top_of_mind":"..."}
不要 markdown 围栏，不要其他文字。`

// rebuildResponse is the JSON object the LLM returns for narrative rebuild.
type rebuildResponse struct {
	WorkContext     string `json:"work_context"`
	PersonalContext string `json:"personal_context"`
	TopOfMind       string `json:"top_of_mind"`
}

// RebuildNarrative reads all confidence ≥ 0.7 facts for the user, buckets them
// by category, calls the LLM to produce three short narrative paragraphs, and
// writes them to user_memory_profile via UpdateNarrative.
//
// Parameters:
//   - ctx        — should be the rebuild-scoped context (timeout-bounded by caller)
//   - factStore  — IUserMemoryFactStore for List
//   - profStore  — IUserMemoryProfileStore for UpdateNarrative
//   - chat       — same chat seam as ExtractorService (test injection)
//   - userID     — target user
//
// Returns nil on success. On LLM error / parse error / store error, returns
// the error wrapped — caller (ExtractorService.maybeRebuildProfile) logs and
// keeps the counter so the next extraction retries.
//
// Behaviour on edge cases:
//   - 0 facts → no-op return nil (counter still resets)
//   - LLM returns empty fields → write empty strings (rebuilds "to-blank" allowed)
//   - List returns > 50 facts → only the top 50 by confidence are sent to LLM
//     (cost cap; older facts likely already reflected in earlier narratives)
func RebuildNarrative(
	ctx context.Context,
	factStore store.IUserMemoryFactStore,
	profStore store.IUserMemoryProfileStore,
	chat extractorChatFn,
	userID uint,
) error {
	if userID == 0 {
		return errors.New("RebuildNarrative: userID required")
	}
	facts, err := factStore.List(ctx, userID, store.ListFactOpts{
		MinConfidence:   0.70,
		IncludeArchived: false,
		OrderBy:         "confidence",
		Limit:           50,
	})
	if err != nil {
		return fmt.Errorf("RebuildNarrative.List(userID=%d): %w", userID, err)
	}
	if len(facts) == 0 {
		log.Infow("memory.RebuildNarrative skipped — no facts", "user_id", userID)
		return nil
	}

	// Bucket by category for prompt clarity (LLM reads category-grouped facts more reliably).
	type bucketEntry struct {
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
	}
	buckets := map[string][]bucketEntry{
		"preference": nil,
		"knowledge":  nil,
		"context":    nil,
		"behavior":   nil,
		"goal":       nil,
		"correction": nil,
	}
	for _, f := range facts {
		if _, ok := buckets[f.Category]; !ok {
			// Defensive: unknown category should never appear, but if it does
			// just skip rather than crash.
			continue
		}
		buckets[f.Category] = append(buckets[f.Category], bucketEntry{
			Content: f.Content, Confidence: f.Confidence,
		})
	}

	// Serialise buckets into the user-side prompt as a labelled list.
	var b strings.Builder
	b.Grow(len(facts) * 64)
	categoryOrder := []string{"context", "preference", "behavior", "knowledge", "goal", "correction"}
	categoryLabel := map[string]string{
		"context":    "[工作背景 context]",
		"preference": "[偏好/风格 preference]",
		"behavior":   "[行为模式 behavior]",
		"knowledge":  "[领域知识 knowledge]",
		"goal":       "[长期目标 goal]",
		"correction": "[历史回退 correction]",
	}
	for _, cat := range categoryOrder {
		items := buckets[cat]
		if len(items) == 0 {
			continue
		}
		b.WriteString(categoryLabel[cat])
		b.WriteString("\n")
		for _, e := range items {
			b.WriteString("- ")
			b.WriteString(e.Content)
			b.WriteString(fmt.Sprintf(" (conf %.2f)\n", e.Confidence))
		}
		b.WriteString("\n")
	}
	promptUser := b.String()

	// Langfuse trace.
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "memory.rebuild_narrative",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]any{
			"fact_count": len(facts),
		}),
		langfuse.WithTraceTags("memory", "rebuild", "layer-a"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	genID := langfuse.SpanID()
	langfuse.CreateGeneration(traceID, genID,
		langfuse.WithGenName("memory.rebuild.llm"),
		langfuse.WithGenInput(promptUser),
	)

	resp, err := chat(ctx, profile.AgentMemoryExtract, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: RebuildPromptSystem}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: promptUser}},
		},
		ResponseFormat: aiservice.ResponseFormatJSONObject,
		MaxTokens:      rebuilderLLMMaxTokens,
		Temperature:    rebuilderLLMTemperature,
	})
	if err != nil {
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
		)
		return fmt.Errorf("RebuildNarrative LLM call: %w", err)
	}
	langfuse.EndGeneration(traceID, genID,
		langfuse.WithGenOutput(resp.Content),
		langfuse.WithGenModel(resp.Model),
		langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
	)

	// Parse JSON object — strip markdown fence if present.
	raw := strings.TrimSpace(resp.Content)
	if strings.HasPrefix(raw, "```") {
		if i := strings.Index(raw, "\n"); i >= 0 {
			raw = raw[i+1:]
		}
		if j := strings.LastIndex(raw, "```"); j >= 0 {
			raw = raw[:j]
		}
		raw = strings.TrimSpace(raw)
	}
	var out rebuildResponse
	if uerr := json.Unmarshal([]byte(raw), &out); uerr != nil {
		return fmt.Errorf("RebuildNarrative JSON unmarshal: %w (raw_len=%d)", uerr, len(raw))
	}

	if uerr := profStore.UpdateNarrative(ctx, userID, out.WorkContext, out.PersonalContext, out.TopOfMind); uerr != nil {
		return fmt.Errorf("RebuildNarrative UpdateNarrative: %w", uerr)
	}

	log.Infow("memory.RebuildNarrative complete",
		"user_id", userID,
		"facts_seen", len(facts),
		"work_len", len(out.WorkContext),
		"personal_len", len(out.PersonalContext),
		"top_len", len(out.TopOfMind),
	)
	return nil
}
