package marketplace

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
)

// ErrSanitizeUnavailable aliases the canonical errno (T7). Existing callers
// (controller mapBizError + tests via errors.Is) keep working because the
// alias is the SAME *Errno pointer — errors.Is comparing the wrapped chain
// against either name resolves to the same identity. The biz layer wraps the
// underlying cause via fmt.Errorf("%w: %s", ...) so callers can still detect
// the category via errors.Is(err, ErrSanitizeUnavailable).
var ErrSanitizeUnavailable error = errno.ErrSanitizeUnavailable

// SanitizeResult records the output of the two-stage sanitization pipeline.
type SanitizeResult struct {
	SanitizedBodyMD  string
	Stages           []string // ["regex", "llm"]
	PromptTokens     int
	CompletionTokens int
}

// rePII is the ordered PII regex set (Stage 1). Order matters: more specific
// patterns must come before generic ones (id_card_cn is more specific than
// bank_card because id-card-cn ends with [0-9Xx], not pure digits).
//
// Spec §3.2 originally had a second regex stage reading
// agent_permission_config.forbidden_competitor_names — that column doesn't
// exist on develop (see manifest S0-D3 REVISED). Stage 2 LLM prompt covers
// org / product / competitor names; mandatory frontend diff review gate is
// the final human check.
var rePII = []struct {
	Name    string
	Pattern *regexp.Regexp
	Replace string
}{
	// id_card_cn: exactly 18 chars (China standard). \b anchors prevent matching
	// the first 18 digits of a longer numeric sequence (e.g. a 19-digit bank card).
	{"id_card_cn", regexp.MustCompile(`\b[1-9]\d{16}[0-9Xx]\b`), "[身份证]"},
	// bank_card: 16-19 digits (covers Visa/Master/银联/JCB). \b on both sides keeps
	// it from matching a substring of a longer numeric blob.
	{"bank_card", regexp.MustCompile(`\b\d{16,19}\b`), "[银行卡]"},
	// phone_cn: 11-digit China mobile, leading 1 then [3-9].
	{"phone_cn", regexp.MustCompile(`\b1[3-9]\d{9}\b`), "[手机]"},
	{"email", regexp.MustCompile(`[\w._%+-]+@[\w.-]+\.[A-Za-z]{2,}`), "[邮箱]"},
}

// chatFn / promptFn are package-level injection points so tests can swap them.
// Production code uses aiservice.Chat + langfuse.FetchPrompt directly.
// Tests do: oldFn := chatFn; chatFn = mockFn; t.Cleanup(func() { chatFn = oldFn })
var (
	chatFn   = aiservice.Chat
	promptFn = langfuse.FetchPrompt
)

// sanitizePromptKey is the Langfuse prompt registry key. If absent or Langfuse
// disabled, the inline fallback below is used (deterministic, ships with code).
const sanitizePromptKey = "skill-marketplace-sanitize-v1"

const sanitizeFallbackPrompt = `你是脱敏助手。请识别以下 markdown 文本中的：
- 具体人名（学员、员工）→ 替换为 [姓名]
- 具体机构名（公司、学校）→ 替换为 [机构]
- 具体产品名/课程名 → 替换为 [产品]
保留行业通用术语和职能描述。仅返回脱敏后的完整 markdown，不要添加任何额外说明。

---原文---
%s
---原文结束---`

// Sanitize runs the two-stage pipeline on a Skill body and returns the result.
// Stage 1: deterministic PII regex (email/phone/id-card/bank-card).
// Stage 2: LLM entity recognition via aiservice.Chat (deepseek-v4-pro via DMXAPI
// — see ADR 0001-sanitize-llm-deepseek-v4-pro.md; profile=skill.marketplace.sanitize).
// Stage 2 failure returns an error wrapping ErrSanitizeUnavailable.
//
// All LLM calls record a Langfuse generation (name=sanitize-skill-body) when a
// TraceCtx is present in ctx (langfuse.FromContext). The actual model name is
// resolved by adapter.resp.Model and set on EndGeneration (per-route accuracy).
// Disabled Langfuse: no-op, business continues.
func Sanitize(ctx context.Context, body string) (*SanitizeResult, error) {
	stage1 := applyPIIRegex(body)
	stage2, prompt, completion, err := callSanitizeLLM(ctx, stage1)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSanitizeUnavailable, err.Error())
	}
	return &SanitizeResult{
		SanitizedBodyMD:  stage2,
		Stages:           []string{"regex", "llm"},
		PromptTokens:     prompt,
		CompletionTokens: completion,
	}, nil
}

// applyPIIRegex runs the Stage 1 deterministic patterns.
func applyPIIRegex(body string) string {
	out := body
	for _, p := range rePII {
		out = p.Pattern.ReplaceAllString(out, p.Replace)
	}
	return out
}

// callSanitizeLLM runs the Stage 2 LLM call with full Langfuse instrumentation.
// Returns the sanitized body and token usage. Wraps Langfuse calls in
// FromContext nil guards so disabled-Langfuse production never panics.
func callSanitizeLLM(ctx context.Context, body string) (sanitized string, promptTokens, completionTokens int, err error) {
	tc := langfuse.FromContext(ctx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		// model is resolved on EndGeneration via resp.Model — adapter knows the
		// actual route chosen (deepseek-v4-pro via DMXAPI today; see ADR 0001).
		// We don't pin a starting hint here so Langfuse UI doesn't display a
		// model that disagrees with the resolved one.
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("sanitize-skill-body"),
			langfuse.WithGenInput(map[string]string{"body": body}),
		)
	}

	// langfuse.FetchPrompt takes (name, fallback) — fallback is returned when
	// Langfuse is disabled or the named prompt isn't registered. Always safe.
	promptTpl, _ := promptFn(sanitizePromptKey, sanitizeFallbackPrompt)
	prompt := fmt.Sprintf(promptTpl, body)

	resp, callErr := chatFn(ctx, profile.SkillMarketplaceSanitize, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: prompt}},
		},
		Temperature: 0.1,
		MaxTokens:   8000,
	})
	if callErr != nil {
		if tc != nil {
			langfuse.EndGeneration(tc.TraceID, genID,
				langfuse.WithGenOutput(map[string]string{"error": callErr.Error()}),
			)
		}
		return "", 0, 0, callErr
	}

	sanitized = strings.TrimSpace(resp.Content)
	promptTokens = resp.Usage.PromptTokens
	completionTokens = resp.Usage.CompletionTokens

	if tc != nil {
		// WithGenModel(resp.Model) — Adapter resolves the actual model that handled
		// the call (qwen-turbo, qwen-turbo-fallback-X, etc.); Langfuse uses this for
		// per-model cost aggregation. Pattern matches biz/memory/extractor.go and
		// biz/memory/selector.go success-path EndGeneration.
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Model),
			langfuse.WithGenOutput(sanitized),
			langfuse.WithGenUsage(promptTokens, completionTokens),
		)
	}
	return sanitized, promptTokens, completionTokens, nil
}
