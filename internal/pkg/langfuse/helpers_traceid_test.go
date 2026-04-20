package langfuse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Regression: pre-2026-04-21 EndGeneration/EndSpan emitted update events with
// an empty TraceID, which Langfuse ingestion rejects per-event with 400
// "Too small: expected string to have >=1 characters" — silently draining
// all endTime updates for 4 days.

func TestEndGeneration_EmitsTraceID(t *testing.T) {
	prev := C
	defer func() { C = prev }()
	C = NewTestClient()

	var captured []*IngestionEvent
	C.InstallEventHook(func(e *IngestionEvent) {
		captured = append(captured, e)
	})

	EndGeneration("trace-abc", "gen-xyz",
		WithGenOutput(map[string]interface{}{"k": "v"}),
	)

	assert.Len(t, captured, 1)
	assert.Equal(t, "generation-update", captured[0].Type)
	body, ok := captured[0].Body.(*GenerationBody)
	if !ok {
		t.Fatalf("expected *GenerationBody, got %T", captured[0].Body)
	}
	assert.Equal(t, "gen-xyz", body.ID)
	assert.Equal(t, "trace-abc", body.TraceID, "TraceID required by Langfuse ingestion")
	assert.NotNil(t, body.EndTime)
}

func TestEndSpan_EmitsTraceID(t *testing.T) {
	prev := C
	defer func() { C = prev }()
	C = NewTestClient()

	var captured []*IngestionEvent
	C.InstallEventHook(func(e *IngestionEvent) {
		captured = append(captured, e)
	})

	EndSpan("trace-abc", "span-xyz")

	assert.Len(t, captured, 1)
	assert.Equal(t, "span-update", captured[0].Type)
	body, ok := captured[0].Body.(*SpanBody)
	if !ok {
		t.Fatalf("expected *SpanBody, got %T", captured[0].Body)
	}
	assert.Equal(t, "span-xyz", body.ID)
	assert.Equal(t, "trace-abc", body.TraceID, "TraceID required by Langfuse ingestion")
	assert.NotNil(t, body.EndTime)
}
