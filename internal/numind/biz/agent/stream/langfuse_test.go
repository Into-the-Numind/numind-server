package stream

import (
	"context"
	"testing"

	"numind-server/internal/pkg/langfuse"
)

// setupTestLangfuse installs a test Langfuse client and returns the captured
// events slice plus a cleanup function. It replaces the global langfuse.C for
// the duration of the test.
func setupTestLangfuse(t *testing.T) (*[]*langfuse.IngestionEvent, func()) {
	t.Helper()
	prev := langfuse.C
	langfuse.C = langfuse.NewTestClient()

	var events []*langfuse.IngestionEvent
	langfuse.C.InstallEventHook(func(e *langfuse.IngestionEvent) {
		events = append(events, e)
	})

	return &events, func() {
		langfuse.C = prev
	}
}

// ctxWithTrace returns a context with a Langfuse trace injected.
func ctxWithTrace(traceID string) context.Context {
	return langfuse.WithTrace(context.Background(), traceID)
}

// TestStartSSESpan_NilContext verifies that StartSSESpan with a nil-like
// context (no trace injected) returns a no-op finalize that does not panic.
func TestStartSSESpan_NilContext(t *testing.T) {
	events, cleanup := setupTestLangfuse(t)
	defer cleanup()

	// Context with no trace → tc == nil branch.
	spanID, finalize := StartSSESpan(context.Background(), "", 42)

	if spanID != "" {
		t.Errorf("expected empty spanID for nil trace, got %q", spanID)
	}
	// finalize must not panic.
	finalize(10, "client_disconnect")

	if len(*events) != 0 {
		t.Errorf("expected 0 langfuse events for nil-trace path, got %d", len(*events))
	}
}

// TestStartSSESpan_NilLangfuseClient verifies graceful degradation when
// langfuse.C is nil.
func TestStartSSESpan_NilLangfuseClient(t *testing.T) {
	prev := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = prev }()

	ctx := ctxWithTrace("trace-abc")
	spanID, finalize := StartSSESpan(ctx, "trace-abc", 7)
	// Even with nil client, should not panic.
	_ = spanID
	finalize(5, "completed")
}

// TestStartSSESpan_CreatesSpan verifies that StartSSESpan emits a span-create
// event when a valid trace context is present.
func TestStartSSESpan_CreatesSpan(t *testing.T) {
	events, cleanup := setupTestLangfuse(t)
	defer cleanup()

	const traceID = "trace-123"
	ctx := ctxWithTrace(traceID)

	spanID, finalize := StartSSESpan(ctx, traceID, 99)
	if spanID == "" {
		t.Error("expected non-empty spanID")
	}

	// Should have emitted span-create.
	if len(*events) != 1 {
		t.Fatalf("expected 1 event after StartSSESpan, got %d", len(*events))
	}
	if (*events)[0].Type != "span-create" {
		t.Errorf("expected span-create event, got %q", (*events)[0].Type)
	}

	// Call finalize — should emit span-update.
	finalize(20, "client_disconnect")

	if len(*events) != 2 {
		t.Fatalf("expected 2 events after finalize, got %d", len(*events))
	}
	if (*events)[1].Type != "span-update" {
		t.Errorf("expected span-update event, got %q", (*events)[1].Type)
	}
}

// TestStartSSESpanWithFirstByte_NilContext verifies the full variant is safe
// with no trace context.
func TestStartSSESpanWithFirstByte_NilContext(t *testing.T) {
	events, cleanup := setupTestLangfuse(t)
	defer cleanup()

	spanID, recordFirstByte, finalize := StartSSESpanWithFirstByte(context.Background(), "", 1)
	if spanID != "" {
		t.Errorf("expected empty spanID, got %q", spanID)
	}
	// None of these should panic.
	recordFirstByte()
	finalize(0, "no_events")

	if len(*events) != 0 {
		t.Errorf("expected 0 events, got %d", len(*events))
	}
}

// TestStartSSESpanWithFirstByte_CreatesSpanAndMetadata verifies span-create +
// span-update with metadata fields populated.
func TestStartSSESpanWithFirstByte_CreatesSpanAndMetadata(t *testing.T) {
	events, cleanup := setupTestLangfuse(t)
	defer cleanup()

	const traceID = "trace-456"
	ctx := ctxWithTrace(traceID)

	spanID, recordFirstByte, finalize := StartSSESpanWithFirstByte(ctx, traceID, 42)
	if spanID == "" {
		t.Error("expected non-empty spanID")
	}

	// span-create emitted.
	if len(*events) < 1 || (*events)[0].Type != "span-create" {
		t.Errorf("expected span-create, got events: %v", eventTypes(*events))
	}

	// Record first byte.
	recordFirstByte()

	// Calling recordFirstByte again should be idempotent (no panic, no extra events).
	recordFirstByte()

	// Finalize.
	finalize(50, "terminal")

	if len(*events) != 2 {
		t.Fatalf("expected 2 events total, got %d: %v", len(*events), eventTypes(*events))
	}
	if (*events)[1].Type != "span-update" {
		t.Errorf("expected span-update, got %q", (*events)[1].Type)
	}
}

// eventTypes extracts Type strings for easier assertion messages.
func eventTypes(events []*langfuse.IngestionEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
