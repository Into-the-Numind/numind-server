package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// registryStub — minimal Registry implementation for Fallback tests
// ----------------------------------------------------------------------------

type registryStub struct {
	primaryRoute   *registry.ResolvedRoute
	fallbackRoutes []registry.ResolvedRoute
	resolveErr     error
	// alternates / alternatesErr drive ResolveModelAlternates (streaming
	// cross-provider fallback). Left zero by tests that don't exercise it.
	alternates    []registry.ResolvedRoute
	alternatesErr error
}

func (r *registryStub) GetService(_ context.Context, _ uint64) (*model.AIService, error) {
	return nil, nil
}
func (r *registryStub) ListServices(_ context.Context, _ registry.ServiceFilter) ([]*model.AIService, error) {
	return nil, nil
}
func (r *registryStub) ListServicesPaginated(_ context.Context, _ registry.ServiceFilter, _, _ int) ([]*model.AIService, int64, error) {
	return nil, 0, nil
}
func (r *registryStub) SaveService(_ context.Context, _ *model.AIService, _ uint64, _ string) error {
	return nil
}
func (r *registryStub) DeprecateService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	return nil
}
func (r *registryStub) RestoreService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	return nil
}
func (r *registryStub) GetTaskProfile(_ context.Context, _ string) (*model.TaskProfile, error) {
	return nil, nil
}
func (r *registryStub) ListTaskProfiles(_ context.Context) ([]*model.TaskProfile, error) {
	return nil, nil
}
func (r *registryStub) SaveTaskProfile(_ context.Context, _ *model.TaskProfile, _ []registry.TaskBinding, _ uint64, _ string) error {
	return nil
}
func (r *registryStub) ResolveTask(_ context.Context, _ string) (*registry.ResolvedRoute, []registry.ResolvedRoute, error) {
	if r.resolveErr != nil {
		return nil, nil, r.resolveErr
	}
	return r.primaryRoute, r.fallbackRoutes, nil
}

func (r *registryStub) ResolveByModelKey(_ context.Context, _ string, _ string) (*registry.ResolvedRoute, error) {
	// stub: always return not-found so gateway falls back to profile default
	return nil, errno.ErrAIServiceNotFound
}

func (r *registryStub) ResolveModelAlternates(_ context.Context, _ string, _, _ uint64) ([]registry.ResolvedRoute, error) {
	return r.alternates, r.alternatesErr
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func fallbackRoute() registry.ResolvedRoute {
	return registry.ResolvedRoute{
		TaskID:      "test.task",
		ServiceID:   99,
		ServiceKey:  "fallback-model",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "fallback-provider"},
		// Pricing amounts removed from route in T-arch; billing middleware
		// resolves them from pricing_rule at call time.
		Pricing: registry.PricingInfo{Unit: "per_1m_tokens"},
	}
}

// upstreamCallTracker records which serviceIDs the inner handler was called with,
// and returns pre-configured responses.
type upstreamCallTracker struct {
	calls     []uint64
	responses map[uint64]upstreamResponse
}

type upstreamResponse struct {
	resp interface{}
	err  error
}

func newUpstreamTracker() *upstreamCallTracker {
	return &upstreamCallTracker{
		responses: make(map[uint64]upstreamResponse),
	}
}

func (u *upstreamCallTracker) setResponse(serviceID uint64, resp interface{}, err error) {
	u.responses[serviceID] = upstreamResponse{resp, err}
}

func (u *upstreamCallTracker) Handler() Handler {
	return func(_ context.Context, route *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		u.calls = append(u.calls, route.ServiceID)
		if entry, ok := u.responses[route.ServiceID]; ok {
			return entry.resp, entry.err
		}
		return nil, errors.New("no response configured for serviceID")
	}
}

// dummy time reference to prevent unused-import.
var _ = time.Now

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestFallback_PrimarySuccess verifies that when the primary service succeeds,
// no fallback is triggered and exactly 1 upstream call is made.
func TestFallback_PrimarySuccess(t *testing.T) {
	fb := fallbackRoute()
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{fb},
	}
	deps := Deps{Resolver: resolver, Logger: &mockLogger{}}
	mw := Fallback(deps)

	tracker := newUpstreamTracker()
	tracker.setResponse(42, "primary-ok", nil)

	handler := mw(tracker.Handler())
	route := buildTestRoute("llm") // ServiceID = 42
	resp, err := handler(context.Background(), route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "primary-ok" {
		t.Errorf("expected primary-ok, got %v", resp)
	}
	if len(tracker.calls) != 1 {
		t.Errorf("expected 1 upstream call, got %d", len(tracker.calls))
	}
}

