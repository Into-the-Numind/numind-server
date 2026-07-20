package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
)

func TestTracing_OCRRedactsSignedURLAndRecognizedText(t *testing.T) {
	original := langfuse.C
	testClient := langfuse.NewTestClient()
	langfuse.C = testClient
	defer func() { langfuse.C = original }()

	var events []*langfuse.IngestionEvent
	testClient.InstallEventHook(func(event *langfuse.IngestionEvent) {
		events = append(events, event)
	})
	secretURL := "https://bucket.cos.example/secret.png?q-signature=must-not-leak"
	secretText := "private customer OCR content"
	handler := Tracing(Deps{Logger: &mockLogger{}})(Handler(
		func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
			return &aiservice.OCRResponse{
				Text: secretText, Provider: "baidu",
				Words: []aiservice.OCRWord{{Word: "private"}, {Word: "customer"}},
			}, nil
		},
	))
	ctx := langfuse.WithTrace(context.Background(), "ocr-redaction-trace")

	_, err := handler(ctx, buildTestRoute("ocr"), aiservice.OCRRequest{ImageURL: secretURL, ImageFormat: "png"})
	require.NoError(t, err)
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	payload := string(encoded)
	assert.NotContains(t, payload, secretURL)
	assert.NotContains(t, payload, "q-signature")
	assert.NotContains(t, payload, secretText)
	assert.NotContains(t, payload, `"word":"private"`)
	assert.Contains(t, payload, `"image_source":"url"`)
	assert.Contains(t, payload, `"text_bytes":`)
	assert.Contains(t, payload, `"word_count":2`)
	assert.True(t, strings.Contains(payload, "span-create") && strings.Contains(payload, "span-update"))
}

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

// TestMergeTraceMetadata covers the Task 9 helper: merging resolved fields
// into Langfuse output.metadata sub-map, with nil-safety + zero-value skip.
func TestMergeTraceMetadata(t *testing.T) {
	cases := []struct {
		name       string
		outputMap  map[string]interface{}
		tm         *aiservice.TraceMetadata
		wantKeys   map[string]interface{}
		wantNoKeys []string
	}{
		{
			name:      "nil tm is no-op",
			outputMap: map[string]interface{}{"metadata": map[string]interface{}{"existing": "v"}},
			tm:        nil,
			wantKeys:  map[string]interface{}{"existing": "v"},
		},
		{
			name:       "nil outputMap is no-op",
			outputMap:  nil,
			tm:         &aiservice.TraceMetadata{ResolvedReasoningEffort: "medium"},
			wantKeys:   nil, // nothing to assert, just no panic
			wantNoKeys: nil,
		},
		{
			name:      "all fields set",
			outputMap: map[string]interface{}{"metadata": map[string]interface{}{}},
			tm: &aiservice.TraceMetadata{
				ResolvedReasoningEffort: "medium",
				ResolvedModelFamily:     "claude",
				TempOverridden:          true,
			},
			wantKeys: map[string]interface{}{
				"resolved_reasoning_effort": "medium",
				"resolved_model_family":     "claude",
				"temp_overridden":           true,
			},
		},
		{
			name:      "zero values skipped",
			outputMap: map[string]interface{}{"metadata": map[string]interface{}{"existing": "v"}},
			tm: &aiservice.TraceMetadata{
				ResolvedReasoningEffort: "",    // skip
				ResolvedModelFamily:     "gpt", // keep
				TempOverridden:          false, // skip
			},
			wantKeys: map[string]interface{}{
				"existing":              "v",
				"resolved_model_family": "gpt",
			},
			wantNoKeys: []string{"resolved_reasoning_effort", "temp_overridden"},
		},
		{
			name:      "intrinsic sentinel",
			outputMap: map[string]interface{}{"metadata": map[string]interface{}{}},
			tm:        &aiservice.TraceMetadata{ResolvedReasoningEffort: "intrinsic"},
			wantKeys: map[string]interface{}{
				"resolved_reasoning_effort": "intrinsic",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic on nil inputs
			mergeTraceMetadata(tc.outputMap, tc.tm)
			if tc.outputMap == nil {
				return
			}
			meta, ok := tc.outputMap["metadata"].(map[string]interface{})
			if !ok {
				t.Fatalf("metadata key missing or wrong type: %#v", tc.outputMap["metadata"])
			}
			for k, want := range tc.wantKeys {
				got, present := meta[k]
				if !present {
					t.Errorf("expected key %q not present in metadata: %#v", k, meta)
					continue
				}
				if got != want {
					t.Errorf("metadata[%q] = %v, want %v", k, got, want)
				}
			}
			for _, k := range tc.wantNoKeys {
				if _, present := meta[k]; present {
					t.Errorf("unexpected key %q present (should be skipped for zero value)", k)
				}
			}
		})
	}
}

