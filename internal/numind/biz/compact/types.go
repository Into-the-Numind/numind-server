// Package compact implements the agent-mode #9 conversation compaction system
// (blueprint §4.8) — PTL recovery chains, max_output_tokens escalation, and
// session restore. Used by AgentRunner (#14 real ReAct loop will wire helpers).
//
// All exported types live here. Algorithms split across threshold.go (qwen-plus
// defaults), prompt.go (compact prompt templates), provider.go (CompactProvider
// + MockCompactProvider + token estimation), ptl_chain.go (CollapseDrain +
// ReactiveCompact + headDropRetry), max_output_chain.go (EscalateMaxTokens),
// restore.go (Restore + cleanseMessages), and attachments.go (AttachmentReinjector).
package compact

import (
	"encoding/json"
	"time"
)

// Message is the compact-system's message abstraction, decoupled from Eino's
// schema.Message. Callers (#14 ReAct loop / #11 student endpoint) convert via
// toCompactMessages / fromCompactMessages adapters.
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	// Meta flags used by CollapseDrain / headDropRetry to decide which messages
	// are protected from dropping (blueprint §4.1.6 Step 1 + §4.8.5 head-drop).
	HasFileRef    bool `json:"has_file_ref,omitempty"`
	IsCompactMark bool `json:"is_compact_mark,omitempty"`
}

// CompactRequest is the CompactProvider.Compact input.
type CompactRequest struct {
	Messages        []Message
	SystemPrompt    string // = FullCompactSystemPrompt() — preamble + base template
	MaxOutputTokens int
}

// CompactResult is the CompactProvider.Compact output.
type CompactResult struct {
	Summary      string
	InputTokens  int
	OutputTokens int
}

// RestoredSession is the Restore output.
type RestoredSession struct {
	Messages         []Message
	SystemNarration  string // injected resumption narration (§4.8.6 step 3)
	FirstTurnNoTools bool   // §4.8.6 step 5 — caller must enforce tool-disable on first turn
}

// CompactStateV1 is the GORM-side agent_run.compact_state JSON payload.
// All fields omitempty + nullable — v2 additions don't break old rows
// (blueprint §4.8.6 storage shape). LastCompactAt is *time.Time so the zero
// value omits cleanly (encoding/json treats time.Time zero as non-empty).
type CompactStateV1 struct {
	LastCompactAt         *time.Time `json:"last_compact_at,omitempty"`
	LastBoundaryMessageID string     `json:"last_boundary_message_id,omitempty"`
	TotalCompactAttempts  int        `json:"total_compact_attempts,omitempty"`
	ConsecutiveFailures   int        `json:"consecutive_failures,omitempty"`
	SummaryTokenCount     int        `json:"summary_token_count,omitempty"`
	StrategyUsed          string     `json:"strategy_used,omitempty"` // "reactive_compact" / "session_memory" (v2)
}
