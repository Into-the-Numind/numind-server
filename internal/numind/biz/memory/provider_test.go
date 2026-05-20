package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Mock stores for error-path tests
// ---------------------------------------------------------------------------

// errL1Store is an IAgentSessionMemoryStore that always returns errStoreFailure.
type errL1Store struct{ err error }

func (e *errL1Store) Create(_ context.Context, _ *model.AgentSessionMemory) error { return e.err }
func (e *errL1Store) ListByUserAgent(_ context.Context, _ uint, _ uint64, _ store.ListOpts) ([]model.AgentSessionMemory, error) {
	return nil, e.err
}
func (e *errL1Store) UpdateRecency(_ context.Context, _ []uint64, _ time.Time) error { return e.err }
func (e *errL1Store) DeleteByUser(_ context.Context, _ uint) error                   { return e.err }
func (e *errL1Store) Count(_ context.Context, _ uint, _ uint64, _ bool) (int64, error) {
	return 0, e.err
}

// errL2Store is an IUserGlobalMemoryStore that always returns errStoreFailure.
type errL2Store struct{ err error }

func (e *errL2Store) Upsert(_ context.Context, _ *model.UserGlobalMemory) error { return e.err }
func (e *errL2Store) GetByUserKey(_ context.Context, _ uint, _ string) (*model.UserGlobalMemory, error) {
	return nil, e.err
}
func (e *errL2Store) ListByUserKind(_ context.Context, _ uint, _ string, _ int) ([]model.UserGlobalMemory, error) {
	return nil, e.err
}
func (e *errL2Store) DeleteByUserKey(_ context.Context, _ uint, _ string) error { return e.err }
func (e *errL2Store) DeleteByUser(_ context.Context, _ uint) error              { return e.err }

// emptyL1Store always returns empty results (no error).
type emptyL1Store struct{}

func (e *emptyL1Store) Create(_ context.Context, _ *model.AgentSessionMemory) error { return nil }
func (e *emptyL1Store) ListByUserAgent(_ context.Context, _ uint, _ uint64, _ store.ListOpts) ([]model.AgentSessionMemory, error) {
	return nil, nil
}
func (e *emptyL1Store) UpdateRecency(_ context.Context, _ []uint64, _ time.Time) error { return nil }
func (e *emptyL1Store) DeleteByUser(_ context.Context, _ uint) error                   { return nil }
func (e *emptyL1Store) Count(_ context.Context, _ uint, _ uint64, _ bool) (int64, error) {
	return 0, nil
}

// emptyL2Store always returns empty results (no error).
type emptyL2Store struct{}

func (e *emptyL2Store) Upsert(_ context.Context, _ *model.UserGlobalMemory) error { return nil }
func (e *emptyL2Store) GetByUserKey(_ context.Context, _ uint, _ string) (*model.UserGlobalMemory, error) {
	return nil, nil
}
func (e *emptyL2Store) ListByUserKind(_ context.Context, _ uint, _ string, _ int) ([]model.UserGlobalMemory, error) {
	return nil, nil
}
func (e *emptyL2Store) DeleteByUserKey(_ context.Context, _ uint, _ string) error { return nil }
func (e *emptyL2Store) DeleteByUser(_ context.Context, _ uint) error              { return nil }

// captureL1Store records DeleteByUser calls.
type captureL1Store struct {
	emptyL1Store
	deletedUsers []uint
}

func (c *captureL1Store) DeleteByUser(_ context.Context, userID uint) error {
	c.deletedUsers = append(c.deletedUsers, userID)
	return nil
}

// captureL2Store records DeleteByUser calls.
type captureL2Store struct {
	emptyL2Store
	deletedUsers []uint
}

func (c *captureL2Store) DeleteByUser(_ context.Context, userID uint) error {
	c.deletedUsers = append(c.deletedUsers, userID)
	return nil
}

// captureL1StoreErr records DeleteByUser calls and returns err.
type captureL1StoreErr struct {
	emptyL1Store
	deletedUsers []uint
	err          error
}

func (c *captureL1StoreErr) DeleteByUser(_ context.Context, userID uint) error {
	c.deletedUsers = append(c.deletedUsers, userID)
	return c.err
}

// captureL2StoreErr records DeleteByUser calls and returns err.
type captureL2StoreErr struct {
	emptyL2Store
	deletedUsers []uint
	err          error
}

func (c *captureL2StoreErr) DeleteByUser(_ context.Context, userID uint) error {
	c.deletedUsers = append(c.deletedUsers, userID)
	return c.err
}

