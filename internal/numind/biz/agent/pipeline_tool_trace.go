package agent

import (
	"context"
	"time"

	"numind-server/internal/pkg/langfuse"
)

const pipelineToolTraceNoError = "none"

// safePipelineToolSpan deliberately accepts only caller-built scalar summaries.
// Callers must never pass raw tool input/output, URLs, cursors, tokens, content,
// customer names, or error strings.
type safePipelineToolSpan struct {
	traceID string
	spanID  string
	start   time.Time
}

func startSafePipelineToolSpan(ctx context.Context, name string, input map[string]any) *safePipelineToolSpan {
	trace := langfuse.FromContext(ctx)
	if trace == nil {
		return nil
	}
	span := &safePipelineToolSpan{
		traceID: trace.TraceID,
		spanID:  langfuse.SpanID(),
		start:   time.Now(),
	}
	langfuse.CreateSpan(span.traceID, span.spanID, name,
		langfuse.WithSpanParent(trace.ParentObservationID),
		langfuse.WithSpanInput(input),
	)
	return span
}

func (s *safePipelineToolSpan) End(output map[string]any, errorClass string) {
	if s == nil {
		return
	}
	safeOutput := make(map[string]any, len(output)+2)
	for key, value := range output {
		safeOutput[key] = value
	}
	if errorClass == "" {
		errorClass = pipelineToolTraceNoError
	}
	safeOutput["duration_ms"] = time.Since(s.start).Milliseconds()
	safeOutput["error_class"] = errorClass
	opts := []langfuse.SpanOption{langfuse.WithSpanOutput(safeOutput)}
	if errorClass != pipelineToolTraceNoError {
		opts = append(opts, langfuse.WithSpanError(errorClass))
	}
	langfuse.EndSpan(s.traceID, s.spanID, opts...)
}
