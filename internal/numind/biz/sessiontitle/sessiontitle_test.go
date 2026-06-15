package sessiontitle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
)

type chatFnSig = func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

// capturedCall records what Generate passed to the LLM call, including the
// billing-relevant ctx flags so we can assert the user is never billed.
type capturedCall struct {
	taskID           string
	req              aiservice.ChatRequest
	billOnly         bool
	billingUserID    uint
	middlewareUserID uint
	skipLegacy       bool
}

// stubChat returns a chatFn that records call args (incl. ctx billing flags) and
// returns the given content.
func stubChat(content string) (chatFnSig, *[]capturedCall) {
	calls := make([]capturedCall, 0, 1)
	fn := func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		c := capturedCall{
			taskID:           taskID,
			req:              req,
			billOnly:         aismw.GatewayBillingOnlyFromCtx(ctx),
			middlewareUserID: aismw.UserIDFromCtx(ctx),
			skipLegacy:       aiservice.ShouldSkipLegacyBilling(ctx),
		}
		if bc := billing.FromContext(ctx); bc != nil {
			c.billingUserID = bc.UserID
		}
		calls = append(calls, c)
		return &aiservice.ChatResponse{
			Content: content,
			Model:   "qwen-turbo",
			Usage:   aiservice.TokenUsage{PromptTokens: 30, CompletionTokens: 6, TotalTokens: 36},
		}, nil
	}
	return fn, &calls
}

func withChatFn(t *testing.T, fn chatFnSig) {
	t.Helper()
	old := chatFn
	chatFn = fn
	t.Cleanup(func() { chatFn = old })
}

// --- sanitizeTitle pure function ---

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "小红书账号定位策略", "小红书账号定位策略"},
		{"surrounding_ascii_quotes", `"账号定位策略"`, "账号定位策略"},
		{"surrounding_cjk_quotes", "「账号定位策略」", "账号定位策略"},
		{"trailing_period_cjk", "账号定位策略。", "账号定位策略"},
		{"trailing_period_ascii", "Growth plan.", "Growth plan"},
		{"leading_trailing_space", "  账号定位  ", "账号定位"},
		{"newlines_collapsed", "账号\n定位\n策略", "账号 定位 策略"},
		{"quote_then_punct", `"账号定位策略！"`, "账号定位策略"},
		{"empty", "", ""},
		{"only_quotes", `""`, ""},
		{"only_cjk_quotes", "“”", ""},
		{"only_book_quotes", "「」", ""},
		{"only_punct", "。！", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, sanitizeTitle(c.in))
		})
	}
}

func TestSanitizeTitle_ClampsToMaxRunes(t *testing.T) {
	in := strings.Repeat("标", 40) // 40 CJK runes
	got := sanitizeTitle(in)
	assert.Equal(t, maxTitleRunes, len([]rune(got)), "title clamped to maxTitleRunes")
}

func TestTruncateRunes_Multibyte(t *testing.T) {
	assert.Equal(t, "你好", truncateRunes("你好世界", 2))
	assert.Equal(t, "你好世界", truncateRunes("你好世界", 10))
	assert.Equal(t, "", truncateRunes("", 5))
}

// --- Generate happy path + request shape ---

func TestGenerate_HappyPath_RequestShape(t *testing.T) {
	stub, calls := stubChat("账号定位策略")
	withChatFn(t, stub)

	title, err := Generate(context.Background(), "帮我做小红书账号定位", "好的，我们从赛道选择开始……")
	require.NoError(t, err)
	assert.Equal(t, "账号定位策略", title)

	require.Len(t, *calls, 1)
	got := (*calls)[0]
	assert.Equal(t, profile.SessionTitle, got.taskID, "must use session.title task")
	assert.Equal(t, "qwen-turbo", got.req.ModelOverride, "must use cheap qwen-turbo")
	assert.Equal(t, 32, got.req.MaxTokens)
	assert.Nil(t, got.req.ContextFragments, "no ContextFragments → gateway pass-through (no reserve)")
	require.Len(t, got.req.Messages, 2)
	assert.Equal(t, aiservice.MessageRoleSystem, got.req.Messages[0].Role)
	assert.Equal(t, aiservice.MessageRoleUser, got.req.Messages[1].Role)
	assert.Contains(t, got.req.Messages[1].Content.Text, "帮我做小红书账号定位")
}

