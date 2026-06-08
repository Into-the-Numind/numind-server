package narration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventCollector_AddAndOrder(t *testing.T) {
	ctx := WithCollector(context.Background())
	c := CollectorFrom(ctx)
	require.NotNil(t, c)

	c.add(Event{ToolCallID: "a", State: StateUse})
	c.add(Event{ToolCallID: "b", State: StateResult})

	got := c.Events()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ToolCallID)
	assert.Equal(t, "b", got[1].ToolCallID)

	// Events returns a copy — mutating it must not affect the collector.
	got[0].ToolCallID = "mutated"
	assert.Equal(t, "a", c.Events()[0].ToolCallID)
}

func TestEventCollector_NilSafe(t *testing.T) {
	var c *EventCollector
	c.add(Event{ToolCallID: "x"}) // must not panic
	assert.Nil(t, c.Events())
	// CollectorFrom on a ctx without a collector returns nil.
	assert.Nil(t, CollectorFrom(context.Background()))
}

func TestEventCollector_CapDropsOverflow(t *testing.T) {
	ctx := WithCollector(context.Background())
	c := CollectorFrom(ctx)
	for i := 0; i < maxCollect+50; i++ {
		c.add(Event{ToolCallID: "t"})
	}
	assert.Len(t, c.Events(), maxCollect)
	assert.Equal(t, 50, c.dropped)
}

// TestProvider_Emit_PopulatesCollector covers the wiring the run finalizer relies
// on: Provider.Emit must record every built event into the run-ctx collector so
// the tool-call timeline can be persisted. Without this the tool_group turn would
// be empty on reload.
func TestProvider_Emit_PopulatesCollector(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	ch, cleanup := p.Subscribe(7)
	defer cleanup()

	ctx := WithCollector(context.Background())
	p.Emit(ctx, 7, "bash_exec", StateUse, EmitPayload{ToolCallID: "tc-9"})
	p.Emit(ctx, 7, "bash_exec", StateResult, EmitPayload{ToolCallID: "tc-9"})
	// Drain the stream channel so the buffered streamer doesn't retain references.
	<-ch
	<-ch

	got := CollectorFrom(ctx).Events()
	require.Len(t, got, 2)
	assert.Equal(t, "tc-9", got[0].ToolCallID)
	assert.Equal(t, StateUse, got[0].State)
	assert.Equal(t, StateResult, got[1].State)

	// A run with no collector on ctx must not panic (non-student paths).
	p.Emit(context.Background(), 7, "bash_exec", StateResult, EmitPayload{ToolCallID: "tc-x"})
}
