package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// TestSynthesizeQuestionPrompt_EmptyOptionsSerializeAsArray reproduces the dev
// run 147 question-card blank-out (2026-06-12) on the session-reload path: the
// snapshot DTO dropped `options` for a zero-option question (omitempty), the
// frontend crashed on `options.length`, and the reloaded session showed an
// empty bubble instead of the question card.
//
// Contract under test: every question in a synthesized question_prompt
// message serializes "options" as a JSON array — present even when empty,
// never null — including questions whose stored row lacks the key entirely.
// Permanent regression protection (NDF Rule 11).
func TestSynthesizeQuestionPrompt_EmptyOptionsSerializeAsArray(t *testing.T) {
	pending := `{"questions":[
		{"question":"创始人的创业经历和背景是什么？","header":"创始人故事","options":[],"multi_select":false},
		{"question":"陪跑模式是怎样的？","multi_select":false}
	]}`
	run := &model.AgentRun{
		ID:                  147,
		StateReason:         string(TerminalWaitingForUserChoice),
		PendingQuestionJSON: datatypes.JSON(pending),
	}

	msg, ok := synthesizeQuestionPrompt(run)
	if !ok {
		t.Fatal("synthesizeQuestionPrompt returned ok=false for a waiting run with valid pending JSON")
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, `"options":null`) {
		t.Fatalf("options must never serialize as null, got: %s", s)
	}
	if got := strings.Count(s, `"options":[]`); got != 2 {
		t.Fatalf("both zero-option questions must carry \"options\":[] (got %d occurrence(s)): %s", got, s)
	}
}

// TestSynthesizeQuestionPrompt_TimestampPrefersPendingQuestionAt covers the
// timestamp source branch: when PendingQuestionAt is set it wins over
// StartedAt (review P2 — the zero-value StartedAt in the test above exercises
// only the fallback branch).
func TestSynthesizeQuestionPrompt_TimestampPrefersPendingQuestionAt(t *testing.T) {
	askedAt := time.Date(2026, 6, 12, 7, 48, 43, 0, time.UTC)
	run := &model.AgentRun{
		ID:                  147,
		StateReason:         string(TerminalWaitingForUserChoice),
		PendingQuestionJSON: datatypes.JSON(`{"questions":[{"question":"q","options":[],"multi_select":false}]}`),
		StartedAt:           time.Date(2026, 6, 12, 7, 47, 0, 0, time.UTC),
		PendingQuestionAt:   &askedAt,
	}
	msg, ok := synthesizeQuestionPrompt(run)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if msg.Timestamp != "2026-06-12T07:48:43Z" {
		t.Fatalf("timestamp must come from PendingQuestionAt, got %s", msg.Timestamp)
	}
}
