package memory

// FactCandidate is the canonical extraction DTO returned by the LLM (and
// consumed downstream by Task 3.4 selector and Task 3.7 dialectic).
//
// Distinct from the internal ExtractedFact (see extractor_prompt.go) which
// carries only the LLM-facing fields. FactCandidate adds source tracking
// (session_id, message_uuid) so downstream code can trace a fact back to
// its origin without re-parsing agent_run.messages.
//
// Layer A only — SubjectID is intentionally absent. V2 Layer B will add a
// separate SubjectCandidate type rather than overloading this one.
type FactCandidate struct {
	Content    string  `json:"content"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	// SourceSessionID is caller-filled from ExtractionJob.SessionID (not returned
	// by the LLM). The omitempty tag is for marshalling to logs / tests where
	// some callers may pass an empty session. The LLM response schema only emits
	// content/category/confidence/source_message_uuid.
	SourceSessionID   string `json:"source_session_id,omitempty"`
	SourceMessageUUID string `json:"source_message_uuid,omitempty"`
}
