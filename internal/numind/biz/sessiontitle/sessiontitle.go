// Package sessiontitle generates a short, content-related title from the first
// turn of a chatbot or agent conversation (adaptive-session-titles feature).
//
// It is a system-internal, non-user-billed LLM call. Generate strips the
// billing context (zeroes userID, clears the bill-only flag) and sends a
// request with no ContextFragments, so the AI gateway's ContextBudgetCredits
// middleware takes its no-fragment pass-through branch and never reserves
// credits. This holds for BOTH call sites:
//   - chatbot biz/chatbot/stream.go (plain request ctx), and
//   - agent biz/agent/runner.go finalizeRun, whose finalizeCtx is derived via
//     context.WithoutCancel and would otherwise inherit bill-only + userID.
//
// See docs/superpowers/specs/2026-06-16-adaptive-session-titles-design.md §2.2
// (S2 review P0).
package sessiontitle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
)

const (
	// maxUserRunes / maxAssistantRunes cap how much of the first turn is sent to
	// the model — a title only needs the gist, and short input keeps the call cheap.
	maxUserRunes      = 500
	maxAssistantRunes = 800
	// maxTitleRunes is the hard cap on the returned title length.
	maxTitleRunes = 20
	// genTimeout bounds the title call so a slow provider cannot stall the
	// caller (chatbot generates synchronously before the SSE done event).
	genTimeout = 3 * time.Second
)

// systemPrompt instructs the model to emit only a bare title.
const systemPrompt = `你是会话标题生成器。根据用户与助手的首轮对话，用 6 到 12 个汉字概括对话主题，作为简短标题。只输出标题本身，不要任何标点、引号、前后缀或解释。`

// chatFn is the package-level injection point so tests can swap the LLM call.
// Production uses aiservice.Chat directly.
var chatFn = aiservice.Chat

// Generate summarises the first conversation turn into a 6-12 char title.
//
// Non-user-billed: it strips the billing context and sends no ContextFragments
// so the gateway pass-through runs and no credit reservation is created. Uses
// qwen-turbo via profile.SessionTitle under a 3s timeout.
//
// Best-effort: on any failure (empty input, LLM error, empty result after
// sanitisation) it returns ("", err); callers must leave the existing title
// untouched and only log.
func Generate(ctx context.Context, userMsg, assistantMsg string) (string, error) {
	user := truncateRunes(strings.TrimSpace(userMsg), maxUserRunes)
	asst := truncateRunes(strings.TrimSpace(assistantMsg), maxAssistantRunes)
	if user == "" && asst == "" {
		return "", fmt.Errorf("sessiontitle.Generate: empty conversation")
	}
	convo := fmt.Sprintf("用户：%s\n助手：%s", user, asst)

	// Strip ALL billing context so both chatbot and agent (finalizeCtx, which
	// inherits bill-only + userID via context.WithoutCancel) take the gateway
	// no-fragment pass-through and never reserve. See spec §2.2 (S2 review P0).
	ctx = billing.WithBilling(ctx, 0, "")
	ctx = aismw.WithUserID(ctx, 0)
	ctx = aismw.WithoutGatewayBillingOnly(ctx)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	ctx, cancel := context.WithTimeout(ctx, genTimeout)
	defer cancel()

	tc := langfuse.FromContext(ctx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("session-title"),
			langfuse.WithGenInput(map[string]string{"conversation": convo}),
		)
	}

	resp, err := chatFn(ctx, profile.SessionTitle, aiservice.ChatRequest{
		// No ContextFragments — keeps the gateway on the pass-through branch.
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: systemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: convo}},
		},
		ModelOverride: "qwen-turbo", // priced cheap model (not a 0-priced member-only model)
		MaxTokens:     32,
		Temperature:   0.3,
	})
	if err != nil {
		if tc != nil {
			langfuse.EndGeneration(tc.TraceID, genID,
				langfuse.WithGenOutput(map[string]string{"error": err.Error()}),
			)
		}
		return "", fmt.Errorf("sessiontitle.Generate: %w", err)
	}

	title := sanitizeTitle(resp.Content)
	if title == "" {
		if tc != nil {
			langfuse.EndGeneration(tc.TraceID, genID,
				langfuse.WithGenModel(resp.Model),
				langfuse.WithGenOutput(map[string]string{"error": "empty title after sanitize"}),
				langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
			)
		}
		return "", fmt.Errorf("sessiontitle.Generate: empty title after sanitize")
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenOutput(title),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
	}
	return title, nil
}

// quoteCutset are the leading/trailing quote-ish runes stripped from a title.
const quoteCutset = " \t\"'`“”‘’「」『』《》【】"

// trailingPunct are trailing punctuation runes stripped from a title (the model
// is told not to add any; this is defence-in-depth).
const trailingPunct = "。.!！?？,，、；;：: 　"

// sanitizeTitle normalises the raw model output into a clean, bounded title:
// collapse whitespace/newlines, strip surrounding quotes and trailing
// punctuation, then clamp to maxTitleRunes. Returns "" when nothing remains.
func sanitizeTitle(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ") // collapse runs of spaces
	s = strings.Trim(s, quoteCutset)
	s = strings.TrimRight(s, trailingPunct)
	s = strings.Trim(s, quoteCutset) // strip quotes again in case punct exposed them
	s = strings.TrimSpace(s)
	return truncateRunes(s, maxTitleRunes)
}

// truncateRunes returns s clamped to at most max runes (multibyte-safe).
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