// TestFallback_PrimaryFailsThenFallbackSucceeds verifies:
//   - Primary service fails with retryable error (Retry layer → 2 upstream calls).
//   - Fallback service is invoked once (skip_retry injected by Fallback middleware).
//   - Total upstream calls = 3 (spec §6.5 upper bound).
//   - Fallback response is returned to the caller.
func TestFallback_PrimaryFailsThenFallbackSucceeds(t *testing.T) {
	fb := fallbackRoute()
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{fb},
	}
	deps := Deps{Resolver: resolver, Logger: &mockLogger{}}
	// Compose Fallback (outer) → Retry (inner) to replicate the production chain.
	chain := Chain(Fallback(deps), retryWithPolicy(zeroDelayPolicy()))

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAIProviderTimeout) // primary always fails
	tracker.setResponse(99, "fallback-ok", nil)              // fallback succeeds

	handler := chain(tracker.Handler())
	route := buildTestRoute("llm") // ServiceID = 42
	resp, err := handler(context.Background(), route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "fallback-ok" {
		t.Errorf("expected fallback-ok, got %v", resp)
	}

	primaryCalls := 0
	fallbackCalls := 0
	for _, id := range tracker.calls {
		switch id {
		case 42:
			primaryCalls++
		case 99:
			fallbackCalls++
		}
	}
	total := len(tracker.calls)

	if primaryCalls != 2 {
		t.Errorf("expected 2 primary calls (initial + 1 retry), got %d", primaryCalls)
	}
	if fallbackCalls != 1 {
		t.Errorf("expected 1 fallback call, got %d", fallbackCalls)
	}
	if total != 3 {
		t.Errorf("total upstream calls must be 3 (spec §6.5), got %d", total)
	}
}

// TestFallback_PrimaryAndFallbackBothFail verifies that when both primary and
// fallback fail, ErrAIFallbackExhausted is returned.
func TestFallback_PrimaryAndFallbackBothFail(t *testing.T) {
	fb := fallbackRoute()
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{fb},
	}
	deps := Deps{Resolver: resolver, Logger: &mockLogger{}}
	chain := Chain(Fallback(deps), retryWithPolicy(zeroDelayPolicy()))

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAIProviderError) // primary fails
	tracker.setResponse(99, nil, errno.ErrAIProviderError) // fallback also fails

	handler := chain(tracker.Handler())
	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAIFallbackExhausted) {
		t.Errorf("expected ErrAIFallbackExhausted, got %v", err)
	}
}

// TestFallback_NoFallbacksConfigured verifies that without fallback routes
// the original error is propagated unchanged.
func TestFallback_NoFallbacksConfigured(t *testing.T) {
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{}, // empty
	}
	deps := Deps{Resolver: resolver, Logger: &mockLogger{}}
	chain := Chain(Fallback(deps), retryWithPolicy(zeroDelayPolicy()))

	handler := chain(errHandler(errno.ErrAIProviderTimeout))
	_, err := handler(context.Background(), buildTestRoute("llm"), nil)

	if errors.Is(err, errno.ErrAIFallbackExhausted) {
		t.Error("must not return ErrAIFallbackExhausted when no fallbacks are configured")
	}
	if err == nil {
		t.Error("expected an error when primary fails with no fallback")
	}
}

// TestFallback_NonRetryableErrorNotFallback verifies that non-retryable errors
// (e.g. ErrAICapabilityMismatch) do not trigger fallback.
func TestFallback_NonRetryableErrorNotFallback(t *testing.T) {
	fb := fallbackRoute()
	resolver := &registryStub{
		primaryRoute:   buildTestRoute("llm"),
		fallbackRoutes: []registry.ResolvedRoute{fb},
	}
	deps := Deps{Resolver: resolver, Logger: &mockLogger{}}
	mw := Fallback(deps)

	tracker := newUpstreamTracker()
	tracker.setResponse(42, nil, errno.ErrAICapabilityMismatch)
	tracker.setResponse(99, "fallback-ok", nil)

	handler := mw(tracker.Handler())
	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAICapabilityMismatch) {
		t.Errorf("non-retryable error should propagate directly, got %v", err)
	}
	for _, id := range tracker.calls {
		if id == 99 {
			t.Error("fallback must not be invoked for non-retryable errors")
		}
	}
}
