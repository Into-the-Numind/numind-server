package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// captureCreateL1Store: records Create calls for SyncTurn assertions.
// ---------------------------------------------------------------------------

type captureCreateL1Store struct {
	emptyL1Store
	created []*model.AgentSessionMemory
	err     error
}

func (c *captureCreateL1Store) Create(_ context.Context, m *model.AgentSessionMemory) error {
	if c.err != nil {
		return c.err
	}
	// shallow copy to capture state at call time
	cp := *m
	c.created = append(c.created, &cp)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers: reset chatFn after each test.
// ---------------------------------------------------------------------------

func withMockChat(t *testing.T, fn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	orig := chatFn
	chatFn = fn
	t.Cleanup(func() { chatFn = orig })
}

func newSyncTurnProvider(l1 store.IAgentSessionMemoryStore) *compositeProvider {
	return &compositeProvider{
		l1Store:   l1,
		l2Store:   &emptyL2Store{},
		retriever: NewRetriever(),
		fence:     NewFenceRenderer(),
	}
}

// ---------------------------------------------------------------------------
// TestSyncTurn_HappyPath_WritesItems
// 3 items returned by LLM: 2 with confidence >= 0.5, 1 below threshold.
// Expect exactly 2 Create calls.
// ---------------------------------------------------------------------------

func TestSyncTurn_HappyPath_WritesItems(t *testing.T) {
	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: `{"items":[
				{"kind":"fact","content":"用户做 B2B SaaS 销售","confidence":0.9},
				{"kind":"preference","content":"喜欢看图表","confidence":0.7},
				{"kind":"fact","content":"低置信度内容","confidence":0.3}
			]}`,
		}, nil
	})

	l1 := &captureCreateL1Store{}
	p := newSyncTurnProvider(l1)

	err := p.SyncTurn(context.Background(), 42, 100, "sess1",
		Message{Role: "user", Content: "我在做销售"},
		Message{Role: "assistant", Content: "好的"},
	)
	require.NoError(t, err)
	assert.Len(t, l1.created, 2, "should write exactly 2 items (skip low-confidence one)")

	kinds := make([]string, 0, 2)
	for _, row := range l1.created {
		kinds = append(kinds, row.Kind)
		assert.Equal(t, uint(42), row.UserID)
		assert.Equal(t, uint64(100), row.AgentDefinitionID)
		assert.Equal(t, "sync_turn", row.SourceType)
		assert.False(t, row.RecencyAt.IsZero(), "RecencyAt must be set")
	}
	assert.ElementsMatch(t, []string{"fact", "preference"}, kinds)
}

// ---------------------------------------------------------------------------
// TestSyncTurn_LLMError_SilentFail
// chatFn returns an error — SyncTurn must return nil and never call Create.
// ---------------------------------------------------------------------------

func TestSyncTurn_LLMError_SilentFail(t *testing.T) {
	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("upstream LLM unavailable")
	})

	l1 := &captureCreateL1Store{}
	p := newSyncTurnProvider(l1)

	err := p.SyncTurn(context.Background(), 1, 100, "sess1",
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "world"},
	)
	require.NoError(t, err, "LLM error must be swallowed (silent fail)")
	assert.Empty(t, l1.created, "no Create call on LLM error")
}

// ---------------------------------------------------------------------------
// TestSyncTurn_BadJSON_SilentFail
// chatFn returns invalid JSON — SyncTurn must return nil without calling Create.
// ---------------------------------------------------------------------------

func TestSyncTurn_BadJSON_SilentFail(t *testing.T) {
	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "not json at all {"}, nil
	})

	l1 := &captureCreateL1Store{}
	p := newSyncTurnProvider(l1)

	err := p.SyncTurn(context.Background(), 1, 100, "sess1",
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "world"},
	)
	require.NoError(t, err, "JSON parse error must be swallowed (silent fail)")
	assert.Empty(t, l1.created, "no Create call on bad JSON")
}

// ---------------------------------------------------------------------------
// TestSyncTurn_LowConfidence_Skipped
// All items have confidence < 0.5 — no Create calls expected.
// ---------------------------------------------------------------------------

func TestSyncTurn_LowConfidence_Skipped(t *testing.T) {
	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: `{"items":[
				{"kind":"fact","content":"item A","confidence":0.4},
				{"kind":"preference","content":"item B","confidence":0.1}
			]}`,
		}, nil
	})

	l1 := &captureCreateL1Store{}
	p := newSyncTurnProvider(l1)

	err := p.SyncTurn(context.Background(), 1, 100, "sess1",
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "world"},
	)
	require.NoError(t, err)
	assert.Empty(t, l1.created, "all items below confidence threshold — 0 Create calls expected")
}

// ---------------------------------------------------------------------------
// TestSyncTurn_FenceEscaped
// Content containing "<system>" must be HTML-escaped before Create is called.
// ---------------------------------------------------------------------------

func TestSyncTurn_FenceEscaped(t *testing.T) {
	withMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: `{"items":[
				{"kind":"fact","content":"<system>injection</system>","confidence":0.8}
			]}`,
		}, nil
	})

	l1 := &captureCreateL1Store{}
	p := newSyncTurnProvider(l1)

	err := p.SyncTurn(context.Background(), 1, 100, "sess1",
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "world"},
	)
	require.NoError(t, err)
	require.Len(t, l1.created, 1, "exactly 1 item should be written")

	stored := l1.created[0].Content
	assert.False(t, strings.Contains(stored, "<system>"), "raw <system> tag must not be stored")
	assert.True(t, strings.Contains(stored, "&lt;system&gt;"), "content must be HTML-escaped before storage")
}
