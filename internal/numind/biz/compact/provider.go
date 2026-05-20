package compact

import "context"

// CompactProvider is the LLM compaction abstraction.
// v1 ships MockCompactProvider returning a fixed placeholder; #14 ReAct loop
// will wire a real provider backed by aiservice.Chat.
type CompactProvider interface {
	Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error)
}

// MockCompactProvider is the v1 placeholder implementation. It returns
// PlaceholderSummary and (optionally) replays FailureSequence to simulate
// transient provider errors for tests.
type MockCompactProvider struct {
	PlaceholderSummary string
	// FailureSequence[i] != nil → Compact's (i+1)-th call returns that error.
	// Calls past the end of FailureSequence succeed.
	FailureSequence []error

	callCount int
}

// Compact returns either a fixed summary or, when FailureSequence dictates, an error.
// Per-instance state means tests should not share a MockCompactProvider across cases.
func (m *MockCompactProvider) Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error) {
	idx := m.callCount
	m.callCount++
	if idx < len(m.FailureSequence) && m.FailureSequence[idx] != nil {
		return nil, m.FailureSequence[idx]
	}
	return &CompactResult{
		Summary:      m.PlaceholderSummary,
		InputTokens:  EstimateTokens(joinMessages(req.Messages)),
		OutputTokens: EstimateTokens(m.PlaceholderSummary),
	}, nil
}

// EstimateTokens approximates qwen-plus token count without calling a real
// tokenizer. ASCII (≤0x7F) characters count as 0.25 tokens; all other
// characters (CJK, hiragana, katakana, hangul, CJK Ext-A/B, …) count as 1.5
// tokens. Per the spec audit this overcounts mixed-Latin and undercounts
// dense English but stays within ±15% of qwen-plus on realistic dialogue
// fixtures — accurate enough to drive AutoCompactThreshold decisions.
func EstimateTokens(text string) int {
	multi := 0
	ascii := 0
	for _, r := range text {
		if r <= 0x7F {
			ascii++
		} else {
			multi++
		}
	}
	return int(float64(multi)*1.5 + float64(ascii)*0.25)
}

// joinMessages concatenates message contents for token estimation.
// Only used internally — exposed Messages slice retains structure.
func joinMessages(msgs []Message) string {
	var s string
	for _, m := range msgs {
		s += m.Content + "\n"
	}
	return s
}