// --- P0 regression: billing context is stripped so the user is never billed,
// even when the inherited ctx carries bill-only + a real userID (agent path). ---

func TestGenerate_StripsBillingContext_NoUserCharge(t *testing.T) {
	stub, calls := stubChat("会话标题")
	withChatFn(t, stub)

	// Simulate the agent finalizeCtx: bill-only flag + real userID inherited.
	ctx := context.Background()
	ctx = aismw.WithGatewayBillingOnly(ctx)
	ctx = billing.WithBilling(ctx, 123, "agent_run")
	ctx = aismw.WithUserID(ctx, 123)

	_, err := Generate(ctx, "查一下竞品定价", "好的，我来调研……")
	require.NoError(t, err)

	require.Len(t, *calls, 1)
	got := (*calls)[0]
	assert.False(t, got.billOnly, "bill-only must be cleared so gateway takes pass-through (no reserve)")
	assert.Equal(t, uint(0), got.billingUserID, "billing userID must be zeroed (no reserve)")
	assert.Equal(t, uint(0), got.middlewareUserID, "ctxKeyUserID must be zeroed: free-model gate falls back to it when billing userID is 0")
	assert.True(t, got.skipLegacy, "legacy UsageRecord must be skipped")
}

// --- Error path: best-effort, no panic without Langfuse trace ---

func TestGenerate_LLMError_ReturnsEmptyError(t *testing.T) {
	withChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("provider quota exceeded")
	})
	// context.Background has no Langfuse trace → error-recording branch must
	// gracefully skip (tc == nil) and not panic.
	title, err := Generate(context.Background(), "hi", "hello")
	assert.Empty(t, title)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider quota exceeded")
}

func TestGenerate_LLMError_WithTrace_NoPanic(t *testing.T) {
	// With a Langfuse trace present (tc != nil), the error branch records a
	// generation error (spec §2.7 / ai-service.md §3). Langfuse is disabled in
	// unit tests so CreateGeneration/EndGeneration are no-ops; this test pins
	// that the tc != nil failure path executes without panicking and still
	// returns the wrapped error. (The tc == nil graceful-skip path is covered
	// by TestGenerate_LLMError_ReturnsEmptyError.)
	withChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("provider down")
	})
	ctx := langfuse.WithTrace(context.Background(), "test-trace-id")
	require.NotNil(t, langfuse.FromContext(ctx), "trace ctx must be present to exercise the tc!=nil branch")

	title, err := Generate(ctx, "做个调研", "好的")
	assert.Empty(t, title)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider down")
}

func TestGenerate_OnlyOneSideEmpty_StillCallsLLM(t *testing.T) {
	stub, calls := stubChat("内容标题")
	withChatFn(t, stub)
	// Early-return guard is user=="" AND asst=="" — one non-empty side proceeds.
	title, err := Generate(context.Background(), "   ", "助手给出的一些内容")
	require.NoError(t, err)
	assert.Equal(t, "内容标题", title)
	require.Len(t, *calls, 1, "one non-empty side must still trigger the LLM call")
}

func TestGenerate_EmptyInput_NoLLMCall(t *testing.T) {
	stub, calls := stubChat("x")
	withChatFn(t, stub)
	title, err := Generate(context.Background(), "   ", "")
	assert.Empty(t, title)
	require.Error(t, err)
	assert.Len(t, *calls, 0, "no LLM call for empty conversation")
}

func TestGenerate_EmptyAfterSanitize_ReturnsError(t *testing.T) {
	stub, _ := stubChat(`""。`) // sanitises to empty
	withChatFn(t, stub)
	title, err := Generate(context.Background(), "q", "a")
	assert.Empty(t, title)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty title")
}
