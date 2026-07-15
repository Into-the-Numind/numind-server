package stream

import (
	"encoding/json"
	"testing"
	"time"
)

// roundTrip encodes payload into an Event, marshals the Event to JSON,
// then unmarshals it back. It returns the reconstituted Event.
func roundTrip(t *testing.T, et EventType, payload any) Event {
	t.Helper()
	ev, err := Encode(et, payload, 1, 42, 0)
	if err != nil {
		t.Fatalf("Encode(%v): %v", et, err)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return got
}

// assertEventFields checks the envelope fields that are common to every event.
func assertEventFields(t *testing.T, got Event, wantType EventType, wantSeq uint64, wantRunID uint64) {
	t.Helper()
	if got.Type != wantType {
		t.Errorf("Type: got %q, want %q", got.Type, wantType)
	}
	if got.Seq != wantSeq {
		t.Errorf("Seq: got %d, want %d", got.Seq, wantSeq)
	}
	if got.RunID != wantRunID {
		t.Errorf("RunID: got %d, want %d", got.RunID, wantRunID)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

// TestEncode_NilPayload verifies that Encode with a nil payload returns an Event
// with nil Data and does not panic.
func TestEncode_NilPayload(t *testing.T) {
	ev, err := Encode(EventPing, nil, 5, 99, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Data != nil {
		t.Errorf("expected nil Data for nil payload, got %s", ev.Data)
	}
	if ev.Type != EventPing {
		t.Errorf("Type: got %q, want %q", ev.Type, EventPing)
	}
}

// TestEncode_Timestamp verifies that Encode sets a non-zero Timestamp close to now.
func TestEncode_Timestamp(t *testing.T) {
	before := time.Now()
	ev, _ := Encode(EventPing, nil, 1, 1, 0)
	after := time.Now()
	if ev.Timestamp.Before(before) || ev.Timestamp.After(after) {
		t.Errorf("Timestamp %v not in [%v, %v]", ev.Timestamp, before, after)
	}
}

// TestRoundTrip_TokenDelta verifies TokenDeltaPayload round-trip.
func TestRoundTrip_TokenDelta(t *testing.T) {
	p := TokenDeltaPayload{MessageID: "msg-1", Text: " hello"}
	got := roundTrip(t, EventTokenDelta, p)
	assertEventFields(t, got, EventTokenDelta, 1, 42)

	var decoded TokenDeltaPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.MessageID != p.MessageID {
		t.Errorf("MessageID: got %q, want %q", decoded.MessageID, p.MessageID)
	}
	if decoded.Text != p.Text {
		t.Errorf("Text: got %q, want %q", decoded.Text, p.Text)
	}
}

// TestTokenDeltaPayload_JSONFieldNames checks that the JSON wire names are exactly
// as specified ("message_id", "text"). This is a field-name regression test.
func TestTokenDeltaPayload_JSONFieldNames(t *testing.T) {
	p := TokenDeltaPayload{MessageID: "", Text: "x"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"message_id":"","text":"x"}`
	if string(b) != want {
		t.Errorf("JSON: got %s, want %s", b, want)
	}
}

// TestRoundTrip_ReasoningDelta verifies ReasoningDeltaPayload round-trip.
func TestRoundTrip_ReasoningDelta(t *testing.T) {
	p := ReasoningDeltaPayload{MessageID: "msg-2", Text: " thinking..."}
	got := roundTrip(t, EventReasoningDelta, p)
	assertEventFields(t, got, EventReasoningDelta, 1, 42)

	var decoded ReasoningDeltaPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Text != p.Text {
		t.Errorf("Text: got %q, want %q", decoded.Text, p.Text)
	}
}

// TestRoundTrip_AssistantMessage verifies AssistantMessagePayload round-trip.
func TestRoundTrip_AssistantMessage(t *testing.T) {
	p := AssistantMessagePayload{
		MessageID:        "msg-3",
		Content:          "Full response",
		ReasoningContent: "Some reasoning",
		HasToolCalls:     true,
	}
	got := roundTrip(t, EventAssistantMessage, p)
	assertEventFields(t, got, EventAssistantMessage, 1, 42)

	var decoded AssistantMessagePayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.HasToolCalls != p.HasToolCalls {
		t.Errorf("HasToolCalls: got %v, want %v", decoded.HasToolCalls, p.HasToolCalls)
	}
	if decoded.ReasoningContent != p.ReasoningContent {
		t.Errorf("ReasoningContent: got %q, want %q", decoded.ReasoningContent, p.ReasoningContent)
	}
}

// TestRoundTrip_ToolCallStart verifies ToolCallStartPayload round-trip.
func TestRoundTrip_ToolCallStart(t *testing.T) {
	p := ToolCallStartPayload{
		ToolCallID:   "tc-1",
		ToolName:     "web_search",
		InputDigest:  "abc123",
		InputPreview: map[string]any{"query": "test"},
	}
	got := roundTrip(t, EventToolCallStart, p)
	assertEventFields(t, got, EventToolCallStart, 1, 42)

	var decoded ToolCallStartPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.ToolName != p.ToolName {
		t.Errorf("ToolName: got %q, want %q", decoded.ToolName, p.ToolName)
	}
	if decoded.InputDigest != p.InputDigest {
		t.Errorf("InputDigest: got %q, want %q", decoded.InputDigest, p.InputDigest)
	}
}

// TestRoundTrip_ToolCallProgress verifies ToolCallProgressPayload round-trip.
func TestRoundTrip_ToolCallProgress(t *testing.T) {
	p := ToolCallProgressPayload{ToolCallID: "tc-1", Message: "Searching...", Verb: "search"}
	got := roundTrip(t, EventToolCallProgress, p)
	assertEventFields(t, got, EventToolCallProgress, 1, 42)

	var decoded ToolCallProgressPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Message != p.Message {
		t.Errorf("Message: got %q, want %q", decoded.Message, p.Message)
	}
}

// TestRoundTrip_ToolCallResult verifies ToolCallResultPayload round-trip.
func TestRoundTrip_ToolCallResult(t *testing.T) {
	p := ToolCallResultPayload{ToolCallID: "tc-1", Preview: "Result text", DurationMs: 350}
	got := roundTrip(t, EventToolCallResult, p)
	assertEventFields(t, got, EventToolCallResult, 1, 42)

	var decoded ToolCallResultPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.DurationMs != p.DurationMs {
		t.Errorf("DurationMs: got %d, want %d", decoded.DurationMs, p.DurationMs)
	}
}

// TestRoundTrip_ToolCallError verifies ToolCallErrorPayload round-trip.
func TestRoundTrip_ToolCallError(t *testing.T) {
	p := ToolCallErrorPayload{ToolCallID: "tc-2", Error: "timeout", DurationMs: 5000}
	got := roundTrip(t, EventToolCallError, p)
	assertEventFields(t, got, EventToolCallError, 1, 42)

	var decoded ToolCallErrorPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Error != p.Error {
		t.Errorf("Error: got %q, want %q", decoded.Error, p.Error)
	}
}

// TestRoundTrip_StepDone verifies StepDonePayload round-trip.
func TestRoundTrip_StepDone(t *testing.T) {
	p := StepDonePayload{StepIndex: 2, StopReason: "stop"}
	got := roundTrip(t, EventStepDone, p)
	assertEventFields(t, got, EventStepDone, 1, 42)

	var decoded StepDonePayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.StepIndex != p.StepIndex {
		t.Errorf("StepIndex: got %d, want %d", decoded.StepIndex, p.StepIndex)
	}
}

// TestRoundTrip_StateChange verifies StateChangePayload round-trip.
func TestRoundTrip_StateChange(t *testing.T) {
	p := StateChangePayload{LoopEvent: "LLMGenerated", PreviousState: "Running"}
	got := roundTrip(t, EventStateChange, p)
	assertEventFields(t, got, EventStateChange, 1, 42)

	var decoded StateChangePayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.LoopEvent != p.LoopEvent {
		t.Errorf("LoopEvent: got %q, want %q", decoded.LoopEvent, p.LoopEvent)
	}
}

// TestRoundTrip_QuestionPrompt verifies QuestionPromptPayload round-trip.
func TestRoundTrip_QuestionPrompt(t *testing.T) {
	p := QuestionPromptPayload{Questions: []QuestionPromptItem{{
		Question:    "Which format?",
		Options:     []QuestionOption{{Label: "PDF"}, {Label: "CSV", Description: "comma-separated"}},
		Header:      "Please choose",
		MultiSelect: false,
	}}}
	got := roundTrip(t, EventQuestionPrompt, p)
	assertEventFields(t, got, EventQuestionPrompt, 1, 42)

	var decoded QuestionPromptPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(decoded.Questions) != 1 {
		t.Fatalf("Questions len: got %d, want 1", len(decoded.Questions))
	}
	if len(decoded.Questions[0].Options) != 2 {
		t.Errorf("Options len: got %d, want 2", len(decoded.Questions[0].Options))
	}
}

// TestRoundTrip_Terminal verifies TerminalPayload round-trip.
func TestRoundTrip_Terminal(t *testing.T) {
	p := TerminalPayload{
		Reason:      "completed",
		DurationMs:  12345,
		StepCount:   3,
		FinalOutput: "Done!",
	}
	got := roundTrip(t, EventTerminal, p)
	assertEventFields(t, got, EventTerminal, 1, 42)

	var decoded TerminalPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Reason != p.Reason {
		t.Errorf("Reason: got %q, want %q", decoded.Reason, p.Reason)
	}
	if decoded.DurationMs != p.DurationMs {
		t.Errorf("DurationMs: got %d, want %d", decoded.DurationMs, p.DurationMs)
	}
}

// TestRoundTrip_Error verifies ErrorPayload round-trip.
func TestRoundTrip_Error(t *testing.T) {
	p := ErrorPayload{Code: "model_error", Message: "LLM provider timeout"}
	got := roundTrip(t, EventError, p)
	assertEventFields(t, got, EventError, 1, 42)

	var decoded ErrorPayload
	if err := json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Code != p.Code {
		t.Errorf("Code: got %q, want %q", decoded.Code, p.Code)
	}
}

// TestRoundTrip_StreamStart verifies that stream_start with a nil payload is safe.
func TestRoundTrip_StreamStart(t *testing.T) {
	got := roundTrip(t, EventStreamStart, nil)
	assertEventFields(t, got, EventStreamStart, 1, 42)
	// nil payload → Data should be nil or absent (both are acceptable).
	// We just verify no panic occurred and the type is correct — covered above.
}

// TestRoundTrip_Ping verifies that ping with nil payload is safe.
func TestRoundTrip_Ping(t *testing.T) {
	got := roundTrip(t, EventPing, nil)
	assertEventFields(t, got, EventPing, 1, 42)
}

// TestAllEventTypesHaveConstants verifies the 16 declared EventType constants.
func TestAllEventTypesHaveConstants(t *testing.T) {
	all := []EventType{
		EventStreamStart, EventTokenDelta, EventReasoningDelta, EventAssistantMessage,
		EventToolCallStart, EventToolCallProgress, EventToolCallArgsDelta, EventToolCallResult, EventToolCallError,
		EventStepDone, EventStateChange, EventQuestionPrompt, EventExternalAction, EventTerminal, EventError, EventPing,
	}
	if len(all) != 16 {
		t.Errorf("expected 16 EventType constants, got %d", len(all))
	}
	seen := map[EventType]bool{}
	for _, et := range all {
		if seen[et] {
			t.Errorf("duplicate EventType: %q", et)
		}
		seen[et] = true
		if et == "" {
			t.Error("empty EventType string")
		}
	}
}

// TestStepIndex verifies that StepIndex is preserved through Encode.
func TestStepIndex(t *testing.T) {
	ev, err := Encode(EventTokenDelta, TokenDeltaPayload{Text: "x"}, 7, 10, 5)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if ev.StepIndex != 5 {
		t.Errorf("StepIndex: got %d, want 5", ev.StepIndex)
	}
}
