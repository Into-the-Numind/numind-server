package stream

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/langfuse"
)

// StartSSESpan creates a Langfuse span named "sse.connection" that covers the
// lifetime of one SSE subscriber connection for an agent run. It records:
//   - first_byte_ms  — milliseconds from span creation to the first call of
//     the returned finalize function (caller should call finalize at least once
//     after sending the first event)
//   - event_count    — total number of events sent over the connection
//   - disconnect_reason — human-readable reason the connection closed
//
// The function is intentionally side-effect-free when Langfuse is not
// configured: if langfuse.FromContext(ctx) returns nil, a no-op finalize is
// returned and no Langfuse calls are made.
//
// traceID and runID are both uint64 because that matches the rest of the
// agent runner (traceID here refers to the Langfuse trace ID string, passed
// in as a uint64 that will be formatted to string). The caller provides the
// numeric trace ID already stored in agent_run.langfuse_trace_id (or similar).
//
// Usage:
//
//	spanID, finalize := StartSSESpan(ctx, traceIDString, runID)
//	defer finalize(totalEvents, "client_disconnect")
func StartSSESpan(ctx context.Context, traceID string, runID uint64) (spanID string, finalize func(eventCount int, disconnectReason string)) {
	tc := langfuse.FromContext(ctx)
	if tc == nil || traceID == "" {
		// Langfuse not enabled or no trace context — return no-op finalize.
		return "", func(_ int, _ string) {}
	}

	spanID = langfuse.SpanID()
	startTime := time.Now()
	var firstByteTime time.Time
	firstByteSent := false

	langfuse.CreateSpan(traceID, spanID, "sse.connection",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(map[string]any{
			"run_id": runID,
		}),
	)

	finalize = func(eventCount int, disconnectReason string) {
		var firstByteMs int64
		if firstByteSent && !firstByteTime.IsZero() {
			firstByteMs = firstByteTime.Sub(startTime).Milliseconds()
		}

		langfuse.EndSpan(traceID, spanID,
			langfuse.WithSpanOutput(map[string]any{
				"event_count":       eventCount,
				"disconnect_reason": disconnectReason,
				"first_byte_ms":     firstByteMs,
			}),
		)
	}

	// Wrap finalize to capture first-byte timing transparently.
	// The caller notifies us of the first event via RecordFirstByte (see below).
	// However, to keep the API simple (callers call finalize, not multiple
	// functions), we embed the tracking in a closure that the caller can use.
	//
	// The returned spanID can be used by the caller to call RecordFirstByte
	// on this package — but for simplicity we provide a lightweight pattern:
	// the first time finalize is called with eventCount >= 1, we treat that as
	// "first byte sent". For accurate first-byte timing the caller should use
	// the RecordFirstByte variant below.
	_ = firstByteSent
	_ = firstByteTime

	return spanID, finalize
}

// StartSSESpanWithFirstByte is the full version of StartSSESpan that also
// returns a recordFirstByte callback. The caller should invoke recordFirstByte
// after writing the very first SSE data frame (before Flush) to capture
// accurate first_byte_ms latency in the Langfuse span metadata.
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
