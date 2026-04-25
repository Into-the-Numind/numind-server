package contextbudget

import (
	"numind-server/internal/pkg/contextbudget"
)

// ----------------------------------------------------------------------------
// ContextFragment factory helpers
//
// These producers simplify Task 9 (SOP) and Task 10 (chatbot/salesrag)
// implementations by providing opinionated constructors for common fragment
// roles. Each helper sets sensible defaults for Role, Source, ContentType,
// Compressibility, and Critical so callers only need to provide business fields.
// ----------------------------------------------------------------------------

// NewImmutableSystemFragment returns a ContextFragment for a system prompt that
// must always be present in the context. It is never compressed or dropped.
//
//	id      — unique fragment ID within the planning session
//	content — system prompt text
func NewImmutableSystemFragment(id, content string) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleImmutable,
		Source:          contextbudget.SourceSystem,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      10,
		Compressibility: contextbudget.CompressNone,
		Critical:        true,
	}
}

// NewCriticalUserFragment returns a ContextFragment for a user message that
// must not be dropped (e.g. the current turn's user input).
//
//	id      — unique fragment ID
//	content — user message text
func NewCriticalUserFragment(id, content string) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleRecent,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      9,
		Compressibility: contextbudget.CompressNone,
		Critical:        true,
	}
}

// NewDurableAssistantFragment returns a ContextFragment for a durable assistant
// response that can be summarised under budget pressure.
//
//	id         — unique fragment ID
//	content    — assistant response text
//	order      — monotonically increasing sequence number (older = lower)
//	importance — caller-assigned [0, 10]; higher = more important
func NewDurableAssistantFragment(id, content string, order, importance int) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceAssistant,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      importance,
		Order:           order,
		Compressibility: contextbudget.CompressSummarize,
		Critical:        false,
	}
}

// NewDurableUserFragment returns a ContextFragment for a durable user message
// that can be summarised under budget pressure.
//
//	id         — unique fragment ID
//	content    — user message text
//	order      — monotonically increasing sequence number (older = lower)
//	importance — caller-assigned [0, 10]
func NewDurableUserFragment(id, content string, order, importance int) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      importance,
		Order:           order,
		Compressibility: contextbudget.CompressSummarize,
		Critical:        false,
	}
}

// NewEvidenceReferenceFragment returns a ContextFragment for knowledge-base
// evidence that supports reference compression (replacing with a pointer stub).
//
//	id           — unique fragment ID
//	sourceRef    — URI or content-addressable key (used as reference pointer)
//	summaryText  — the text body of the evidence fragment
//	order        — sequence number
//	importance   — caller-assigned [0, 10]
func NewEvidenceReferenceFragment(id, sourceRef, summaryText string, order, importance int) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleEvidence,
		Source:          contextbudget.SourceKB,
		ContentType:     contextbudget.ContentText,
		Content:         summaryText,
		Importance:      importance,
		Order:           order,
		Compressibility: contextbudget.CompressReference,
		SourceReference: sourceRef,
		Critical:        false,
	}
}

// NewDiscardableFragment returns a ContextFragment that may be dropped under
// budget pressure with no compression attempt (e.g. tool call metadata, debug info).
//
//	id         — unique fragment ID
//	content    — fragment text
//	order      — sequence number
//	importance — caller-assigned [0, 10]; lower = dropped first
func NewDiscardableFragment(id, content string, order, importance int) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleDiscardable,
		Source:          contextbudget.SourceInternal,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      importance,
		Order:           order,
		Compressibility: contextbudget.CompressDrop,
		Critical:        false,
	}
}

// NewSummaryFragment returns a ContextFragment for a pre-computed summary that
// replaces one or more original fragments.
//
//	id          — unique fragment ID
//	content     — summary text (should be significantly shorter than originals)
//	sourceRef   — hash/key identifying the original content (for cache lookup)
//	order       — sequence position in the conversation
//	importance  — caller-assigned [0, 10]
func NewSummaryFragment(id, content, sourceRef string, order, importance int) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceInternal,
		ContentType:     contextbudget.ContentSummary,
		Content:         content,
		Importance:      importance,
		Order:           order,
		Compressibility: contextbudget.CompressNone, // summaries are already compressed
		SourceReference: sourceRef,
		Critical:        false,
	}
}
