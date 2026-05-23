package marketplace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// stubChat returns a chatFn that always returns the given output (and counts calls).
type chatCall struct {
	TaskID string
	Req    aiservice.ChatRequest
}

func stubChat(t *testing.T, output string, promptTokens, completionTokens int) (chatFnSig, *[]chatCall) {
	t.Helper()
	calls := make([]chatCall, 0, 2)
	fn := func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		calls = append(calls, chatCall{TaskID: taskID, Req: req})
		return &aiservice.ChatResponse{
			Content: output,
			Usage: aiservice.TokenUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
		}, nil
	}
	return fn, &calls
}

// errChat returns a chatFn that always errors.
func errChat(err error) chatFnSig {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, err
	}
}

// chatFnSig is the function signature of aiservice.Chat (used for test mocks).
type chatFnSig = func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// withChatFn swaps the package-level chatFn for the duration of the test.
func withChatFn(t *testing.T, fn chatFnSig) {
	t.Helper()
	old := chatFn
	chatFn = fn
	t.Cleanup(func() { chatFn = old })
}

// withPromptFn swaps the package-level promptFn for the duration of the test.
func withPromptFn(t *testing.T, fn func(name, fallback string) (string, int)) {
	t.Helper()
	old := promptFn
	promptFn = fn
	t.Cleanup(func() { promptFn = old })
}

// --- Stage 1: PII regex ---

func TestApplyPIIRegex_Email(t *testing.T) {
	out := applyPIIRegex("联系 admin@example.com 或 j.smith+sales@sub.example.co")
	assert.Contains(t, out, "[邮箱]")
	assert.NotContains(t, out, "admin@example.com")
	assert.NotContains(t, out, "j.smith+sales@sub.example.co")
}

func TestApplyPIIRegex_PhoneCN(t *testing.T) {
	out := applyPIIRegex("电话 13800138000 或 18912345678")
	assert.Contains(t, out, "[手机]")
	assert.NotContains(t, out, "13800138000")
	assert.NotContains(t, out, "18912345678")
}

func TestApplyPIIRegex_IDCardCN(t *testing.T) {
	// 18-bit Chinese ID card
	out := applyPIIRegex("身份证号: 110101199001011234")
	assert.Contains(t, out, "[身份证]")
	assert.NotContains(t, out, "110101199001011234")

	// Ends with X
	out2 := applyPIIRegex("身份证号: 11010119900101123X")
	assert.Contains(t, out2, "[身份证]")
	assert.NotContains(t, out2, "11010119900101123X")
}

func TestApplyPIIRegex_BankCard(t *testing.T) {
	out := applyPIIRegex("卡号 6228480402564890018")
	assert.Contains(t, out, "[银行卡]")
	assert.NotContains(t, out, "6228480402564890018")
}

func TestApplyPIIRegex_MultiplePIITypes(t *testing.T) {
	in := "联系 alice@example.com 电话 13900001111\n卡号 6228480402564890018"
	out := applyPIIRegex(in)
	assert.NotContains(t, out, "alice@example.com")
	assert.NotContains(t, out, "13900001111")
	assert.NotContains(t, out, "6228480402564890018")
	assert.Contains(t, out, "[邮箱]")
	assert.Contains(t, out, "[手机]")
	assert.Contains(t, out, "[银行卡]")
}

func TestApplyPIIRegex_NoMatchesUnchanged(t *testing.T) {
	in := "# 销售调研流程\n\n## 步骤一：识别客户类型\n\n根据行业划分..."
	out := applyPIIRegex(in)
	assert.Equal(t, in, out, "no PII = no rewrite")
}

func TestApplyPIIRegex_IDCardTakesPrecedenceOverBankCard(t *testing.T) {
	// 18-char string ending in digit (matches both bank_card 16-19 digits and
	// id_card_cn 17-digit prefix + ending [0-9Xx]). id_card pattern is more
	// specific (must start with [1-9], must end with [0-9Xx]) and listed first.
	// Verify the ID-card rule wins.
	out := applyPIIRegex("110101199001011234")
	assert.Equal(t, "[身份证]", out, "id_card rule must win when both could match")
}

// --- Stage 2: LLM happy path ---

