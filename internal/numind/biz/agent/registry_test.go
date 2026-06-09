package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// ── mock stores ──────────────────────────────────────────────────────────────

type mockDefStore struct {
	mu      sync.Mutex
	upserts map[string]*model.ToolDefinition
}

func newMockDefStore() *mockDefStore {
	return &mockDefStore{upserts: map[string]*model.ToolDefinition{}}
}

func (m *mockDefStore) Upsert(_ context.Context, def *model.ToolDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts[def.ToolName] = def
	return nil
}

func (m *mockDefStore) Get(_ context.Context, _ string) (*model.ToolDefinition, error) {
	return nil, nil
}

func (m *mockDefStore) ListEnabled(_ context.Context) ([]model.ToolDefinition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.ToolDefinition, 0, len(m.upserts))
	for _, d := range m.upserts {
		if d.IsEnabled {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *mockDefStore) ListBySource(_ context.Context, _ string) ([]model.ToolDefinition, error) {
	return nil, nil
}

func (m *mockDefStore) SetEnabled(_ context.Context, _ string, _ bool) error { return nil }

type mockFacStore struct {
	mu      sync.Mutex
	upserts map[string]*model.ToolFactoryRegistryRow
}

func newMockFacStore() *mockFacStore {
	return &mockFacStore{upserts: map[string]*model.ToolFactoryRegistryRow{}}
}

func (m *mockFacStore) Upsert(_ context.Context, row *model.ToolFactoryRegistryRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts[row.FactoryID] = row
	return nil
}

func (m *mockFacStore) List(_ context.Context) ([]model.ToolFactoryRegistryRow, error) {
	return nil, nil
}

func (m *mockFacStore) UpdateLoadStats(_ context.Context, _ string, _ int, _ time.Time) error {
	return nil
}

// ── minimal test tool ────────────────────────────────────────────────────────

type testTool struct {
	BaseTool
	name string
}

func (t *testTool) Name() string                                               { return t.name }
func (t *testTool) Description() string                                        { return "test-tool-desc" }
func (t *testTool) UserFacingName() string                                     { return t.name }
func (t *testTool) NarrationVerb() string                                      { return "执行" }
func (t *testTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) { return nil, nil }

// ── stub factory ─────────────────────────────────────────────────────────────

type stubFactory struct {
	id       string
	src      string
	tools    []FullTool
	metadata []ToolMetadata
	err      error
}

func (f *stubFactory) FactoryID() string   { return f.id }
func (f *stubFactory) Source() string      { return f.src }
func (f *stubFactory) DisplayName() string { return "stub-" + f.id }

func (f *stubFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.tools, f.metadata, nil
}

func (f *stubFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error { return nil }

// ── tests ────────────────────────────────────────────────────────────────────

func TestRegistry_LoadAll_Basic(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:  "f1",
		src: "platform",
		tools: []FullTool{
			&testTool{name: "tool_a"},
			&testTool{name: "tool_b"},
		},
		metadata: []ToolMetadata{
			{ToolName: "tool_a", Source: "platform"},
			{ToolName: "tool_b", Source: "platform"},
		},
	})
	require.NoError(t, r.LoadAll(context.Background()))

	if _, ok := r.GetTool("tool_a"); !ok {
		t.Error("tool_a should be registered")
	}
	if _, ok := r.GetTool("tool_b"); !ok {
		t.Error("tool_b should be registered")
	}
	assert.Len(t, r.ListAllTools(), 2)
}

func TestRegistry_LoadAll_LengthMismatch_Fails(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:  "f",
		src: "platform",
		tools: []FullTool{
			&testTool{name: "a"},
		},
		metadata: []ToolMetadata{
			{ToolName: "a"},
			{ToolName: "b"}, // mismatch: 2 metadata for 1 tool
		},
	})
	err := r.LoadAll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "length mismatch")
}

func TestRegistry_LoadAll_NameMismatch_Fails(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:  "f",
		src: "platform",
		tools: []FullTool{
			&testTool{name: "actual_name"},
		},
		metadata: []ToolMetadata{
			{ToolName: "wrong_name"},
		},
	})
	err := r.LoadAll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong_name")
}

func TestRegistry_GetTool_NotFound(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_, ok := r.GetTool("nonexistent")
	assert.False(t, ok)
}

func TestRegistry_ConcurrentGetTool_Race(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:       "f",
		src:      "platform",
		tools:    []FullTool{&testTool{name: "shared"}},
		metadata: []ToolMetadata{{ToolName: "shared"}},
	})
	require.NoError(t, r.LoadAll(context.Background()))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.GetTool("shared")
		}()
	}
	wg.Wait()
}

func TestRegistry_RegisterFactory_Nil_Fails(t *testing.T) {
	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	err := r.RegisterFactory(nil)
	require.Error(t, err)
}

func TestRegistry_LoadAll_UpsertsFactoryRegistry(t *testing.T) {
	facStore := newMockFacStore()
	r := NewAgentToolRegistry(newMockDefStore(), facStore)
	_ = r.RegisterFactory(&stubFactory{
		id:       "platform-builtin",
		src:      "platform",
		tools:    []FullTool{&testTool{name: "a"}},
		metadata: []ToolMetadata{{ToolName: "a"}},
	})
	require.NoError(t, r.LoadAll(context.Background()))
	assert.Len(t, facStore.upserts, 1)
	row := facStore.upserts["platform-builtin"]
	require.NotNil(t, row)
	assert.Equal(t, 1, row.LoadedToolsCount)
	assert.NotNil(t, row.LastLoadedAt)
}

