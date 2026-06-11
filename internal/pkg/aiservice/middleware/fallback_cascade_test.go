package middleware

import (
	"context"
	"errors"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// cascadeRoute builds a fallback route with a distinct ServiceID/name.
func cascadeRoute(id uint64, name string) registry.ResolvedRoute {
	return registry.ResolvedRoute{
		TaskID:      "test.task",
		ServiceID:   id,
		ServiceKey:  name,
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: name},
	}
}

// TestFallback_CascadeTriesAllUntilSuccess (T4): with 3 fallbacks, the first two
// fail and the third succeeds; the cascade must try them in priority order and
// return the third's result. Pre-T4 only fallbacks[0] was tried.
func TestFallback_CascadeTriesAllUntilSuccess(t *testing.T) {
	resolver := &registryStub{
		primaryRoute: buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{
			cascadeRoute(99, "fb1"), cascadeRoute(100, "fb2"), cascadeRoute(101, "fb3"),
		},
	}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAIProviderTimeout) // primary fails (retryable)
	tracker.setResponse(99, nil, errno.ErrAIProviderError)   // fb1 fails
	tracker.setResponse(100, nil, errno.ErrAIProviderError)  // fb2 fails
	tracker.setResponse(101, "fb3-ok", nil)                  // fb3 succeeds

	resp, err := mw(tracker.Handler())(context.Background(), buildTestRoute("llm"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "fb3-ok" {
		t.Errorf("expected fb3-ok, got %v", resp)
	}
	// Primary + all three fallbacks attempted, in order.
	want := []uint64{42, 99, 100, 101}
	if len(tracker.calls) != len(want) {
		t.Fatalf("expected calls %v, got %v", want, tracker.calls)
	}
	for i, id := range want {
		if tracker.calls[i] != id {
			t.Errorf("call[%d] = %d; want %d (order matters)", i, tracker.calls[i], id)
		}
	}
}

// TestFallback_CascadeAllFailReturnsExhaustedWithProvenance (T4 + reviewer F4):
// all fallbacks fail → ErrAIFallbackExhausted whose message lists tried service IDs.
func TestFallback_CascadeAllFailReturnsExhaustedWithProvenance(t *testing.T) {
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{cascadeRoute(99, "fb1"), cascadeRoute(100, "fb2")},
	}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAIProviderTimeout)
	tracker.setResponse(99, nil, errno.ErrAIProviderError)
	tracker.setResponse(100, nil, errno.ErrAIProviderError)

	_, err := mw(tracker.Handler())(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAIFallbackExhausted) {
		t.Fatalf("expected ErrAIFallbackExhausted, got %v", err)
	}
	if !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), "100") {
		t.Errorf("expected exhausted error to carry tried service IDs (99,100); got %v", err)
	}
}

// TestFallback_CascadeContinuesOnNonRetryableCandidate (reviewer F5): a fallback
// candidate returning a NON-retryable error must not stop the cascade — the next
// candidate is still tried (fallbacks are last-resort, try them all).
func TestFallback_CascadeContinuesOnNonRetryableCandidate(t *testing.T) {
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{cascadeRoute(99, "fb1"), cascadeRoute(100, "fb2")},
	}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAIProviderTimeout)        // primary retryable
	tracker.setResponse(99, nil, errors.New("HTTP 403: forbidden")) // non-retryable candidate
	tracker.setResponse(100, "fb2-ok", nil)                         // next candidate succeeds

	resp, err := mw(tracker.Handler())(context.Background(), buildTestRoute("llm"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "fb2-ok" {
		t.Errorf("expected cascade to continue past non-retryable fb1 to fb2-ok, got %v", resp)
	}
	if len(tracker.calls) != 3 { // 42, 99, 100
		t.Errorf("expected primary + both fallbacks attempted (3 calls), got %v", tracker.calls)
	}
}
