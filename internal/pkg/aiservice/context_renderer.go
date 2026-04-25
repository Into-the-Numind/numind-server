package aiservice

import "numind-server/internal/pkg/contextbudget"

// RenderContextFragments converts an ordered slice of ContextFragment into
// ChatMessage entries while preserving fragment Order.
//
// Source role mapping:
//
//	SourceSystem    → "system"
//	SourceUser      → "user"
//	SourceAssistant → "assistant"
//	SourceTool      → "tool"
//	SourceFile, SourceKB, SourceDB, SourceWeb, SourceInternal → "user"
//
// Fragments with empty Content are skipped (e.g. Compressibility=Reference with
// SourceReference set — such fragments carry no text body to render).
//
// The returned slice preserves the relative ordering of the input slice; callers
// are responsible for pre-sorting fragments by Order if a particular sequence is
// required.
func RenderContextFragments(fragments []contextbudget.ContextFragment) []ChatMessage {
	out := make([]ChatMessage, 0, len(fragments))
	for _, f := range fragments {
		// Skip fragments with empty content — they carry no renderable text body.
		if f.Content == "" {
			continue
		}
		role := sourceToRole(f.Source)
		out = append(out, ChatMessage{
			Role:    role,
			Content: MessageContent{Text: f.Content},
		})
	}
	return out
}

// sourceToRole maps a FragmentSource to a MessageRole.
// SourceSystem maps to system; SourceAssistant to assistant; SourceTool to tool;
// all other sources (user-facing or retrieved content) map to user.
func sourceToRole(src contextbudget.FragmentSource) MessageRole {
	switch src {
	case contextbudget.SourceSystem:
		return MessageRoleSystem
	case contextbudget.SourceAssistant:
		return MessageRoleAssistant
	case contextbudget.SourceTool:
		return MessageRole("tool")
	default:
		// SourceUser, SourceFile, SourceKB, SourceDB, SourceWeb, SourceInternal → "user"
		return MessageRoleUser
	}
}
