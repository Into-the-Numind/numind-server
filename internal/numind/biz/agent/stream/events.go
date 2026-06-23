// Package stream defines the SSE event protocol for agent run streaming.
// All events emitted over the SSE connection are instances of Event, with
// type-specific payload structs encoded as JSON in the Data field.
package stream

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies the kind of a streaming event.
type EventType string

const (
	// EventStreamStart is sent immediately after SSE handshake; contains run_id / session_id.
	EventStreamStart EventType = "stream_start"

	// EventTokenDelta carries an LLM text increment (highest-frequency event).
	EventTokenDelta EventType = "token_delta"

	// EventReasoningDelta carries an internal reasoning increment from thinking models.
	EventReasoningDelta EventType = "reasoning_delta"

	// EventAssistantMessage is emitted at the end of each ReAct step with the
	// complete assistant turn.
	EventAssistantMessage EventType = "assistant_message"

	// EventToolCallStart is emitted when a tool invocation begins.
	EventToolCallStart EventType = "tool_call_start"

	// EventToolCallProgress carries narration progress from an in-flight tool call.
	EventToolCallProgress EventType = "tool_call_progress"

	// EventToolCallArgsDelta carries an incremental fragment of an in-flight
	// tool call's function arguments (i.e. the "code/content being written").
	// Emitted ONLY for allowlisted code/content generating tools (see the
	// runner's isCodeStreamingTool). Lets the frontend render a live, collapsible
	// "writing code" box. Purely observational — never affects execution.
	EventToolCallArgsDelta EventType = "tool_call_args_delta"

	// EventToolCallResult is emitted when a tool call completes successfully.
	EventToolCallResult EventType = "tool_call_result"

	// EventToolCallError is emitted when a tool call fails.
	EventToolCallError EventType = "tool_call_error"

	// EventStepDone marks the end of one ReAct iteration.
	EventStepDone EventType = "step_done"

	// EventStateChange reports a state-machine transition (LoopEvent).
	EventStateChange EventType = "state_change"

	// EventQuestionPrompt is emitted when the agent yields an ask_user_question.
	EventQuestionPrompt EventType = "question_prompt"

	// EventTerminal signals the end of the stream (contains TerminalReason).
	EventTerminal EventType = "terminal"

	// EventError signals a fatal mid-stream error.
	EventError EventType = "error"

	// EventPing is a 25-second keepalive comment.
	EventPing EventType = "ping"
)

// Event is the unified SSE envelope. Each event is JSON-marshalled and sent as
// a single SSE data frame: "data: <json>\n\n".
type Event struct {
	// Type identifies the event kind.
	Type EventType `json:"type"`

	// Seq is a monotonically increasing sequence number (reserved for
	// gap-detection on reconnect; not yet used for replay).
	Seq uint64 `json:"seq"`

	// Timestamp is the server-side wall clock when the event was created.
	Timestamp time.Time `json:"ts"`

	// RunID is the agent_run primary key.
	RunID uint64 `json:"run_id"`

	// StepIndex is the current ReAct iteration index (0-based).
	StepIndex int `json:"step,omitempty"`

	// Data holds the type-specific payload, encoded as JSON.
	Data json.RawMessage `json:"data,omitempty"`
}

// Encode assembles an Event from a payload struct. The payload is
// JSON-marshalled into Event.Data. A nil payload produces an Event with nil
// Data (no panic). The returned Event uses time.Now() as its Timestamp.
func Encode(t EventType, payload any, seq uint64, runID uint64, step int) (Event, error) {
	ev := Event{
		Type:      t,
		Seq:       seq,
		Timestamp: time.Now(),
		RunID:     runID,
		StepIndex: step,
	}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("stream.Encode(%s): marshal payload: %w", t, err)
		}
		ev.Data = json.RawMessage(b)
	}
	return ev, nil
}

// ---------------------------------------------------------------------------
// Payload structs — one per EventType that carries data.
// Field names are snake_case to match the JSON wire format exactly.
// ---------------------------------------------------------------------------

// TokenDeltaPayload carries a single LLM text increment.
type TokenDeltaPayload struct {
	// MessageID is the assistant message UUID that this delta belongs to.
	// Multiple deltas share a MessageID within one ReAct step.
	MessageID string `json:"message_id"`

	// Text is the incremental text fragment.
	Text string `json:"text"`
}

