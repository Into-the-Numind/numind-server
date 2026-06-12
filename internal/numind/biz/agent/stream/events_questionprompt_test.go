package stream

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestQuestionPromptItem_EmptyOptionsSerializeAsArray reproduces the dev run
// 147 question-card blank-out (2026-06-12): a question with zero suggested
// options (a legitimate open question, schema minItems=0) was serialized with
// the `options` key omitted entirely. The frontend QuestionPrompt.vue reads
// `options.length` and crashed with "Cannot read properties of undefined",
// blanking the whole card both live and on reload — the run looked stuck.
//
// Contract under test: the SSE question_prompt payload always carries
// "options" as a JSON array ("options":[]), never omits it and never emits
// null. Permanent regression protection (NDF Rule 11).
func TestQuestionPromptItem_EmptyOptionsSerializeAsArray(t *testing.T) {
	payload := QuestionPromptPayload{Questions: []QuestionPromptItem{
		{Question: "创始人的创业经历和背景是什么？", Header: "创始人故事", Options: []QuestionOption{}},
		{Question: "陪跑模式是怎样的？", Options: []QuestionOption{{Label: "90天"}}},
	}}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, `"options":null`) {
		t.Fatalf("options must never serialize as null, got: %s", s)
	}
	if !strings.Contains(s, `"options":[]`) {
		t.Fatalf("a zero-option question must still carry \"options\":[] (frontend reads options.length), got: %s", s)
	}
}