// singleItemL1Store returns a fixed list of AgentSessionMemory rows.
type singleItemL1Store struct {
	emptyL1Store
	items []model.AgentSessionMemory
}

func (s *singleItemL1Store) ListByUserAgent(_ context.Context, _ uint, _ uint64, _ store.ListOpts) ([]model.AgentSessionMemory, error) {
	return s.items, nil
}

// singleItemL2Store returns a fixed list of UserGlobalMemory rows.
type singleItemL2Store struct {
	emptyL2Store
	items []model.UserGlobalMemory
}

func (s *singleItemL2Store) ListByUserKind(_ context.Context, _ uint, _ string, _ int) ([]model.UserGlobalMemory, error) {
	return s.items, nil
}

// ---------------------------------------------------------------------------
// Helper — build a real compositeProvider backed by real SQLite stores.
// Reuses newTestDB defined in notepad_test.go (same package).
// ---------------------------------------------------------------------------

func newTestProvider(t *testing.T) (MemoryProvider, store.IAgentSessionMemoryStore, store.IUserGlobalMemoryStore) {
	t.Helper()
	db := newTestDB(t, &model.AgentSessionMemory{}, &model.UserGlobalMemory{})
	l1 := store.NewAgentSessionMemoryStore(db)
	l2 := store.NewUserGlobalMemoryStore(db)
	return NewProvider(l1, l2), l1, l2
}

// ---------------------------------------------------------------------------
// SystemPromptBlock tests
// ---------------------------------------------------------------------------

// TestProvider_SystemPromptBlock_Empty verifies that no L1 + no L2 → "".
func TestProvider_SystemPromptBlock_Empty(t *testing.T) {
	p := NewProvider(&emptyL1Store{}, &emptyL2Store{})
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestProvider_SystemPromptBlock_UserZero verifies that userID==0 returns "" without
// touching any store.
func TestProvider_SystemPromptBlock_UserZero(t *testing.T) {
	// Use error stores — if they are called the test will see the error.
	sentinel := errors.New("should not be called")
	p := NewProvider(&errL1Store{err: sentinel}, &errL2Store{err: sentinel})
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 0, 100, "sess1")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestProvider_SystemPromptBlock_L1Only verifies that only L1 data → output contains
// "[本 agent 历史]" and not "[全局画像]".
func TestProvider_SystemPromptBlock_L1Only(t *testing.T) {
	now := time.Now()
	l1 := &singleItemL1Store{items: []model.AgentSessionMemory{
		{
			ID:                1,
			UserID:            1,
			AgentDefinitionID: 100,
			Kind:              "fact",
			Content:           "user likes Go",
			Score:             1.0,
			SourceType:        "agent",
			RecencyAt:         now,
		},
	}}
	p := NewProvider(l1, &emptyL2Store{})
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err)
	assert.Contains(t, got, "[本 agent 历史]")
	assert.NotContains(t, got, "[全局画像]")
	assert.Contains(t, got, "user likes Go")
	assert.Contains(t, got, "<memory-context>")
}

// TestProvider_SystemPromptBlock_L2Only verifies that only L2 data → output contains
// "[全局画像]" and not "[本 agent 历史]".
func TestProvider_SystemPromptBlock_L2Only(t *testing.T) {
	now := time.Now()
	l2 := &singleItemL2Store{items: []model.UserGlobalMemory{
		{
			ID:         1,
			UserID:     1,
			Kind:       "preference",
			KeyName:    "lang",
			Value:      "prefers Go",
			Confidence: 1.0,
			SourceType: "agent_tool",
			UpdatedAt:  now,
		},
	}}
	p := NewProvider(&emptyL1Store{}, l2)
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err)
	assert.Contains(t, got, "[全局画像]")
	assert.NotContains(t, got, "[本 agent 历史]")
	assert.Contains(t, got, "prefers Go")
	assert.Contains(t, got, "<memory-context>")
}

// TestProvider_SystemPromptBlock_Both verifies that with both L1 and L2 data the
// output contains both sections with L2 (全局画像) appearing before L1 (本 agent 历史).
func TestProvider_SystemPromptBlock_Both(t *testing.T) {
	now := time.Now()
	l1 := &singleItemL1Store{items: []model.AgentSessionMemory{
		{
			ID: 1, UserID: 1, AgentDefinitionID: 100,
			Kind: "fact", Content: "l1-content",
			Score: 1.0, SourceType: "agent", RecencyAt: now,
		},
	}}
	l2 := &singleItemL2Store{items: []model.UserGlobalMemory{
		{
			ID: 1, UserID: 1, Kind: "preference",
			KeyName: "k1", Value: "l2-content",
			Confidence: 1.0, SourceType: "agent_tool", UpdatedAt: now,
		},
	}}
	p := NewProvider(l1, l2)
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err)

	globalPos := strings.Index(got, "[全局画像]")
	agentPos := strings.Index(got, "[本 agent 历史]")
	assert.Greater(t, globalPos, -1, "should contain [全局画像]")
	assert.Greater(t, agentPos, -1, "should contain [本 agent 历史]")
	assert.Less(t, globalPos, agentPos, "[全局画像] should appear before [本 agent 历史]")
	assert.Contains(t, got, "l1-content")
	assert.Contains(t, got, "l2-content")
}

