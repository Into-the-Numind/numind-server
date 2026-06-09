package agent

import (
	"time"

	"numind-server/internal/numind/biz/narration"
)

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

// buildTranscriptTurns interleaves each ReAct step's assistant turn with the
// tool calls that step triggered, in chronological order, producing the full
// persisted transcript for a streaming run:
//
//	[user, assistant(step1), tool_group(step1), assistant(step2), …, final assistant]
//
// Returns nil when no steps were captured (non-stream Run, or no streaming), so
// the caller falls back to the collapsed [user, tool_group, assistant] shape.
//
// Interleave key is the server wall-clock timestamp, NOT Eino's StepIdx: a
// step's tool calls fire AFTER its assistant FinishReason and BEFORE the next
// step's, so each tool aggregate (positioned by its first event's Timestamp)
// attaches to the assistant step it followed. This sidesteps the fragile
// StepIdx off-by-one (StepIdx increments between the assistant emit and the
// tools node).
//
// finalContent/finalReasoning are the reconciled final message (final answer +
// inline images, or a friendly error) — they overwrite the LAST assistant turn
// (or are appended when the run ended on a tool_group) so the durable transcript
// always ends with the authoritative answer, exactly like the collapsed shape.
func buildTranscriptTurns(
	userInput string,
	steps []stepEntry,
	events []narration.Event,
	finalContent, finalReasoning string,
) []map[string]any {
	if len(steps) == 0 {
		return nil
	}
	aggs := aggregateToolEvents(events) // first-seen (chronological) order

	turns := []map[string]any{{"role": "user", "content": userInput}}
	toolIdx := 0
	for k := range steps {
		s := steps[k]
		if s.Content != "" || s.Reasoning != "" {
			turns = append(turns, assistantTurn(s.Content, s.Reasoning))
		}
		// Drain the tool aggregates that fired before the NEXT step began — i.e.
		// the tools this step triggered. The last step has no successor, so its
		// window is open-ended (drains the remainder).
		hasNext := k+1 < len(steps)
		var nextTS time.Time
		if hasNext {
			nextTS = steps[k+1].TS
		}
		group := make([]persistedToolCall, 0)
		for toolIdx < len(aggs) {
			if hasNext && !aggFirstTS(aggs[toolIdx]).Before(nextTS) {
				break
			}
			group = append(group, aggs[toolIdx])
			toolIdx++
		}
		if len(group) > 0 {
			turns = append(turns, map[string]any{"role": "tool_group", "tool_calls": group})
		}
	}
	// Defensive: any tool aggregates not attributed to a step (shouldn't happen —
	// every tool follows some assistant step) trail as a final tool_group.
	if toolIdx < len(aggs) {
		turns = append(turns, map[string]any{"role": "tool_group", "tool_calls": aggs[toolIdx:]})
	}

	// Reconcile the final answer onto the trailing assistant turn (or append one
	// when the run ended on a tool_group, e.g. an error after tools).
	if finalContent != "" {
		if last := turns[len(turns)-1]; last["role"] == "assistant" {
			last["content"] = finalContent
			// The final answer's reasoning is authoritative — set it, and clear any
			// stale intermediate-step reasoning when there is none (else the prior
			// step's reasoning would be mis-attributed to the final answer).
			if finalReasoning != "" {
				last["reasoning"] = finalReasoning
			} else {
				delete(last, "reasoning")
			}
		} else {
			turns = append(turns, assistantTurn(finalContent, finalReasoning))
		}
	}
	return turns
}

// assistantTurn builds an assistant turn map, omitting the reasoning key when
// empty to avoid persisting spurious empty fields into agent_run.messages.
func assistantTurn(content, reasoning string) map[string]any {
	m := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		m["reasoning"] = reasoning
	}
	return m
}

// aggFirstTS returns the timestamp of an aggregate's first narration event,
// used to position the tool call chronologically against the step timeline.
func aggFirstTS(a persistedToolCall) time.Time {
	if len(a.Events) > 0 {
		return a.Events[0].Timestamp
	}
	return time.Time{}
}
