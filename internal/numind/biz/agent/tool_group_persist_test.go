package agent

import (
	"context"
	"encoding/json"
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

// Customer regression (Dev run 252): after the first authorization, successful
// Base create/list calls existed only as UI narration. A second authorization
// yield rebuilt provider history without those results, so the model created a
// second Base and repeated a field write.
func TestBuildTranscriptTurns_PreservesSafeLarkResultsForNextAuthorization(t *testing.T) {
	base := time.Now()
	steps := []stepEntry{
		{Content: "创建多维表格", Reasoning: "先创建并读取表", TS: base},
		{Content: "继续创建字段", Reasoning: "已经拿到 base token", TS: base.Add(3 * time.Millisecond)},
	}
	events := []narration.Event{
		{
			RunID: 252, ToolCallID: "tc-base-create", ToolName: "lark_execute",
			State: narration.StateResult, Message: "操作完成", Timestamp: base.Add(time.Millisecond),
			InternalInput:  json.RawMessage(`{"argv":["base","+base-create","--name","验收"]}`),
			InternalResult: json.RawMessage(`{"ok":true,"state":"succeeded","data":{"base_token":"bas_1"}}`),
		},
	}

	turns := buildTranscriptTurns("创建 Base", steps, events, "", "")
	require.Len(t, turns, 6)
	assert.Equal(t, "tool_group", turns[2]["role"])
	assert.Equal(t, "assistant", turns[3]["role"])
	calls, ok := turns[3]["tool_calls"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, calls, 1)
	function := calls[0]["function"].(map[string]any)
	assert.Equal(t, "lark_execute", function["name"])
	assert.Equal(t, `{}`, function["arguments"], "model-supplied argv must never be persisted for replay")
	assert.Equal(t, "tool", turns[4]["role"])
	assert.Equal(t, "tc-base-create", turns[4]["tool_call_id"])
	assert.JSONEq(t,
		`{"ok":true,"state":"succeeded","data":{"base_token":"bas_1"}}`,
		turns[4]["content"].(string),
	)
	assert.Equal(t, "assistant", turns[5]["role"])

	// Exercise the real persistence boundary: JSON turns are decoded back into
	// []map[string]any before the next authorization continuation rebuilds the
	// provider protocol history.
	encoded, err := json.Marshal(turns)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	history, err := turnsToExternalResumeHistoryMessages(decoded, "")
	require.NoError(t, err)
	foundResult := false
	for _, message := range history {
		if message.Content == turns[4]["content"].(string) {
			foundResult = true
			break
		}
	}
	assert.True(t, foundResult, "the next authorization leg must receive the successful Base result")
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

func TestBuildTranscriptTurns_NilWhenNoSteps(t *testing.T) {
	// Non-stream Run captures no steps → caller falls back to collapsed shape.
	assert.Nil(t, buildTranscriptTurns("hi", nil, nil, "answer", ""))
	assert.Nil(t, buildTranscriptTurns("hi", []stepEntry{}, nil, "answer", ""))
}

func TestBuildTranscriptTurns_InterleavesByTimestamp(t *testing.T) {
	base := time.Now()
	t1, t2, t3 := base, base.Add(time.Millisecond), base.Add(2*time.Millisecond)
	steps := []stepEntry{
		{Content: "我先查数据", Reasoning: "think1", TS: t1},
		{Content: "这是结果", Reasoning: "think2", TS: t3},
	}
	// web_search fires at t2 — between step1 (t1) and step2 (t3) → belongs to step1.
	events := []narration.Event{
		{RunID: 1, ToolCallID: "tc1", ToolName: "web_search", State: narration.StateResult, Message: "已获取搜索结果", Timestamp: t2},
	}
	turns := buildTranscriptTurns("查销量", steps, events, "这是结果（最终）", "think2")

	require.Len(t, turns, 4)
	assert.Equal(t, "user", turns[0]["role"])
	assert.Equal(t, "查销量", turns[0]["content"])

	assert.Equal(t, "assistant", turns[1]["role"])
	assert.Equal(t, "我先查数据", turns[1]["content"]) // intermediate step kept verbatim
	assert.Equal(t, "think1", turns[1]["reasoning"])

	assert.Equal(t, "tool_group", turns[2]["role"])
	tcs := turns[2]["tool_calls"].([]persistedToolCall)
	require.Len(t, tcs, 1)
	assert.Equal(t, "web_search", tcs[0].ToolName)

	// final assistant turn reconciled to the authoritative final content.
	assert.Equal(t, "assistant", turns[3]["role"])
	assert.Equal(t, "这是结果（最终）", turns[3]["content"])
}

func TestBuildTranscriptTurns_ErrorEndingOnToolGroupAppendsAssistant(t *testing.T) {
	base := time.Now()
	steps := []stepEntry{{Content: "查一下", Reasoning: "", TS: base}}
	events := []narration.Event{
		{RunID: 1, ToolCallID: "tc1", ToolName: "web_search", State: narration.StateError, Reason: "超时", Timestamp: base.Add(time.Millisecond)},
	}
	// run errored after the tool → finalContent is a friendly error message.
	turns := buildTranscriptTurns("q", steps, events, "执行出错", "")
	require.Len(t, turns, 4)
	assert.Equal(t, "assistant", turns[1]["role"]) // the step that called the tool
	assert.Equal(t, "tool_group", turns[2]["role"])
	assert.Equal(t, "assistant", turns[3]["role"]) // appended final error turn
	assert.Equal(t, "执行出错", turns[3]["content"])
}

func TestBuildTranscriptTurns_ClearsStaleReasoningOnFinal(t *testing.T) {
	// The last captured step has its own reasoning, but the run's finalReasoning
	// is empty — the reconciled final answer turn must NOT carry the stale step
	// reasoning (else the FE attributes it to the final answer).
	steps := []stepEntry{{Content: "答案", Reasoning: "旧思考", TS: time.Now()}}
	turns := buildTranscriptTurns("q", steps, nil, "最终答案", "")
	require.Len(t, turns, 2)
	last := turns[1]
	assert.Equal(t, "assistant", last["role"])
	assert.Equal(t, "最终答案", last["content"])
	_, hasReasoning := last["reasoning"]
	assert.False(t, hasReasoning, "stale reasoning must be cleared when finalReasoning is empty")
}

func TestBuildTranscriptTurns_SingleStepNoTools(t *testing.T) {
	// Direct answer with no tools → [user, assistant]; matches collapsed shape.
	steps := []stepEntry{{Content: "直接回答", Reasoning: "想一下", TS: time.Now()}}
	turns := buildTranscriptTurns("q", steps, nil, "直接回答", "想一下")
	require.Len(t, turns, 2)
	assert.Equal(t, "user", turns[0]["role"])
	assert.Equal(t, "assistant", turns[1]["role"])
	assert.Equal(t, "直接回答", turns[1]["content"])
}
