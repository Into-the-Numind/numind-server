package memory

import (
	"html"
	"strings"
)

// FenceRenderer assembles the <memory-context> XML segment injected into the
// system prompt.  Content stored in DB is already html-escaped (written via
// EscapeForStorage); RenderMemoryBlock does NOT escape again.
type FenceRenderer struct{}

// NewFenceRenderer returns a ready-to-use FenceRenderer.
func NewFenceRenderer() *FenceRenderer { return &FenceRenderer{} }

// RenderMemoryBlock assembles the full <memory-context> block from L1 and L2
// items.  Returns an empty string when both slices are empty.
//
// Output format (when both layers have data):
//
//	\n\n<memory-context>\n
//	[全局画像]\n
//	- <kind>: <content>\n
//	... (one per L2 item)
//	\n
//	[本 agent 历史]\n
//	- <kind>: <content>\n
//	... (one per L1 item)
//	</memory-context>\n
func (f *FenceRenderer) RenderMemoryBlock(l1, l2 []MemoryItem) string {
	if len(l1) == 0 && len(l2) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n<memory-context>\n")
	if len(l2) > 0 {
		sb.WriteString("[全局画像]\n")
		for _, item := range l2 {
			sb.WriteString("- ")
			sb.WriteString(string(item.Kind))
			sb.WriteString(": ")
			sb.WriteString(item.Content)
			sb.WriteString("\n")
		}
	}
	if len(l1) > 0 {
		if len(l2) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[本 agent 历史]\n")
		for _, item := range l1 {
			sb.WriteString("- ")
			sb.WriteString(string(item.Kind))
			sb.WriteString(": ")
			sb.WriteString(item.Content)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</memory-context>\n")
	return sb.String()
}

// EscapeForStorage escapes raw user/agent content before writing to the DB.
// Converts <, >, & (and " ') to their HTML entity equivalents so that
// raw XML tags cannot be injected into the <memory-context> fence.
func EscapeForStorage(raw string) string {
	return html.EscapeString(raw)
}

// UnescapeForToolResponse reverses EscapeForStorage so that memory_read tool
// responses contain the original content rather than HTML entities.
func UnescapeForToolResponse(escaped string) string {
	return html.UnescapeString(escaped)
}
