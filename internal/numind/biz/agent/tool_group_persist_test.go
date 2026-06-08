package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/narration"
)

func TestAggregateToolEvents_GroupsByToolCallID(t *testing.T) {
	ts := time.Now()
	events := []narration.Event{
		{RunID: 1, ToolCallID: "tc-1", ToolName: "web_search", State: narration.StateUse, Message: "搜索中", Timestamp: ts},
		{RunID: 1, ToolCallID: "tc-2", ToolName: "image_gen", State: narration.StateUse, Message: "生成中", Timestamp: ts},
		{RunID: 1, ToolCallID: "tc-1", ToolName: "web_search", State: narration.StateResult, Message: "完成", Timestamp: ts},
		{RunID: 1, ToolCallID: "tc-2", ToolName: "image_gen", State: narration.StateError, Reason: "配额不足", Message: "失败", Timestamp: ts},
	}

	groups := aggregateToolEvents(events)
	require.Len(t, groups, 2)

	// First-seen order preserved: tc-1 then tc-2.
	assert.Equal(t, "tc-1", groups[0].ToolCallID)
	assert.Equal(t, "web_search", groups[0].ToolName)
	assert.Equal(t, "result", groups[0].CurrentState) // latest state wins
	assert.Len(t, groups[0].Events, 2)
	assert.Empty(t, groups[0].ErrorMessage)

	assert.Equal(t, "tc-2", groups[1].ToolCallID)
	assert.Equal(t, "error", groups[1].CurrentState)
	assert.Equal(t, "配额不足", groups[1].ErrorMessage) // reason preferred over message
}

func TestAggregateToolEvents_Empty(t *testing.T) {
	assert.Nil(t, aggregateToolEvents(nil))
	assert.Nil(t, aggregateToolEvents([]narration.Event{}))
}

func TestEventCollector_NilSafeAndOrdered(t *testing.T) {
	// nil collector: add no-ops, Events returns nil.
	var nilC *narration.EventCollector
	assert.Nil(t, nilC.Events())

	ctx := narration.WithCollector(context.Background())
	c := narration.CollectorFrom(ctx)
	require.NotNil(t, c)
	assert.Nil(t, c.Events()) // empty → nil so caller can skip the turn
}
