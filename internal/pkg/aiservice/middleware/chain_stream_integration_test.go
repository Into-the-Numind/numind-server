package middleware

import (
	"context"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
)

// tsUsageStore is a thread-safe UsageStore for the integration tests. Billing
// persists records from its own goroutine (fire-and-forget relative to the
// stream consumer), and a failover drives two such goroutines, so the shared
// non-locking mockUsageStore would race here. GetPricingRule returns not-found
// (cost 0) — these tests assert on record COUNT, not cost.
type tsUsageStore struct {
	mu      sync.Mutex
	records []*model.UsageRecord
}

func (s *tsUsageStore) CreateUsageRecord(_ context.Context, r *model.UsageRecord) error {
	s.mu.Lock()
	s.records = append(s.records, r)
	s.mu.Unlock()
	return nil
}

func (s *tsUsageStore) GetPricingRule(_ context.Context, _, _, _ string) (*model.PricingRule, error) {
	return nil, gorm.ErrRecordNotFound
}

func (s *tsUsageStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// creditCounts snapshots the mockCreditService counters under its lock.
func creditCounts(m *mockCreditService) (reserve, refund, finalize int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reserveCalls, m.refundCalls, m.finalizeCalls
}

// eventually polls cond up to ~2s. Billing/ContextBudget finalize run in
// background goroutines that are not awaited by the stream's channel close, so
// the assertions must converge rather than read a single post-close snapshot.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}

// TestChainStream_RetryThenCrossProviderFailover_BillingCorrect is the end-to-end
// billing-correctness proof for Part B. It composes the production-shaped chain
//
//	Fallback → ContextBudgetCredits(Reserve) → Billing → Retry → adapter
//
// and drives the worst-case failover: the primary provider's stream stalls
// before the first content chunk on BOTH the initial attempt and the
// same-provider retry, then the call fails over to the same model on a different
// provider, which succeeds.
//
// The decisive assertion is reserveCalls == 2 (NOT 3): the same-provider retry
// re-invokes only the adapter (it sits BELOW ContextBudgetCredits), so it does
// NOT create a second reservation. Only the primary (refunded) and the
// alternate (reconciled) reserve. This is the structural guarantee that retry
// never double-charges.
func TestChainStream_RetryThenCrossProviderFailover_BillingCorrect(t *testing.T) {
	cap := profileCapability()
	primary := &registry.ResolvedRoute{
		TaskID: "agent.run", ServiceID: 24, ServiceKey: "deepseek-v4-pro",
		ServiceType: "llm", Provider: registry.ProviderInfo{ID: 3, Name: "aihubmix"}, Capability: cap,
	}
	alt := registry.ResolvedRoute{
		TaskID: "agent.run", ServiceID: 24, ServiceKey: "deepseek-v4-pro",
		ServiceType: "llm", Provider: registry.ProviderInfo{ID: 1, Name: "dmxapi"}, Capability: cap,
	}

	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{alt}}
	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult([]aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "go"}},
		}, true),
	}
	creditSvc := &mockCreditService{
		checkResult:   &credit.PreCheckResult{SkipDeduction: false, Sufficient: true, EstimatedCredits: 15},
		reserveResult: &credit.Reservation{ID: 101, ReservedCredits: 15, Status: credit.StatusReserved},
	}
	usageStore := &tsUsageStore{}

	deps := Deps{
		Resolver:      resolver,
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		UsageStore:    usageStore,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Innermost adapter: aihubmix stalls pre-content every call; dmxapi succeeds.
	var mu sync.Mutex
	adapterCalls := map[string]int{}
	adapter := Handler(func(_ context.Context, route *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		mu.Lock()
		adapterCalls[route.Provider.Name]++
		mu.Unlock()

		var chunks []aiservice.ChatChunk
		if route.Provider.Name == "aihubmix" {
			chunks = []aiservice.ChatChunk{idleErrChunk()}
		} else {
			chunks = successChunks("recovered")
		}
		ch := make(chan aiservice.ChatChunk, len(chunks)+1)
		for _, c := range chunks {
			ch <- c
		}
		close(ch)
		return (<-chan aiservice.ChatChunk)(ch), nil
	})

	// Production-shaped chain.
	chain := Chain(
		Fallback(deps),
		ContextBudgetCredits(deps),
		Billing(deps),
		retryWithPolicy(zeroDelayPolicy()),
	)
	handler := chain(adapter)

	req := chatReqWithFragments(simpleFragment("f1", "input"))
	ctx := billing.WithBillingMeta(context.Background(), 7, "agent_run", nil)
	ctx = WithUserID(ctx, 7)

	resp, err := handler(ctx, primary, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	// --- behaviour ---
	if concatDelta(got) != "recovered" {
		t.Errorf("expected failover content %q, got %q", "recovered", concatDelta(got))
	}
	if countTerminals(got) != 1 {
		t.Errorf("consumer must see exactly 1 terminal chunk, got %d", countTerminals(got))
	}
	for _, c := range got {
		if c.IsFinal && c.Err != nil {
			t.Errorf("failover succeeded but consumer saw an error terminal: %v", c.Err)
		}
	}

	// --- upstream call shape (adapter calls are all done once content arrived) ---
	mu.Lock()
	aih, dmx := adapterCalls["aihubmix"], adapterCalls["dmxapi"]
	mu.Unlock()
	if aih != 2 {
		t.Errorf("primary adapter must be hit twice (initial + same-provider retry), got %d", aih)
	}
	if dmx != 1 {
		t.Errorf("alternate adapter must be hit once, got %d", dmx)
	}
	if aih+dmx != 3 {
		t.Errorf("total upstream calls must be 3 (≤3 budget), got %d", aih+dmx)
	}

	// --- billing correctness (the whole point) — converge on the async finalize ---
	eventually(t, func() bool {
		r, rf, f := creditCounts(creditSvc)
		return r == 2 && rf == 1 && f == 1 && usageStore.count() == 2
	}, "billing did not settle to reserve=2 refund=1 finalize=1 records=2")

	r, rf, f := creditCounts(creditSvc)
	if r != 2 {
		t.Errorf("Reserve must be called exactly twice (primary + alternate), got %d. "+
			"A value of 3 would mean the same-provider retry wrongly created a second reservation.", r)
	}
	if rf != 1 {
		t.Errorf("primary reservation must be refunded exactly once, got %d", rf)
	}
	if f != 1 {
		t.Errorf("alternate reservation must be reconciled exactly once, got %d", f)
	}
	if usageStore.count() != 2 {
		t.Errorf("expected 2 usage records (primary failed + alternate success), got %d", usageStore.count())
	}
}

// TestChainStream_NormalStream_NoRetryNoFallback_BillingNormal is the AC9
// zero-regression proof through the FULL chain: a primary that streams content
// normally (the chatbot / SOP / salesrag common case) takes NO retry, NO
// failover, reserves once, reconciles once, and forwards the content unchanged.
// Guards against the streaming-aware Retry/Fallback wrappers altering the happy
// path for the non-agent callers that share this chain.
func TestChainStream_NormalStream_NoRetryNoFallback_BillingNormal(t *testing.T) {
	cap := profileCapability()
	primary := &registry.ResolvedRoute{
		TaskID: "chatbot.stream", ServiceID: 7, ServiceKey: "glm-4-7",
		ServiceType: "llm", Provider: registry.ProviderInfo{ID: 5, Name: "volc-ark"}, Capability: cap,
	}
	// Alternate exists in the registry but must never be used on the happy path.
	altUnused := registry.ResolvedRoute{TaskID: "chatbot.stream", ServiceID: 7, Provider: registry.ProviderInfo{ID: 1, Name: "dmxapi"}, Capability: cap}
	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{altUnused}}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult([]aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		}, true),
	}
	creditSvc := &mockCreditService{
		checkResult:   &credit.PreCheckResult{SkipDeduction: false, Sufficient: true, EstimatedCredits: 15},
		reserveResult: &credit.Reservation{ID: 202, ReservedCredits: 15, Status: credit.StatusReserved},
	}
	usageStore := &tsUsageStore{}
	deps := Deps{
		Resolver: resolver, ContextBudget: budgetSvc, CreditService: creditSvc,
		UsageStore: usageStore, Logger: &mockLogger{}, Clock: fixedClock{t: time.Now()},
	}

	var mu sync.Mutex
	calls := 0
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		chunks := []aiservice.ChatChunk{
			{Delta: "hello ", Index: 0},
			{Delta: "world", Index: 1},
			{IsFinal: true, FinishReason: "stop", Usage: &aiservice.TokenUsage{PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28}},
		}
		ch := make(chan aiservice.ChatChunk, len(chunks)+1)
		for _, c := range chunks {
			ch <- c
		}
		close(ch)
		return (<-chan aiservice.ChatChunk)(ch), nil
	})

	chain := Chain(Fallback(deps), ContextBudgetCredits(deps), Billing(deps), retryWithPolicy(zeroDelayPolicy()))
	req := chatReqWithFragments(simpleFragment("f1", "input"))
	ctx := billing.WithBillingMeta(context.Background(), 9, "chatbot_chat", nil)
	ctx = WithUserID(ctx, 9)

	resp, err := chain(adapter)(ctx, primary, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Errorf("happy path must make exactly 1 upstream call, got %d", gotCalls)
	}
	if concatDelta(got) != "hello world" {
		t.Errorf("content altered on happy path: got %q", concatDelta(got))
	}
	// Reserve is synchronous (pre-call); reconcile is async — converge on it.
	eventually(t, func() bool {
		r, rf, f := creditCounts(creditSvc)
		return r == 1 && rf == 0 && f == 1 && usageStore.count() == 1
	}, "happy-path billing did not settle to reserve=1 refund=0 finalize=1 records=1")

	if usageStore.count() != 1 {
		t.Errorf("happy path must write exactly 1 usage record (Billing not bypassed), got %d", usageStore.count())
	}
}