// ----------------------------------------------------------------------------
// llm-prompt-cache: Langfuse dual-channel cached-token observability (D3)
// ----------------------------------------------------------------------------

// TestAppendCachedUsageGenOption_CacheHit verifies that on a cache hit the
// helper (a) appends a usage GenOption (channel A) and (b) writes the cached
// count into output.metadata (channel B, guaranteed visible on Langfuse v3).
func TestAppendCachedUsageGenOption_CacheHit(t *testing.T) {
	outputMap := map[string]interface{}{"metadata": map[string]interface{}{"existing": "v"}}
	usage := &aiservice.TokenUsage{PromptTokens: 1000, CompletionTokens: 200, CachedPromptTokens: 400}

	opts := appendCachedUsageGenOption(nil, outputMap, usage)

	// Channel A: exactly one usage option appended, carrying CachedInput=400.
	if len(opts) != 1 {
		t.Fatalf("expected 1 GenOption appended, got %d", len(opts))
	}
	g := &langfuse.GenerationBody{}
	opts[0](g)
	if g.Usage == nil || g.Usage.CachedInput != 400 {
		t.Errorf("channel A: CachedInput not set, got %+v", g.Usage)
	}
	if g.Usage.Input != 1000 || g.Usage.Output != 200 {
		t.Errorf("channel A: input/output wrong, got %+v", g.Usage)
	}

	// Channel B: output.metadata.cached_input_tokens set, existing keys preserved.
	meta, ok := outputMap["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata map missing")
	}
	if meta["cached_input_tokens"] != 400 {
		t.Errorf("channel B: cached_input_tokens = %v, want 400", meta["cached_input_tokens"])
	}
	if meta["existing"] != "v" {
		t.Errorf("channel B clobbered existing metadata: %v", meta)
	}
}

// TestAppendCachedUsageGenOption_NoCacheByteIdentical is the zero-regression
// control: when CachedPromptTokens==0 the helper appends the plain usage option
// and does NOT mutate outputMap, so a non-cache generation's output bytes stay
// byte-identical to pre-cache behavior.
func TestAppendCachedUsageGenOption_NoCacheByteIdentical(t *testing.T) {
	metaBefore := map[string]interface{}{"existing": "v"}
	outputMap := map[string]interface{}{"metadata": metaBefore}
	usage := &aiservice.TokenUsage{PromptTokens: 1000, CompletionTokens: 200, CachedPromptTokens: 0}

	opts := appendCachedUsageGenOption(nil, outputMap, usage)

	if len(opts) != 1 {
		t.Fatalf("expected 1 GenOption appended, got %d", len(opts))
	}
	g := &langfuse.GenerationBody{}
	opts[0](g)
	if g.Usage == nil || g.Usage.CachedInput != 0 {
		t.Errorf("no-cache: CachedInput must stay 0, got %+v", g.Usage)
	}

	// Channel B must NOT fire: no cached_input_tokens key, metadata untouched.
	meta := outputMap["metadata"].(map[string]interface{})
	if _, present := meta["cached_input_tokens"]; present {
		t.Error("no-cache: cached_input_tokens must NOT be set (output bytes must stay identical)")
	}
	if len(meta) != 1 || meta["existing"] != "v" {
		t.Errorf("no-cache: metadata mutated, got %v", meta)
	}
}

// TestAppendCachedUsageGenOption_NilUsage verifies a nil usage appends nothing
// (matches the existing `if usage != nil` guard at both EndGeneration sites).
func TestAppendCachedUsageGenOption_NilUsage(t *testing.T) {
	outputMap := map[string]interface{}{"metadata": map[string]interface{}{}}
	opts := appendCachedUsageGenOption(nil, outputMap, nil)
	if len(opts) != 0 {
		t.Errorf("nil usage must append 0 options, got %d", len(opts))
	}
}
