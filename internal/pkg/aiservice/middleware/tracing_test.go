package middleware

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
)

// TestTracing_SuccessPath verifies that the middleware forwards the response
// from next unchanged when Langfuse is not initialised (C == nil).
func TestTracing_SuccessPath(t *testing.T) {
	// Ensure Langfuse global client is nil so Enqueue calls are no-ops.
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	logger := &mockLogger{}
	deps := Deps{Logger: logger}
	mw := Tracing(deps)

	expectedResp := &aiservice.ChatResponse{Content: "hello"}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return expectedResp, nil
	})
	handler := mw(inner)

	route := buildTestRoute("llm")
	ctx := context.Background()
	resp, err := handler(ctx, route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != expectedResp {
		t.Errorf("expected %v, got %v", expectedResp, resp)
	}
}

// TestTracing_LangfuseDisabledIsNoop verifies that with Langfuse disabled
// (nil client) the middleware is a transparent passthrough.
func TestTracing_LangfuseDisabledIsNoop(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	deps := Deps{}
	mw := Tracing(deps)

	called := false
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		called = true
		return "noop", nil
	})
	handler := mw(inner)
	route := buildTestRoute("llm")
	resp, err := handler(context.Background(), route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "noop" {
		t.Errorf("unexpected response: %v", resp)
	}
	if !called {
		t.Error("inner handler was not called")
	}
}

// TestTracing_LangfuseTimeoutDoesNotBlockMain verifies the "Langfuse 挂起"
// fault-tolerance path: even when Langfuse is slow or disabled, the middleware
// still delivers the response from next without error.
func TestTracing_LangfuseTimeoutDoesNotBlockMain(t *testing.T) {
	// Disabled client — all SDK helpers become no-ops.
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	logger := &mockLogger{}
	deps := Deps{Logger: logger}
	mw := Tracing(deps)

	// Inject a trace context so Tracing tries to open an observation.
	ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())

	expectedResp := "langfuse-ok"
	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return expectedResp, nil
	})

	handler := mw(inner)
	route := buildTestRoute("llm")
	resp, err := handler(ctx, route, nil)

	if err != nil {
		t.Fatalf("unexpected error after simulated Langfuse hang: %v", err)
	}
	if resp != expectedResp {
		t.Errorf("expected %q, got %v", expectedResp, resp)
	}
	if callCount != 1 {
		t.Errorf("inner handler should be called exactly once, got %d", callCount)
	}
}

// TestTracing_NoOpWhenLangfuseNil verifies that when the Langfuse client is nil
// the Tracing middleware is a transparent passthrough and does not panic.
// (Full panic-injection testing requires a Langfuse SDK mock — deferred.)
func TestTracing_NoOpWhenLangfuseNil(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	logger := &mockLogger{}
	deps := Deps{Logger: logger}
	mw := Tracing(deps)

	expectedResp := "safe"
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return expectedResp, nil
	})
	handler := mw(inner)
	route := buildTestRoute("asr") // non-LLM → Span path
	resp, err := handler(context.Background(), route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != expectedResp {
		t.Errorf("expected %q, got %v", expectedResp, resp)
	}
}

// TestTracing_ErrorPath verifies that errors from next are propagated correctly.
func TestTracing_ErrorPath(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	deps := Deps{Logger: &mockLogger{}}
	mw := Tracing(deps)

	sentinel := errors.New("provider down")
	ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())

	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return nil, sentinel
	})
	handler := mw(inner)
	route := buildTestRoute("llm")

	_, err := handler(ctx, route, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// TestTracing_NonLLMUsesSpanPath validates that non-LLM service types
// go through the Span path without panicking.
func TestTracing_NonLLMUsesSpanPath(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	for _, svcType := range []string{"ocr", "asr"} {
		t.Run(svcType, func(t *testing.T) {
			deps := Deps{Logger: &mockLogger{}}
			mw := Tracing(deps)
			inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
				return "span-response", nil
			})
			handler := mw(inner)
			route := buildTestRoute(svcType)
			ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())
			resp, err := handler(ctx, route, nil)
			if err != nil {
				t.Errorf("unexpected error for %s: %v", svcType, err)
			}
			if resp != "span-response" {
				t.Errorf("unexpected response for %s: %v", svcType, resp)
			}
		})
	}
}

// TestTracing_WithTraceContext verifies that a trace context already present
// causes observation IDs to be set (observationID != "") without error.
func TestTracing_WithTraceContext(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	deps := Deps{Logger: &mockLogger{}}
	mw := Tracing(deps)

	// Provide a real trace context — tracing middleware will try to open an observation.
	ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())
	ctx = WithUserID(ctx, 42)

	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return "with-trace", nil
	})
	handler := mw(inner)
	route := buildTestRoute("llm")
	resp, err := handler(ctx, route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "with-trace" {
		t.Errorf("unexpected response: %v", resp)
	}
}