func TestSanitize_HappyPath_RegexThenLLM(t *testing.T) {
	// Capture chatFn input so we can verify the regex stage ran BEFORE the LLM.
	stub, calls := stubChat(t, "脱敏后正文", 1200, 1100)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) {
		return fallback, 0
	})

	body := "联系 admin@example.com 销售经理"
	res, err := Sanitize(context.Background(), body)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "脱敏后正文", res.SanitizedBodyMD)
	assert.Equal(t, []string{"regex", "llm"}, res.Stages)
	assert.Equal(t, 1200, res.PromptTokens)
	assert.Equal(t, 1100, res.CompletionTokens)

	require.Len(t, *calls, 1, "chatFn called exactly once")
	got := (*calls)[0]
	assert.Equal(t, profile.SkillMarketplaceSanitize, got.TaskID, "task ID must be skill.marketplace.sanitize")
	require.Len(t, got.Req.Messages, 1)
	msg := got.Req.Messages[0]
	assert.Equal(t, aiservice.MessageRoleUser, msg.Role)
	assert.Contains(t, msg.Content.Text, "[邮箱]", "regex stage must run BEFORE LLM (LLM input contains scrubbed PII)")
	assert.NotContains(t, msg.Content.Text, "admin@example.com", "raw email must not reach LLM")
	assert.InDelta(t, 0.1, got.Req.Temperature, 0.0001, "low-temperature for deterministic output")
	assert.Equal(t, 8000, got.Req.MaxTokens)
}

// --- Stage 2: LLM failure ---

func TestSanitize_LLMFailure_WrapsErrSanitizeUnavailable(t *testing.T) {
	withChatFn(t, errChat(errors.New("provider quota exceeded")))
	withPromptFn(t, func(name, fallback string) (string, int) { return fallback, 0 })

	res, err := Sanitize(context.Background(), "test body")
	assert.Nil(t, res)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSanitizeUnavailable, "caller must be able to errors.Is")
	assert.Contains(t, err.Error(), "provider quota exceeded", "underlying cause must be in error message")
}

// --- Prompt fallback path ---

func TestSanitize_PromptFallback_UsesInlineWhenLangfuseEmpty(t *testing.T) {
	// promptFn returns ("", 0) — simulates Langfuse miss or disabled.
	// callSanitizeLLM should fall through to sanitizeFallbackPrompt.
	stub, calls := stubChat(t, "out", 1, 1)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) {
		assert.Equal(t, sanitizePromptKey, name, "key must be skill-marketplace-sanitize-v1")
		assert.NotEmpty(t, fallback, "fallback must always be passed for safety")
		return fallback, 0 // simulate "no Langfuse entry; use fallback"
	})

	_, err := Sanitize(context.Background(), "body")
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	prompt := (*calls)[0].Req.Messages[0].Content.Text
	assert.Contains(t, prompt, "脱敏助手", "must use the inline fallback template content")
	assert.Contains(t, prompt, "body", "must interpolate the body parameter")
}

func TestSanitize_PromptFromLangfuse_PrefersRegisteredTemplate(t *testing.T) {
	// Simulate Langfuse returning a custom template.
	customTemplate := "CUSTOM:[%s]"
	withPromptFn(t, func(name, fallback string) (string, int) { return customTemplate, 7 })

	stub, calls := stubChat(t, "out", 1, 1)
	withChatFn(t, stub)

	_, err := Sanitize(context.Background(), "body")
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	prompt := (*calls)[0].Req.Messages[0].Content.Text
	assert.Equal(t, "CUSTOM:[body]", prompt, "custom Langfuse template must be used verbatim")
}

// --- Sanitize end-to-end with empty body ---

func TestSanitize_EmptyBody_NoCrash(t *testing.T) {
	stub, calls := stubChat(t, "", 0, 0)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) { return fallback, 0 })

	res, err := Sanitize(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, res.SanitizedBodyMD)
	require.Len(t, *calls, 1)
}

// --- TrimSpace behavior on LLM response ---

func TestSanitize_TrimsWhitespaceFromLLMResponse(t *testing.T) {
	stub, _ := stubChat(t, "\n\n  trimmed body  \n", 1, 1)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) { return fallback, 0 })

	res, err := Sanitize(context.Background(), "in")
	require.NoError(t, err)
	assert.Equal(t, "trimmed body", res.SanitizedBodyMD)
	assert.False(t, strings.HasPrefix(res.SanitizedBodyMD, "\n"))
	assert.False(t, strings.HasSuffix(res.SanitizedBodyMD, "\n"))
}

// --- Langfuse defensive: nil context (no TraceCtx) must not panic ---

func TestSanitize_NoLangfuseContext_NoPanic(t *testing.T) {
	// Plain context.Background has no TraceCtx — callSanitizeLLM's tc==nil branch.
	stub, _ := stubChat(t, "ok", 1, 1)
	withChatFn(t, stub)
	withPromptFn(t, func(name, fallback string) (string, int) { return fallback, 0 })

	// Must not panic even when Langfuse trace context is absent.
	res, err := Sanitize(context.Background(), "input")
	require.NoError(t, err)
	assert.Equal(t, "ok", res.SanitizedBodyMD)
}
