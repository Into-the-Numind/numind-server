package agent

import "context"

type toolCallIDContextKey struct{}

// WithToolCallID returns a child context carrying the current adapter-generated
// tool call identity. A nil parent is treated as context.Background.
func WithToolCallID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolCallIDContextKey{}, id)
}

// ToolCallIDFromContext returns the adapter-generated tool call identity, or an
// empty string when the context is nil or does not carry one.
func ToolCallIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDContextKey{}).(string)
	return id
}