// ReasoningDeltaPayload carries an internal reasoning increment from a
// thinking-mode model.
type ReasoningDeltaPayload struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// AssistantMessagePayload is emitted once per ReAct step when the assistant
// message is complete (i.e. FinishReason is set).
type AssistantMessagePayload struct {
	MessageID        string `json:"message_id"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	HasToolCalls     bool   `json:"has_tool_calls"`
}

// ToolCallStartPayload is emitted when a tool invocation begins.
type ToolCallStartPayload struct {
	ToolCallID   string         `json:"tool_call_id"`
	ToolName     string         `json:"tool_name"`
	InputDigest  string         `json:"input_digest"`
	InputPreview map[string]any `json:"input_preview,omitempty"`
}

// ToolCallProgressPayload carries narration from an in-flight tool call.
type ToolCallProgressPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Message    string `json:"message"`
	Verb       string `json:"verb,omitempty"`
}

// ToolCallArgsDeltaPayload carries an incremental fragment of an in-flight
// tool call's function arguments. Concatenating all ArgsDelta values for a
// given ToolCallID reconstructs the full arguments JSON the model is writing.
// Emitted only for allowlisted code/content tools.
type ToolCallArgsDeltaPayload struct {
	ToolCallID   string `json:"tool_call_id"`
	FunctionName string `json:"function_name"`
	ArgsDelta    string `json:"args_delta"`
}

// ToolCallResultPayload is emitted when a tool call completes successfully.
// For file-producing tools (image_gen / create_*), the artifact_* fields carry
// the generated file so the frontend can render it (e.g. an inline <img> for
// images) instead of only showing a "图片已生成" text line.
type ToolCallResultPayload struct {
	ToolCallID       string `json:"tool_call_id"`
	Preview          string `json:"preview"`
	ArtifactURL      string `json:"artifact_url,omitempty"`
	ArtifactFilename string `json:"artifact_filename,omitempty"`
	ArtifactMime     string `json:"artifact_mime,omitempty"`
	DurationMs       int64  `json:"duration_ms"`
}

// ToolCallErrorPayload is emitted when a tool call fails.
type ToolCallErrorPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
}

// StepDonePayload marks the end of one ReAct iteration.
type StepDonePayload struct {
	StepIndex  int    `json:"step_index"`
	StopReason string `json:"stop_reason,omitempty"`
}

// StateChangePayload reports a state-machine LoopEvent transition.
type StateChangePayload struct {
	LoopEvent     string `json:"loop_event"`
	PreviousState string `json:"previous_state,omitempty"`
}

// QuestionOption is a single ask_user_question choice as sent to the client.
// Mirrors the frontend QuestionPromptOption ({label, description}). The backend
// YieldOption also carries a machine `key`, but the client identifies options by
// label (matching the narration/polling path), so key is intentionally not
// forwarded over SSE.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionPromptItem is one question in a QuestionPromptPayload.
//
// Options must ALWAYS serialize as a JSON array — present even when empty,
// never null and never omitted. The frontend reads options.length unguarded;
// an omitted key crashed the whole question card (dev run 147, 2026-06-12).
// Emitters build the slice with make(...) so it is non-nil and marshals to [].
type QuestionPromptItem struct {
	Question    string           `json:"question"`
	Options     []QuestionOption `json:"options"`
	Header      string           `json:"header,omitempty"`
	MultiSelect bool             `json:"multi_select"`
}

// QuestionPromptPayload is emitted when the agent yields an ask_user_question.
//
// agent-multi-question: carries 1-4 independent questions (Claude Code's
// AskUserQuestion model). The frontend renders a tabbed navigator over them.
//
// feishu-integration: PauseType/AuthURL mirror YieldPayload so the streaming
// frontend can render an authorization card on an auth pause. Without these the
// SSE path would never carry pause_type (only the non-stream
// pending_question_json would), so the frontend (T13) could not distinguish an
// auth pause from a question pause on a live stream. Both are omitempty so an
// ordinary question prompt serializes identically to the pre-feishu shape.
type QuestionPromptPayload struct {
	Questions []QuestionPromptItem `json:"questions"`

	// PauseType is "question" (default/empty) or "auth".
	PauseType string `json:"pause_type,omitempty"`

	// AuthURL is the third-party authorization URL, set only when PauseType=auth.
	AuthURL string `json:"auth_url,omitempty"`
}

// TerminalPayload signals the end of the stream and carries run summary data.
type TerminalPayload struct {
	Reason     string `json:"reason"`
	DurationMs int64  `json:"duration_ms"`
	StepCount  int    `json:"step_count"`
	// UserMessage is a friendly Chinese message derived from Reason for error
	// terminals (empty for successful / waiting-for-user terminals). The frontend
	// shows this instead of mapping the machine-code Reason itself.
	UserMessage      string         `json:"user_message,omitempty"`
	FinalOutput      string         `json:"final_output,omitempty"`
	TerminalMetadata map[string]any `json:"terminal_metadata,omitempty"`
	PermissionDenial map[string]any `json:"permission_denial,omitempty"`
}

// ErrorPayload carries details of a fatal mid-stream error.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
