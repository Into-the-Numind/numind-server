package agent

import (
	"encoding/json"
	"strings"
	"testing"

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
