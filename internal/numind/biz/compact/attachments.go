package compact

import "context"

// AttachmentReinjector is the post-compact context restoration hook. After a
// compact discards old messages, the LLM loses memory of file uploads, Skill
// instructions, and MCP tool descriptions. The reinjector appends a delta to
// the system prompt so the LLM retains relevant context (blueprint §4.8.7).
//
// v1 ships NullAttachmentReinjector (no-op). #11 (student endpoint) and #14
// (real ReAct loop) implement real file/Skill/MCP reinjection with token budgets.
type AttachmentReinjector interface {
	// Reinject appends attachment context to systemPrompt for the given runID.
	// Implementations must respect token budgets (file 50k / Skill 25k / MCP 3k
	// per §4.8.7) and degrade gracefully on missing dependencies (DB, file store).
	Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error)
}

// NullAttachmentReinjector is the v1 no-op implementation: it returns
// systemPrompt unchanged. Used by Restore when caller has no real reinjector
// to wire (e.g. unit tests, #2 mock runner, this whole feature pre-#11).
type NullAttachmentReinjector struct{}

// Reinject returns systemPrompt unchanged.
func (n *NullAttachmentReinjector) Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error) {
	return systemPrompt, nil
}
