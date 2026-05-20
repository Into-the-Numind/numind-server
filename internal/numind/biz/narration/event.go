// Package narration implements the learner-facing narration layer for Agent
// Runtime (blueprint §4.7). See proposals/agent-mode-narration-layer-proposal.md
// for architecture and ADRs.
package narration

import (
	"encoding/json"
	"time"
)

// State is the lifecycle phase of a tool call as visible to the learner.
// Blueprint §4.7.4 defines 6 values; v1 emitter actively fires use/result/error/rejected.
// queued is reserved for #14 (ReAct loop reification); progress for #13 (sandbox push).
type State string

const (
	StateQueued   State = "queued"
	StateUse      State = "use"
	StateProgress State = "progress"
	StateResult   State = "result"
	StateError    State = "error"
	StateRejected State = "rejected"
)

// IsTerminal returns true if the state is a terminal-emit for a tool call.
func (s State) IsTerminal() bool {
	return s == StateResult || s == StateError || s == StateRejected
}

// Event is the wire-format struct surfaced via Provider.Subscribe(runID).
// JSON tags target the #11 student-ux SSE consumer.
type Event struct {
	RunID      uint64    `json:"run_id"`
	ToolCallID string    `json:"tool_call_id"`
	ToolName   string    `json:"tool_name"`
	State      State     `json:"state"`
	Verb       string    `json:"verb,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Icon       string    `json:"icon,omitempty"`
	Message    string    `json:"message"`
	Reason     string    `json:"reason,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// EmitPayload is what callers (adapter) pass to Provider.Emit.
// Provider fills in computed fields (Icon, Message, Timestamp, ToolCallID).
type EmitPayload struct {
	RunID           uint64
	ToolCallID      string
	Input           json.RawMessage
	Result          json.RawMessage
	Err             error
	Reason          string // for StateRejected; v1 always "" (S1-D21)
	OverrideMessage string // reserved for #14 LLM-supplied narration (S1-D17); v1 always ""
}

// iconForState is the canonical State→icon mapping (blueprint §4.7.6).
func iconForState(s State) string {
	switch s {
	case StateResult:
		return "✓"
	case StateError:
		return "⚠️"
	case StateRejected:
		return "✕"
	default: // queued, use, progress, and any future
		return "⋯"
	}
}