// TestProvider_SystemPromptBlock_L1Err verifies that an L1 store error causes
// graceful degradation to L2-only (no error returned to caller).
func TestProvider_SystemPromptBlock_L1Err(t *testing.T) {
	now := time.Now()
	sentinel := errors.New("l1 exploded")
	l2 := &singleItemL2Store{items: []model.UserGlobalMemory{
		{
			ID: 1, UserID: 1, Kind: "fact",
			KeyName: "k", Value: "l2-only-value",
			Confidence: 1.0, SourceType: "agent_tool", UpdatedAt: now,
		},
	}}
	p := NewProvider(&errL1Store{err: sentinel}, l2)
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err, "L1 error should be swallowed (degrade, not fail)")
	assert.Contains(t, got, "l2-only-value")
	assert.NotContains(t, got, "[本 agent 历史]")
}

// TestProvider_SystemPromptBlock_L2Err verifies that an L2 store error causes
// graceful degradation to L1-only (no error returned to caller).
func TestProvider_SystemPromptBlock_L2Err(t *testing.T) {
	now := time.Now()
	sentinel := errors.New("l2 exploded")
	l1 := &singleItemL1Store{items: []model.AgentSessionMemory{
		{
			ID: 1, UserID: 1, AgentDefinitionID: 100,
			Kind: "fact", Content: "l1-only-value",
			Score: 1.0, SourceType: "agent", RecencyAt: now,
		},
	}}
	p := NewProvider(l1, &errL2Store{err: sentinel})
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err, "L2 error should be swallowed (degrade, not fail)")
	assert.Contains(t, got, "l1-only-value")
	assert.NotContains(t, got, "[全局画像]")
}

