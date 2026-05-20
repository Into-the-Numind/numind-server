package narration

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIconForState_AllValues(t *testing.T) {
	cases := map[State]string{
		StateQueued:   "⋯",
		StateUse:      "⋯",
		StateProgress: "⋯",
		StateResult:   "✓",
		StateError:    "⚠️",
		StateRejected: "✕",
	}
	for s, want := range cases {
		if got := iconForState(s); got != want {
			t.Errorf("State %q: want icon %q, got %q", s, want, got)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	cases := map[State]bool{
		StateQueued:   false,
		StateUse:      false,
		StateProgress: false,
		StateResult:   true,
		StateError:    true,
		StateRejected: true,
	}
	for s, want := range cases {
		if got := s.IsTerminal(); got != want {
			t.Errorf("State %q.IsTerminal(): want %v, got %v", s, want, got)
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
	if got.RunID != ev.RunID || got.ToolName != ev.ToolName || got.Message != ev.Message {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestEvent_OmitEmptyFields(t *testing.T) {
	ev := Event{RunID: 1, ToolName: "x", State: StateUse, Message: "msg"}
	raw, _ := json.Marshal(ev)
	s := string(raw)
	// optional fields should be omitted when zero
	for _, omit := range []string{`"verb"`, `"detail"`, `"icon"`, `"reason"`} {
		if contains(s, omit) {
			t.Errorf("expected %s to be omitted (omitempty), got: %s", omit, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
