package sop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
)

func noopCancel() {}

// collectHandler records the events the consumer emits.
func collectHandler() (StreamHandler, *[]string, *[]string) {
	var mu sync.Mutex
	thinking := []string{}
	messages := []string{}
	h := func(event, chunk string) error {
		mu.Lock()
		defer mu.Unlock()
		switch event {
		case "thinking":
			thinking = append(thinking, chunk)
		case "message":
			messages = append(messages, chunk)
		}
		return nil
	}
	return h, &thinking, &messages
}

func TestConsumeGatewayStream_NormalCompletion(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 8)
	ch <- aiservice.ChatChunk{ReasoningDelta: "think"}
	ch <- aiservice.ChatChunk{Delta: "hello "}
	ch <- aiservice.ChatChunk{Delta: "world"}
	ch <- aiservice.ChatChunk{IsFinal: true, Model: "m", Provider: "p",
		Usage: &aiservice.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
	close(ch)

	h, think, msgs := collectHandler()
	content, usage, err := consumeGatewayStream(context.Background(), ch, time.Minute, noopCancel, 1, h)

	require.NoError(t, err)
	assert.Equal(t, "hello world", content)
	require.NotNil(t, usage)
	assert.Equal(t, 15, usage.TotalTokens)
	assert.Equal(t, "m", usage.ModelName)
	assert.Equal(t, "p", usage.Provider)
	assert.Equal(t, []string{"think"}, *think)
	assert.Equal(t, []string{"hello ", "world"}, *msgs)
}

func TestConsumeGatewayStream_MidStreamError(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 4)
	ch <- aiservice.ChatChunk{Delta: "partial"}
	ch <- aiservice.ChatChunk{IsFinal: true, Err: errors.New("boom")}
	close(ch)

	h, _, _ := collectHandler()
	content, _, err := consumeGatewayStream(context.Background(), ch, time.Minute, noopCancel, 1, h)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.Equal(t, "partial", content, "partial content preserved on mid-stream error")
}

func TestConsumeGatewayStream_EmptyResponse(t *testing.T) {
	ch := make(chan aiservice.ChatChunk)
	close(ch)

	h, _, _ := collectHandler()
	_, _, err := consumeGatewayStream(context.Background(), ch, time.Minute, noopCancel, 1, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

// TestConsumeGatewayStream_EmptyCompletionWithUsage verifies that when the
// provider returns usage (prompt tokens) but produces NO content and zero
// completion tokens — an empty generation, e.g. the prompt filled the model's
// context window — the consumer returns an error (so the node is marked failed
// instead of a silent empty success that stuck the UI on "等待执行") while still
// surfacing the usage for accounting. Regression: prod bug, user 600920v.
// (empty-completion-refund-guard)
func TestConsumeGatewayStream_EmptyCompletionWithUsage(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 2)
	ch <- aiservice.ChatChunk{IsFinal: true, Model: "deepseek-v4-pro", Provider: "dmxapi",
		Usage: &aiservice.TokenUsage{PromptTokens: 124666, CompletionTokens: 0, TotalTokens: 124666}}
	close(ch)

	h, _, _ := collectHandler()
	content, usage, err := consumeGatewayStream(context.Background(), ch, time.Minute, noopCancel, 1, h)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty completion")
	assert.Equal(t, "", content)
	require.NotNil(t, usage, "usage must be surfaced for accounting/refund")
	assert.Equal(t, 124666, usage.PromptTokens)
	assert.Equal(t, 0, usage.CompletionTokens)
}

// TestConsumeGatewayStream_PartialContentThenZeroUsage proves the empty-completion
// guard is keyed on empty CONTENT, not on the reported completion count: when the
// provider streams real content but then reports CompletionTokens==0 in the final
// usage (a provider misreport), the streamed content WINS — the consumer returns
// it as a success (no error, no spurious empty-completion failure).
func TestConsumeGatewayStream_PartialContentThenZeroUsage(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 3)
	ch <- aiservice.ChatChunk{Delta: "partial answer"}
	ch <- aiservice.ChatChunk{IsFinal: true, Model: "m", Provider: "p",
		Usage: &aiservice.TokenUsage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100}}
	close(ch)

	h, _, _ := collectHandler()
	content, usage, err := consumeGatewayStream(context.Background(), ch, time.Minute, noopCancel, 1, h)

	require.NoError(t, err, "streamed content must win over a 0-completion misreport")
	assert.Equal(t, "partial answer", content)
	require.NotNil(t, usage)
}

// TestConsumeGatewayStream_IdleTimeout feeds one chunk then stalls. The idle
// timer must fire → cancelStream called → timeout error wrapping
// context.DeadlineExceeded. The cancel stub closes the channel (simulating the
// adapter reacting to cancellation) so the drain goroutine exits cleanly.
func TestConsumeGatewayStream_IdleTimeout(t *testing.T) {
	ch := make(chan aiservice.ChatChunk)
	var cancelCalled atomic.Bool
	var closeOnce sync.Once
	cancel := func() {
		cancelCalled.Store(true)
		closeOnce.Do(func() { close(ch) }) // adapter would close the stream on cancel
	}

	h, _, _ := collectHandler()
	done := make(chan struct {
		content string
		err     error
	}, 1)
	go func() {
		content, _, err := consumeGatewayStream(context.Background(), ch, 60*time.Millisecond, cancel, 1, h)
		done <- struct {
			content string
			err     error
		}{content, err}
	}()

	ch <- aiservice.ChatChunk{Delta: "hi"} // one chunk, then stall

	select {
	case res := <-done:
		require.Error(t, res.err)
		assert.True(t, errors.Is(res.err, context.DeadlineExceeded),
			"idle timeout must wrap context.DeadlineExceeded (got %v)", res.err)
		assert.Equal(t, "hi", res.content, "content received before stall is preserved")
		assert.True(t, cancelCalled.Load(), "idle timeout must cancel the stream")
	case <-time.After(2 * time.Second):
		t.Fatal("consumeGatewayStream did not return on idle stall")
	}
}
