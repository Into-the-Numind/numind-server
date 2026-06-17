package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/callctx"
	"numind-server/internal/pkg/aiservice"
)

// TestAdapter_Stream_StashesUsage reproduces an inert budget guardrail: the
// PRODUCTION path is streaming (adapter.Stream), but only Generate stashed token usage
// — so budgetgate's credit accounting stayed 0 for streaming runs (originally the
// per-session cap, now the daily-credits dimension). Stream must stash the final
// chunk's usage keyed by call-id, exactly like Generate, so the credit total accrues.
func TestAdapter_Stream_StashesUsage(t *testing.T) {
	origStreamFn := chatStreamFn
	t.Cleanup(func() { chatStreamFn = origStreamFn })
	chatStreamFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		ch := make(chan aiservice.ChatChunk, 2)
		ch <- aiservice.ChatChunk{Delta: "hello"}
		ch <- aiservice.ChatChunk{
			IsFinal:      true,
			FinishReason: "stop",
			Model:        "glm-4-7-251222",
			Usage:        &aiservice.TokenUsage{PromptTokens: 200, CompletionTokens: 80},
		}
		close(ch)
		return ch, nil
	}

	adapter := NewAiserviceAdapter("glm-4-7-251222", "agent.task").(*aiserviceAdapter)
	callID := callctx.NewCallID()
	ctx := callctx.WithCallID(context.Background(), callID)

	sr, err := adapter.Stream(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	require.NoError(t, err)
	// Drain so the wrapper goroutine processes the final chunk and stashes usage.
	for {
		if _, rerr := sr.Recv(); rerr != nil {
			break // EOF
		}
	}
	sr.Close()

	u, ok := adapter.LookupUsage(callID)
	require.True(t, ok, "Stream must stash usage so the per-session credit cap accrues")
	assert.Equal(t, 200, u.PromptTokens)
	assert.Equal(t, 80, u.CompletionTokens)
	assert.Equal(t, "glm-4-7-251222", u.Model)
}
