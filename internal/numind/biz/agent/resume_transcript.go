package agent

import "encoding/json"

// mergeResumeTranscript prepends a resumed run's pre-yield transcript to the
// current leg's turns (answer-resume-lifecycle F2).
//
// prior is the agent_run.messages value captured at the ExistingRunID takeover:
// the first leg's turns plus the answer user message that AnswerAndClear
// appended. The resumed runner rebuilds its persistence from the current leg
// only, so writing it verbatim would CLOBBER the first leg (dev run 148 lost
// its original prompt and pre-question research). When the current leg's first
// turn is the same user message prior already ends with (the resume Input),
// it is dropped to avoid a duplicate.
//
// Empty / "[]" / "null" / unparseable prior returns turns unchanged, so
// non-resume runs are a strict no-op (spec invariant I2).
func mergeResumeTranscript(prior json.RawMessage, turns []map[string]any) []map[string]any {
	if len(prior) == 0 || string(prior) == "[]" || string(prior) == "null" {
		return turns
	}
	var priorTurns []map[string]any
	if err := json.Unmarshal(prior, &priorTurns); err != nil || len(priorTurns) == 0 {
		return turns
	}

	rest := turns
	if len(turns) > 0 {
		first, last := turns[0], priorTurns[len(priorTurns)-1]
		// content may be an OAI-style []any for multimodal turns; comparing
		// incomparable interface values with == panics. Dedup only applies to
		// the plain-string answer message AnswerAndClear appends, so a string
		// type-assert is both safe and sufficient (review P2).
		firstContent, fOK := first["content"].(string)
		lastContent, lOK := last["content"].(string)
		if fOK && lOK && first["role"] == "user" && last["role"] == "user" && firstContent == lastContent {
			rest = turns[1:]
		}
	}
	merged := make([]map[string]any, 0, len(priorTurns)+len(rest))
	merged = append(merged, priorTurns...)
	merged = append(merged, rest...)
	return merged
}