// TestListAllTools_DeterministicOrder is the prefix-stability regression guard
// (llm-prompt-cache §4.8, D4 agent verdict). The agent's tool list is rendered
// into the LLM request's `tools` array; if its order varies call-to-call, the
// serialized prompt prefix changes byte-for-byte and the provider's auto
// prefix-cache never fires. ListAllTools() previously ranged a Go map, whose
// iteration order is randomized — so two consecutive calls could (and over many
// turns would) emit different orderings.
//
// This test registers tools in REVERSE-alphabetical order, then asserts that
// ListAllTools() returns a STABLE, ASCENDING-by-Name() sequence across two
// consecutive calls. It fails today (map iteration) and passes after the sort
// fix. Name() == the LLM-facing ToolName (adapter_full_to_eino.go:295), so
// sorting by Name() directly stabilises the prompt's tool-identity order; both
// runner consumers (runner.go / runner_runstream.go) range+filter the result and
// thus inherit a sorted subset without change.
func TestListAllTools_DeterministicOrder(t *testing.T) {
	// Register a wide set in reverse-alphabetical order. A large N makes Go's
	// randomized map iteration practically certain to differ from sorted order
	// (and from itself across calls) without the fix.
	names := []string{
		"tool_zeta", "tool_yankee", "tool_xray", "tool_whiskey", "tool_victor",
		"tool_uniform", "tool_tango", "tool_sierra", "tool_romeo", "tool_quebec",
		"tool_papa", "tool_oscar", "tool_november", "tool_mike", "tool_lima",
		"tool_kilo", "tool_juliet", "tool_india", "tool_hotel", "tool_golf",
		"tool_foxtrot", "tool_echo", "tool_delta", "tool_charlie", "tool_bravo",
		"tool_alpha",
	}

	tools := make([]FullTool, 0, len(names))
	meta := make([]ToolMetadata, 0, len(names))
	for _, n := range names {
		tools = append(tools, &testTool{name: n})
		meta = append(meta, ToolMetadata{ToolName: n, Source: "platform"})
	}

	r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:       "f-order",
		src:      "platform",
		tools:    tools,
		metadata: meta,
	})
	require.NoError(t, r.LoadAll(context.Background()))

	extract := func(ts []FullTool) []string {
		out := make([]string, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.Name())
		}
		return out
	}

	first := extract(r.ListAllTools())
	second := extract(r.ListAllTools())

	require.Len(t, first, len(names), "all registered tools should be returned")

	// 1. Two consecutive calls must be byte-identical in order.
	assert.Equal(t, first, second,
		"ListAllTools() must return a deterministic order across consecutive calls "+
			"(else the LLM `tools` array order varies and the provider prefix-cache never hits)")

	// 2. The order must be ascending by Name() (the LLM-facing ToolName).
	want := make([]string, len(names))
	copy(want, names)
	sort.Strings(want)
	assert.Equal(t, want, first,
		"ListAllTools() must be sorted ascending by Name() so the rendered tool prefix is byte-stable")
}

// TestListAllTools_DeterministicOrder_AcrossFreshRegistries hardens the guard
// against a single-process map seed: two independently-built registries that
// loaded the SAME tools in reverse order must still emit the identical sorted
// sequence. Without the sort, two distinct map instances can iterate in
// different orders even within one test binary.
func TestListAllTools_DeterministicOrder_AcrossFreshRegistries(t *testing.T) {
	names := make([]string, 0, 24)
	for i := 23; i >= 0; i-- { // reverse: tool_23 .. tool_00
		names = append(names, fmt.Sprintf("tool_%02d", i))
	}

	build := func() AgentToolRegistry {
		tools := make([]FullTool, 0, len(names))
		meta := make([]ToolMetadata, 0, len(names))
		for _, n := range names {
			tools = append(tools, &testTool{name: n})
			meta = append(meta, ToolMetadata{ToolName: n, Source: "platform"})
		}
		r := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
		_ = r.RegisterFactory(&stubFactory{id: "f", src: "platform", tools: tools, metadata: meta})
		require.NoError(t, r.LoadAll(context.Background()))
		return r
	}

	extract := func(ts []FullTool) []string {
		out := make([]string, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.Name())
		}
		return out
	}

	a := extract(build().ListAllTools())
	b := extract(build().ListAllTools())
	assert.Equal(t, a, b, "two fresh registries must emit the identical deterministic tool order")
}

func TestRegistry_ListEnabled_FiltersDisabled(t *testing.T) {
	defStore := newMockDefStore()
	r := NewAgentToolRegistry(defStore, newMockFacStore())
	_ = r.RegisterFactory(&stubFactory{
		id:  "f",
		src: "platform",
		tools: []FullTool{
			&testTool{name: "enabled_tool"},
			&testTool{name: "disabled_tool"},
		},
		metadata: []ToolMetadata{
			{ToolName: "enabled_tool"},
			{ToolName: "disabled_tool"},
		},
	})
	require.NoError(t, r.LoadAll(context.Background()))

	// Mark disabled_tool as disabled directly in mock store.
	defStore.mu.Lock()
	defStore.upserts["disabled_tool"].IsEnabled = false
	defStore.mu.Unlock()

	enabled, err := r.ListEnabled(context.Background())
	require.NoError(t, err)
	assert.Len(t, enabled, 1)
	assert.Equal(t, "enabled_tool", enabled[0].Name())
}
