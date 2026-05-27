package stream

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/langfuse"
)

// StartSSESpan creates a Langfuse span named "sse.connection" that covers the
// lifetime of one SSE subscriber connection for an agent run. It records:
//   - event_count       — total number of events sent over the connection
//   - disconnect_reason — human-readable reason the connection closed
//
// This simple variant does NOT track first_byte_ms. For accurate first-byte
// timing (required by spec §7), use StartSSESpanWithFirstByte instead. SSE
// controllers should default to the WithFirstByte variant; this variant is
// kept for callers that only need begin/end markers without per-event timing.
//
// The function is intentionally side-effect-free when Langfuse is not
// configured: if langfuse.FromContext(ctx) returns nil, a no-op finalize is
// returned and no Langfuse calls are made.
//
// Usage:
//
//	spanID, finalize := StartSSESpan(ctx, traceID, runID)
//	defer finalize(totalEvents, "client_disconnect")
func StartSSESpan(ctx context.Context, traceID string, runID uint64) (spanID string, finalize func(eventCount int, disconnectReason string)) {
	tc := langfuse.FromContext(ctx)
	if tc == nil || traceID == "" {
		return "", func(_ int, _ string) {}
	}

	spanID = langfuse.SpanID()

	langfuse.CreateSpan(traceID, spanID, "sse.connection",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(map[string]any{
			"run_id": runID,
		}),
	)

	finalize = func(eventCount int, disconnectReason string) {
		langfuse.EndSpan(traceID, spanID,
			langfuse.WithSpanOutput(map[string]any{
				"event_count":       eventCount,
				"disconnect_reason": disconnectReason,
			}),
		)
	}

	return spanID, finalize
}

// StartSSESpanWithFirstByte is the full version of StartSSESpan that also
// returns a recordFirstByte callback. The caller should invoke recordFirstByte
// after writing the very first SSE data frame (before Flush) to capture
// accurate first_byte_ms latency in the Langfuse span metadata.
//
// PRECONDITION: recordFirstByte and finalize must be called from the same
// goroutine (or otherwise externally synchronized). The shared firstByteMs
// is not protected by sync/atomic; the documented call pattern is the
// controller's SSE write loop calling recordFirstByte on first event and
// finalize via defer on the same goroutine. If you need cross-goroutine
// invocation, wrap firstByteMs in sync/atomic.Int64 here.
//
// Example:
//
//	spanID, recordFirstByte, finalize := StartSSESpanWithFirstByte(ctx, traceID, runID)
//	defer finalize(totalEvents, "client_disconnect")
//	// ... write first event ...
//	recordFirstByte()
func StartSSESpanWithFirstByte(
	ctx context.Context,
	traceID string,
	runID uint64,
) (spanID string, recordFirstByte func(), finalize func(eventCount int, disconnectReason string)) {
	tc := langfuse.FromContext(ctx)
	if tc == nil || traceID == "" {
		return "", func() {}, func(_ int, _ string) {}
	}

	spanID = langfuse.SpanID()
	startTime := time.Now()
	var firstByteMs int64

	langfuse.CreateSpan(traceID, spanID, "sse.connection",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(map[string]any{
			"run_id": runID,
		}),
	)

	recordFirstByte = func() {
		if firstByteMs == 0 {
			firstByteMs = time.Since(startTime).Milliseconds()
		}
	}

	finalize = func(eventCount int, disconnectReason string) {
		langfuse.EndSpan(traceID, spanID,
			langfuse.WithSpanOutput(map[string]any{
				"event_count":       eventCount,
				"disconnect_reason": disconnectReason,
				"first_byte_ms":     firstByteMs,
				"total_duration_ms": time.Since(startTime).Milliseconds(),
			}),
			langfuse.WithSpanMetadata(map[string]string{
				"run_id":                  fmt.Sprintf("%d", runID),
				"stream_protocol_version": "v2",
			}),
		)
	}

	return spanID, recordFirstByte, finalize
}
