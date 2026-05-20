package memory

import (
	"time"
)

// MemoryKind enumerates the allowed memory categories.
type MemoryKind string

const (
	KindSummary    MemoryKind = "summary" // L1 only
	KindLearning   MemoryKind = "learning"
	KindDecision   MemoryKind = "decision"
	KindIssue      MemoryKind = "issue"
	KindFact       MemoryKind = "fact"
	KindPreference MemoryKind = "preference"
)

// Valid reports whether the kind is allowed for the given layer.
// layer must be "L1" or "L2".
// summary is only valid for L1; all other non-empty kinds are valid for both layers.
func (k MemoryKind) Valid(layer string) bool {
	switch k {
	case KindLearning, KindDecision, KindIssue, KindFact, KindPreference:
		return true
	case KindSummary:
		return layer == "L1" // summary is L1-only
	}
	return false
}

// SourceType enumerates who wrote the memory entry.
type SourceType string

const (
	SourceAgent        SourceType = "agent"
	SourceUserExplicit SourceType = "user_explicit"
	SourceAgentTool    SourceType = "agent_tool"
)

// MemoryItem is a unified view of either an L1 (agent_session_memory) or
// L2 (user_global_memory) row.  Fields that are not applicable to a layer
// are left at their zero values.
type MemoryItem struct {
	// Shared L1/L2 fields
	ID                      uint64
	Kind                    MemoryKind
	Content                 string // L1: content field; L2: value field (unified)
	SourceType              SourceType
	SourceAgentDefinitionID *uint64
	CreatedAt               time.Time
	UpdatedAt               time.Time

	// L1 only
	Score             float64   // BM25/vector fusion score; L2 == 0
	RecencyAt         time.Time // L2 uses UpdatedAt
	AgentDefinitionID uint64    // L1 isolation boundary; L2 == 0

	// L2 only
	KeyName    string  // Notepad key; L1 == ""
	Confidence float64 // L1 == 0
}

// Message is a single conversation turn (role + content).
type Message struct {
	Role    string
	Content string
}

// WriteOpts carries optional parameters for Notepad.Write.
type WriteOpts struct {
	SourceType              SourceType
	SourceAgentDefinitionID *uint64
	// Confidence == nil → biz layer defaults to 1.0.
	// Confidence == &0.0 is a valid low-confidence value and will be stored as-is.
	Confidence *float64
	ExpiresAt  *time.Time
}
