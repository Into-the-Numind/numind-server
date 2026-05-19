package main

import (
	"context"

	"numind-server/internal/pkg/langfuse"
)

// instrumentedToolCall wraps a tool execution with a Langfuse span so that tool
// invocations appear as child observations under the current trace. If no trace
// context is present in ctx the function is called directly with no overhead.
//
// Span naming convention: "span-tool-exec-<toolName>" (spec §3.6).
// Langfuse API used (exact signatures, verified against helpers.go):
//
//	langfuse.CreateSpan(traceID, spanID, name string, opts ...SpanOption)
//	langfuse.EndSpan(traceID, spanID string, opts ...SpanOption)
func instrumentedToolCall(ctx context.Context, toolName string, fn func() (string, error)) (string, error) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		// No active trace — call directly without instrumentation.
		return fn()
	}

	spanID := langfuse.SpanID()
	spanName := "span-tool-exec-" + toolName

	// name is the 3rd positional argument, not an option (verified in helpers.go:117).
	langfuse.CreateSpan(tc.TraceID, spanID, spanName,
		langfuse.WithSpanParent(tc.ParentObservationID),
	)
	// traceID is the 1st positional argument (verified in helpers.go:142).
	defer langfuse.EndSpan(tc.TraceID, spanID)

	return fn()
}
