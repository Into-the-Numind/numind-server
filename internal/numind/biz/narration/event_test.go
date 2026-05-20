package narration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIconForState_AllValues(t *testing.T) {
	// Slice (not map) for deterministic iteration in failure messages.
	cases := []struct {
		state State
		want  string
	}{
		{StateQueued, "⋯"},
		{StateUse, "⋯"},
		{StateProgress, "⋯"},
		{StateResult, "✓"},
		{StateError, "⚠️"},
		{StateRejected, "✕"},
	}
	for _, c := range cases {
		if got := iconForState(c.state); got != c.want {
			t.Errorf("State %q: want icon %q, got %q", c.state, c.want, got)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	cases := []struct {
		state State
		want  bool
	}{
		{StateQueued, false},
		{StateUse, false},
		{StateProgress, false},
		{StateResult, true},
		{StateError, true},
		{StateRejected, true},
	}
	for _, c := range cases {
		if got := c.state.IsTerminal(); got != c.want {
			t.Errorf("State %q.IsTerminal(): want %v, got %v", c.state, c.want, got)
		}
	}
}

func TestEvent_JSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	ev := Event{
		RunID:      42,
		ToolCallID: "42-1",
		ToolName:   "bash_exec",
		State:      StateResult,
		Verb:       "正在执行",
		Detail:     "命令",
		Icon:       "✓",
		Message:    "命令执行完成",
		Reason:     "",
		Timestamp:  now,
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Field-by-field round-trip check (P2 fix: catch timezone/State drift).
	if got.RunID != ev.RunID {
		t.Errorf("RunID: want %d, got %d", ev.RunID, got.RunID)
	}
	if got.ToolCallID != ev.ToolCallID {
		t.Errorf("ToolCallID: want %q, got %q", ev.ToolCallID, got.ToolCallID)
	}
	if got.ToolName != ev.ToolName {
		t.Errorf("ToolName: want %q, got %q", ev.ToolName, got.ToolName)
	}
	if got.State != ev.State {
		t.Errorf("State: want %q, got %q", ev.State, got.State)
	}
	if got.Verb != ev.Verb || got.Detail != ev.Detail || got.Icon != ev.Icon {
		t.Errorf("Verb/Detail/Icon mismatch: got %q/%q/%q", got.Verb, got.Detail, got.Icon)
	}
	if got.Message != ev.Message {
		t.Errorf("Message: want %q, got %q", ev.Message, got.Message)
	}
	if !got.Timestamp.Equal(ev.Timestamp) {
		t.Errorf("Timestamp: want %v, got %v", ev.Timestamp, got.Timestamp)
	}
}

func TestEvent_OmitEmptyFields(t *testing.T) {
	ev := Event{RunID: 1, ToolName: "x", State: StateUse, Message: "msg"}
	raw, _ := json.Marshal(ev)
	s := string(raw)
	for _, omit := range []string{`"verb"`, `"detail"`, `"icon"`, `"reason"`} {
		if strings.Contains(s, omit) {
			t.Errorf("expected %s to be omitted (omitempty), got: %s", omit, s)
		}
	}
}
