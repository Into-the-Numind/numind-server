package agent

import "numind-server/internal/numind/biz/narration"

// persistedToolCall is the durable, frontend-shaped aggregate of one tool call's
// narration events. It is written into agent_run.messages as part of a
// {"role":"tool_group","tool_calls":[...]} turn at finalize, and read back by
// transformMessages on session snapshot. The JSON shape is 1:1 with the
// frontend ToolCallAggregate (numind-web-v3 src/types/agent.ts), so the FE
// renders it via AgentToolCallList with no transform.
type persistedToolCall struct {
	ToolCallID   string `json:"tool_call_id"`
	ToolName     string `json:"tool_name"`
	CurrentState string `json:"current_state"`
	// Preview is kept for shape-parity with the frontend ToolCallAggregate but is
	// intentionally NOT populated on persist: the live preview comes from the
	// transient tool_call_result SSE event (the raw tool output), which narration
	// events do not carry. On reload the timeline is conveyed via Events instead.
	Preview      string            `json:"preview,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	Events       []narration.Event `json:"events"`
}

// aggregateToolEvents folds an ordered narration event stream into per-tool-call
// aggregates, preserving first-seen order. current_state tracks the latest event;
// error_message is filled from the terminal error/rejected event's reason
// (falling back to its learner-facing message). Returns nil when there are no
// events, so the caller can skip writing an empty tool_group turn.
func aggregateToolEvents(events []narration.Event) []persistedToolCall {
	if len(events) == 0 {
		return nil
	}
	order := make([]string, 0)
	byID := make(map[string]*persistedToolCall)
	for _, ev := range events {
		id := ev.ToolCallID
		agg, ok := byID[id]
		if !ok {
			agg = &persistedToolCall{ToolCallID: id, ToolName: ev.ToolName}
			byID[id] = agg
			order = append(order, id)
		}
		if ev.ToolName != "" {
			agg.ToolName = ev.ToolName
		}
		agg.Events = append(agg.Events, ev)
		agg.CurrentState = string(ev.State)
		if ev.State == narration.StateError || ev.State == narration.StateRejected {
			// Last terminal error wins. The current tool lifecycle emits exactly one
			// terminal event per tool_call_id, so this is unambiguous in practice; a
			// future retriable tool would keep its most recent error here.
			if ev.Reason != "" {
				agg.ErrorMessage = ev.Reason
			} else {
				agg.ErrorMessage = ev.Message
			}
		}
	}
	out := make([]persistedToolCall, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}