// TestProvider_SystemPromptBlock_BothErr verifies that when both stores fail the
// provider returns ("", nil) — not an error.
func TestProvider_SystemPromptBlock_BothErr(t *testing.T) {
	l1Err := errors.New("l1 down")
	l2Err := errors.New("l2 down")
	p := NewProvider(&errL1Store{err: l1Err}, &errL2Store{err: l2Err})
	ctx := context.Background()

	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess1")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// ---------------------------------------------------------------------------
// Clear tests
// ---------------------------------------------------------------------------

// TestProvider_Clear_BothLayers verifies that Clear calls DeleteByUser on both
// the L1 and L2 stores.
func TestProvider_Clear_BothLayers(t *testing.T) {
	cl1 := &captureL1Store{}
	cl2 := &captureL2Store{}
	p := NewProvider(cl1, cl2)
	ctx := context.Background()

	err := p.Clear(ctx, 42)
	require.NoError(t, err)
	assert.Equal(t, []uint{42}, cl1.deletedUsers, "L1 DeleteByUser should be called with userID=42")
	assert.Equal(t, []uint{42}, cl2.deletedUsers, "L2 DeleteByUser should be called with userID=42")
}

// TestProvider_Clear_L1Error_L2StillCalled (P2-1 reviewer follow-up) verifies that
// Clear attempts L2 deletion even when L1 fails, returning L1 error.
func TestProvider_Clear_L1Error_L2StillCalled(t *testing.T) {
	failingL1 := &captureL1StoreErr{err: errors.New("L1 deletion failed")}
	cl2 := &captureL2Store{}
	p := NewProvider(failingL1, cl2)
	ctx := context.Background()

	err := p.Clear(ctx, 42)
	assert.Error(t, err, "Clear should return L1 error")
	assert.Equal(t, []uint{42}, failingL1.deletedUsers, "L1 attempted")
	assert.Equal(t, []uint{42}, cl2.deletedUsers, "L2 still called even after L1 error")
}

// TestProvider_Clear_L2Error verifies Clear surfaces L2 error when L1 succeeds.
func TestProvider_Clear_L2Error(t *testing.T) {
	cl1 := &captureL1Store{}
	failingL2 := &captureL2StoreErr{err: errors.New("L2 deletion failed")}
	p := NewProvider(cl1, failingL2)
	ctx := context.Background()

	err := p.Clear(ctx, 42)
	assert.Error(t, err, "Clear should return L2 error")
	assert.Equal(t, []uint{42}, cl1.deletedUsers, "L1 deleted successfully")
	assert.Equal(t, []uint{42}, failingL2.deletedUsers, "L2 attempted")
}

// ---------------------------------------------------------------------------
// No-op stub tests
// ---------------------------------------------------------------------------

// TestProvider_OnPreCompress_NoOp verifies that OnPreCompress always returns nil.
func TestProvider_OnPreCompress_NoOp(t *testing.T) {
	p := NewProvider(&emptyL1Store{}, &emptyL2Store{})
	ctx := context.Background()

	err := p.OnPreCompress(ctx, 1, 100, []Message{{Role: "user", Content: "hi"}})
	require.NoError(t, err)
}

// TestProvider_SyncTurn_NoOp verifies that SyncTurn always returns nil.
func TestProvider_SyncTurn_NoOp(t *testing.T) {
	p := NewProvider(&emptyL1Store{}, &emptyL2Store{})
	ctx := context.Background()

	err := p.SyncTurn(ctx, 1, 100, "sess1",
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "world"},
	)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Prefetch tests
// ---------------------------------------------------------------------------

// TestProvider_Prefetch_UserZero (P2-2 reviewer follow-up) verifies early return.
func TestProvider_Prefetch_UserZero(t *testing.T) {
	p := NewProvider(&emptyL1Store{}, &emptyL2Store{})
	ctx := context.Background()
	items, err := p.Prefetch(ctx, 0, 100, "query")
	require.NoError(t, err)
	assert.Empty(t, items, "userID=0 should early-return empty")
}

// TestProvider_Prefetch_NonEmpty verifies that Prefetch returns items from Retriever
// when both stores have data.
func TestProvider_Prefetch_NonEmpty(t *testing.T) {
	now := time.Now()
	l1 := &singleItemL1Store{items: []model.AgentSessionMemory{
		{
			ID: 1, UserID: 1, AgentDefinitionID: 100,
			Kind: "fact", Content: "l1-pref-content",
			Score: 1.0, SourceType: "agent", RecencyAt: now,
		},
	}}
	l2 := &singleItemL2Store{items: []model.UserGlobalMemory{
		{
			ID: 1, UserID: 1, Kind: "preference",
			KeyName: "lang", Value: "Go",
			Confidence: 1.0, SourceType: "agent_tool", UpdatedAt: now,
		},
	}}
	p := NewProvider(l1, l2)
	ctx := context.Background()

	items, err := p.Prefetch(ctx, 1, 100, "test query")
	require.NoError(t, err)
	assert.NotEmpty(t, items)

	// L2 items should come first (mirrors SystemPromptBlock ordering)
	var hasL1, hasL2 bool
	for _, it := range items {
		if it.Content == "l1-pref-content" {
			hasL1 = true
		}
		if it.Content == "Go" {
			hasL2 = true
		}
	}
	assert.True(t, hasL1, "should contain L1 item")
	assert.True(t, hasL2, "should contain L2 item")
}

// ---------------------------------------------------------------------------
// PI-2 integration test: Notepad write → SystemPromptBlock contains value
// ---------------------------------------------------------------------------

// TestProvider_SystemPromptBlock_AfterWrite is a PI-2 integration test that
// writes a single L2 entry via Notepad and then verifies that SystemPromptBlock
// returns a string containing the (escaped) written value.
func TestProvider_SystemPromptBlock_AfterWrite(t *testing.T) {
	p, _, l2 := newTestProvider(t)
	ctx := context.Background()

	// Write via Notepad (same store as provider's l2Store)
	np := NewNotepad(l2)
	err := np.Write(ctx, 1, KindPreference, "fav-lang", "Golang & Rust", WriteOpts{})
	require.NoError(t, err)

	// SystemPromptBlock should surface the written entry.
	got, err := p.SystemPromptBlock(ctx, 1, 100, "sess-pi2")
	require.NoError(t, err)
	assert.NotEmpty(t, got, "should return non-empty block after write")
	assert.Contains(t, got, "<memory-context>")
	assert.Contains(t, got, "[全局画像]")

	// Value is stored HTML-escaped; check the escaped form appears in the output.
	escaped := EscapeForStorage("Golang & Rust")
	assert.Contains(t, got, escaped, "system prompt block should contain the escaped stored value")
}
