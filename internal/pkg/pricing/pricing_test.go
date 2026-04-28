package pricing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Stub PricingStore for unit tests — no database, fully deterministic.
// ----------------------------------------------------------------------------

// stubPricingStore is an in-memory PricingStore implementation for tests.
// Call counts are tracked via atomic counters so cache tests can assert that
// repeated lookups hit the cache (DB call count stays stable).
type stubPricingStore struct {
	// pricingRules is keyed by "<serviceType>|<provider>|<model>".
	pricingRules map[string]*model.PricingRule
	// pricingErr overrides the error returned by GetPricingRule (nil = use map).
	pricingErr error

	// pricingRuleTiers is keyed by rule ID.
	pricingRuleTiers map[uint][]model.PricingRuleTier

	// providerModelIDs is keyed by "<modelKey>|<providerName>".
	providerModelIDs map[string]string
	// providerModelIDErr overrides the error returned by GetProviderModelID.
	providerModelIDErr error

	// Call counters (atomic so concurrent tests are safe).
	getPricingRuleCalls      atomic.Int64
	getProviderModelIDCalls  atomic.Int64
	getPricingRuleTiersCalls atomic.Int64
}

func (s *stubPricingStore) GetPricingRule(_ context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	s.getPricingRuleCalls.Add(1)
	if s.pricingErr != nil {
		return nil, s.pricingErr
	}
	key := serviceType + "|" + provider + "|" + modelName
	if rule, ok := s.pricingRules[key]; ok {
		return rule, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *stubPricingStore) GetPricingRuleTiers(_ context.Context, ruleID uint) ([]model.PricingRuleTier, error) {
	s.getPricingRuleTiersCalls.Add(1)
	if tiers, ok := s.pricingRuleTiers[ruleID]; ok {
		return tiers, nil
	}
	return nil, nil
}

func (s *stubPricingStore) GetProviderModelID(_ context.Context, modelKey, providerName string) (string, error) {
	s.getProviderModelIDCalls.Add(1)
	if s.providerModelIDErr != nil {
		return "", s.providerModelIDErr
	}
	key := modelKey + "|" + providerName
	if id, ok := s.providerModelIDs[key]; ok {
		return id, nil
	}
	return "", gorm.ErrRecordNotFound
}

// flatRule builds a flat (non-tiered) PricingRule for tests.
func flatRule(id uint, inputPrice, outputPrice, sellIn, sellOut float64) *model.PricingRule {
	return &model.PricingRule{
		ID:                     id,
		BillingMode:            "flat",
		InputPricePerMTok:      inputPrice,
		OutputPricePerMTok:     outputPrice,
		SellInputPricePerMTok:  sellIn,
		SellOutputPricePerMTok: sellOut,
		CreditMultiplier:       1.0,
		IsActive:               true,
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
	}
}

// ----------------------------------------------------------------------------
// Task B.1: NewCalculator constructor
// ----------------------------------------------------------------------------

// TestNewCalculator_ReturnsNonNil verifies the constructor returns a working
// ICalculator backed by the given store (smoke test for Task B.1 skeleton).
func TestNewCalculator_ReturnsNonNil(t *testing.T) {
	store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{}}
	calc := NewCalculator(store)
	if calc == nil {
		t.Fatal("NewCalculator returned nil")
	}
	// Verify CalculateCost is callable on the returned interface (will return
	// a typed not-found error with empty store — covered by Task B.2 tests).
	_, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 0, 0)
	if err == nil {
		t.Error("expected error from empty store, got nil")
	}
}

// ----------------------------------------------------------------------------
// Task B.2: CalculateCost — formula + table-driven coverage
// ----------------------------------------------------------------------------

// TestCalculateCost_QwenTurboBaseline is the canonical happy path: the plan
// literally specifies this input/output pair. Input price 0.3¥/Mtok,
// output 0.6¥/Mtok, 100 prompt + 50 completion tokens ==>
// 100/1e6*0.3 + 50/1e6*0.6 = 0.00003 + 0.00003 = 0.00006 yuan = 0.006 cents
// → rounded to 0 cents. Use larger token counts to get a measurable value.
func TestCalculateCost_QwenTurboBaseline(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
		},
	}
	calc := NewCalculator(store)

	// Use 1M prompt + 1M completion tokens so the ¥ math maps cleanly to cents.
	// Expected: (1_000_000/1e6*0.3 + 1_000_000/1e6*0.6) yuan = 0.9 yuan = 90 cents.
	cost, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo",
		1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const want = int64(90)
	if cost != want {
		t.Errorf("cost = %d cents, want %d", cost, want)
	}
}

// TestCalculateCost_TableDriven exercises the flat-billing formula across a
// range of providers, models, and token volumes plus the zero-token and
// unknown-model edge cases.
func TestCalculateCost_TableDriven(t *testing.T) {
	// Fixture: rule set covering ali/volc/dmxapi + an embedding rule (used for
	// unknown-model tests so the resolver path is deterministic).
	rules := map[string]*model.PricingRule{
		// Flat-rate LLM rules with clean numbers so hand-calculated expected
		// costs are obvious from the formula.
		"llm_chat|ali|qwen-turbo":            flatRule(1, 0.3, 0.6, 0.5, 1.0),
		"llm_chat|ali|qwen-plus":             flatRule(2, 0.8, 2.0, 1.5, 3.0),
		"llm_chat|volc|deepseek-v3-2-251201": flatRule(3, 2.0, 8.0, 3.0, 10.0),
		"llm_chat|dmxapi|qwen-turbo-latest":  flatRule(4, 0.5, 1.0, 0.8, 1.5),
		"embedding|ali|text-embedding-v4":    flatRule(5, 0.07, 0, 0.1, 0),
	}

	// Helper to build a fresh calculator per subtest so cache state does not
	// leak across cases (clean separation vs. relying on LRU eviction).
	newCalc := func() ICalculator {
		store := &stubPricingStore{pricingRules: rules}
		return NewCalculator(store)
	}

	tests := []struct {
		name             string
		serviceType      string
		provider         string
		model            string
		promptTokens     int
		completionTokens int
		wantCost         int64
		wantErrIs        error // nil => no error expected
	}{
		{
			name:             "qwen-turbo 1M+1M tokens",
			serviceType:      "llm_chat",
			provider:         "ali",
			model:            "qwen-turbo",
			promptTokens:     1_000_000,
			completionTokens: 1_000_000,
			wantCost:         90, // (0.3+0.6)*100 = 90 cents
		},
		{
			name:             "qwen-plus small conversation",
			serviceType:      "llm_chat",
			provider:         "ali",
			model:            "qwen-plus",
			promptTokens:     500_000,
			completionTokens: 100_000,
			// 500k/1M*0.8 + 100k/1M*2.0 = 0.4 + 0.2 = 0.6¥ = 60 cents
			wantCost: 60,
		},
		{
			name:             "deepseek large prompt",
			serviceType:      "llm_chat",
			provider:         "volc",
			model:            "deepseek-v3-2-251201",
			promptTokens:     2_000_000,
			completionTokens: 500_000,
			// 2*2.0 + 0.5*8.0 = 4 + 4 = 8¥ = 800 cents
			wantCost: 800,
		},
		{
			name:             "dmxapi short output",
			serviceType:      "llm_chat",
			provider:         "dmxapi",
			model:            "qwen-turbo-latest",
			promptTokens:     1_000_000,
			completionTokens: 0,
			// 1*0.5 + 0 = 0.5¥ = 50 cents
			wantCost: 50,
		},
		{
			name:             "embedding only input tokens",
			serviceType:      "embedding",
			provider:         "ali",
			model:            "text-embedding-v4",
			promptTokens:     1_000_000,
			completionTokens: 0,
			// 1*0.07 = 0.07¥ = 7 cents
			wantCost: 7,
		},
		{
			name:             "zero tokens returns zero cost with no error",
			serviceType:      "llm_chat",
			provider:         "ali",
			model:            "qwen-turbo",
			promptTokens:     0,
			completionTokens: 0,
			wantCost:         0,
			wantErrIs:        nil,
		},
		{
			name:             "very large token count survives float precision",
			serviceType:      "llm_chat",
			provider:         "ali",
			model:            "qwen-plus",
			promptTokens:     100_000_000, // 100M tokens
			completionTokens: 10_000_000,  // 10M tokens
			// 100*0.8 + 10*2.0 = 80 + 20 = 100¥ = 10000 cents
			wantCost: 10000,
		},
		{
			name:             "unknown model returns ErrRecordNotFound",
			serviceType:      "llm_chat",
			provider:         "ali",
			model:            "nonexistent-model-xyz",
			promptTokens:     1000,
			completionTokens: 500,
			wantErrIs:        gorm.ErrRecordNotFound,
		},
		{
			name:             "unknown provider returns ErrRecordNotFound",
			serviceType:      "llm_chat",
			provider:         "nowhere",
			model:            "some-model",
			promptTokens:     1000,
			completionTokens: 500,
			wantErrIs:        gorm.ErrRecordNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calc := newCalc()
			cost, err := calc.CalculateCost(context.Background(), tc.serviceType, tc.provider,
				tc.model, tc.promptTokens, tc.completionTokens)

			if tc.wantErrIs != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil (cost=%d)", tc.wantErrIs, cost)
				}
				if !gormErrIs(err, tc.wantErrIs) {
					t.Errorf("error chain: got %v, want wrapping %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cost != tc.wantCost {
				t.Errorf("cost = %d cents, want %d", cost, tc.wantCost)
			}
		})
	}
}

// gormErrIs is a local errors.Is shim — tests use it instead of importing
// errors directly so the imports stay narrow (we only need gorm.ErrRecordNotFound).
func gormErrIs(err, target error) bool {
	if err == target { //nolint:errorlint // intentional pointer identity check
		return true
	}
	type unwrapper interface{ Unwrap() error }
	for {
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
		if err == target { //nolint:errorlint
			return true
		}
	}
}

// TestCalculateCost_ProviderModelIDFallback verifies the two-step resolver:
// when a model_key has no direct pricing row, GetProviderModelID is consulted
// and the pricing is looked up under the canonical provider_model_id.
func TestCalculateCost_ProviderModelIDFallback(t *testing.T) {
	rule := flatRule(42, 1.0, 4.0, 1.5, 5.0)
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			// Only the canonical ID has a pricing row — the alias "...-think"
			// does not.
			"llm_chat|aihubmix|claude-sonnet-4-6": rule,
		},
		providerModelIDs: map[string]string{
			"claude-sonnet-4-6-think|aihubmix": "claude-sonnet-4-6",
		},
	}

	calc := NewCalculator(store)
	cost, err := calc.CalculateCost(context.Background(), "llm_chat", "aihubmix",
		"claude-sonnet-4-6-think", 1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("expected fallback hit, got error %v", err)
	}
	const want = int64(500) // (1.0+4.0)*100 = 500 cents
	if cost != want {
		t.Errorf("cost = %d, want %d", cost, want)
	}

	if got := store.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("expected 2 GetPricingRule calls (direct miss + fallback hit), got %d", got)
	}
	if got := store.getProviderModelIDCalls.Load(); got != 1 {
		t.Errorf("expected 1 GetProviderModelID call, got %d", got)
	}
}

// TestCalculateCost_TieredBilling exercises the tiered_token path, ensuring
// the rule's tier sub-rows are consulted and the bracket selected by prompt
// tokens applies to both input and output pricing (current policy per
// recorder.go:calculateTieredCost).
func TestCalculateCost_TieredBilling(t *testing.T) {
	rule := &model.PricingRule{
		ID:          99,
		BillingMode: "tiered_token",
		IsActive:    true,
	}
	maxTier1 := uint(32_000)
	tiers := []model.PricingRuleTier{
		// Tier 1: prompt <= 32k tokens
		{RuleID: 99, TokenType: "input", MinTokens: 0, MaxTokens: &maxTier1, CostPerMTok: 1.0, SellPerMTok: 1.5},
		{RuleID: 99, TokenType: "output", MinTokens: 0, MaxTokens: &maxTier1, CostPerMTok: 4.0, SellPerMTok: 6.0},
		// Tier 2: prompt > 32k, no upper bound
		{RuleID: 99, TokenType: "input", MinTokens: 32_001, MaxTokens: nil, CostPerMTok: 2.5, SellPerMTok: 3.5},
		{RuleID: 99, TokenType: "output", MinTokens: 32_001, MaxTokens: nil, CostPerMTok: 10.0, SellPerMTok: 15.0},
	}
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|gemini|gemini-2.5-flash": rule,
		},
		pricingRuleTiers: map[uint][]model.PricingRuleTier{
			99: tiers,
		},
	}

	calc := NewCalculator(store)

	// Case 1: prompt within tier 1 bracket (10k tokens).
	cost, err := calc.CalculateCost(context.Background(), "llm_chat", "gemini", "gemini-2.5-flash",
		10_000, 2_000)
	if err != nil {
		t.Fatalf("tier 1: unexpected error %v", err)
	}
	// sell: 10k/1M*1.5 + 2k/1M*6.0 = 0.015 + 0.012 = 0.027 yuan = 2.7 cents → rounds to 3.
	if cost != 3 {
		t.Errorf("tier 1 cost = %d, want 3", cost)
	}

	// Case 2: prompt in tier 2 bracket (100k tokens).
	cost, err = calc.CalculateCost(context.Background(), "llm_chat", "gemini", "gemini-2.5-flash",
		100_000, 20_000)
	if err != nil {
		t.Fatalf("tier 2: unexpected error %v", err)
	}
	// sell: 100k/1M*3.5 + 20k/1M*15.0 = 0.35 + 0.3 = 0.65 yuan = 65 cents.
	if cost != 65 {
		t.Errorf("tier 2 cost = %d, want 65", cost)
	}
}

// ----------------------------------------------------------------------------
// Task B.3: LRU cache — hit behaviour, TTL, and pubsub invalidation
// ----------------------------------------------------------------------------

// TestCache_RepeatedLookupsHitCache verifies that three calls to CalculateCost
// for the same (serviceType, provider, model) triple result in only one DB
// round-trip via GetPricingRule — the subsequent two serve from LRU.
func TestCache_RepeatedLookupsHitCache(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
		},
	}
	calc := NewCalculator(store)

	for i := 0; i < 3; i++ {
		_, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo",
			1_000_000, 500_000)
		if err != nil {
			t.Fatalf("call %d: unexpected error %v", i+1, err)
		}
	}

	if got := store.getPricingRuleCalls.Load(); got != 1 {
		t.Errorf("GetPricingRule called %d times, want 1 (cache should absorb calls 2 and 3)", got)
	}
}

// TestCache_TTLExpiry verifies that entries older than the configured TTL are
// treated as expired and trigger a fresh DB lookup on next access. The cache
// here is constructed directly with a short TTL so the test does not need to
// sleep for the production 5-minute TTL.
func TestCache_TTLExpiry(t *testing.T) {
	rc := newRuleCache(10, 20*time.Millisecond)
	rule := flatRule(1, 0.1, 0.2, 0.15, 0.3)
	rc.Put("k1", rule)

	if _, ok := rc.Get("k1"); !ok {
		t.Fatal("Get immediately after Put should hit cache")
	}

	time.Sleep(30 * time.Millisecond) // exceed TTL

	if _, ok := rc.Get("k1"); ok {
		t.Error("Get after TTL should miss cache")
	}
}

// TestCache_TTLExpiryThroughCalculator exercises the TTL path end-to-end: a
// fresh Calculator observes the second call hitting the DB again once the
// cache has expired.
func TestCache_TTLExpiryThroughCalculator(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
		},
	}

	// Build a calculator with a 15ms TTL cache by injecting ruleCache manually
	// via the unexported field. This is intentional — we don't expose a
	// test-configurable TTL on the public API to avoid polluting production
	// callers with a parameter they never need.
	calc := &calculator{
		store: store,
		cache: newRuleCache(10, 15*time.Millisecond),
	}
	registerCache(calc.cache)
	defer unregisterCache(calc.cache)

	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	// Two immediate calls → 1 DB hit.
	if got := store.getPricingRuleCalls.Load(); got != 1 {
		t.Errorf("before expiry: GetPricingRule calls = %d, want 1", got)
	}

	time.Sleep(20 * time.Millisecond)
	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	// After TTL: cache entry evicted, DB re-hit.
	if got := store.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("after expiry: GetPricingRule calls = %d, want 2", got)
	}
}

// TestInvalidateCache_EvictsEntry exercises the pubsub-driven invalidation
// contract: after a rule is cached, InvalidateCache for the same triple
// removes the entry and the next CalculateCost re-queries the DB.
func TestInvalidateCache_EvictsEntry(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
		},
	}
	calc := NewCalculator(store)

	// Warm cache.
	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	// Second call served from cache.
	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	if got := store.getPricingRuleCalls.Load(); got != 1 {
		t.Fatalf("warmed cache: calls = %d, want 1", got)
	}

	// Evict exactly this triple.
	InvalidateCache("llm_chat", "ali", "qwen-turbo")

	if _, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 1_000, 500); err != nil {
		t.Fatal(err)
	}
	if got := store.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("after InvalidateCache: calls = %d, want 2", got)
	}
}

// TestInvalidateCache_OnlyEvictsNamedKey verifies that invalidating one triple
// does not disturb cached entries for other triples (operator-edit precision).
func TestInvalidateCache_OnlyEvictsNamedKey(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
			"llm_chat|ali|qwen-plus":  flatRule(2, 0.8, 2.0, 1.5, 3.0),
		},
	}
	calc := NewCalculator(store)

	// Warm both entries.
	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-plus", 100, 50)
	before := store.getPricingRuleCalls.Load()
	if before != 2 {
		t.Fatalf("warm cache: calls = %d, want 2", before)
	}

	// Invalidate only qwen-turbo; qwen-plus should still be cached.
	InvalidateCache("llm_chat", "ali", "qwen-turbo")

	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-plus", 100, 50)
	if got := store.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("qwen-plus should have stayed cached: calls = %d, want 2", got)
	}

	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	if got := store.getPricingRuleCalls.Load(); got != 3 {
		t.Errorf("qwen-turbo should have re-fetched: calls = %d, want 3", got)
	}
}

// TestInvalidateCache_FanoutAcrossCalculators exercises the global registry:
// two Calculators share the same invalidation fan-out, so admin CRUD fires
// once and every replica / instance evicts simultaneously.
func TestInvalidateCache_FanoutAcrossCalculators(t *testing.T) {
	store1 := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
		},
	}
	store2 := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(2, 0.3, 0.6, 0.5, 1.0),
		},
	}

	calc1 := NewCalculator(store1)
	calc2 := NewCalculator(store2)

	// Warm both caches.
	_, _ = calc1.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	_, _ = calc2.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)

	InvalidateCache("llm_chat", "ali", "qwen-turbo")

	_, _ = calc1.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	_, _ = calc2.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)

	if got := store1.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("store1 calls = %d, want 2 (warm + re-fetch after invalidate)", got)
	}
	if got := store2.getPricingRuleCalls.Load(); got != 2 {
		t.Errorf("store2 calls = %d, want 2 (warm + re-fetch after invalidate)", got)
	}
}

// TestInvalidateCache_UnknownKeyIsNoop ensures the API is a safe no-op when
// called for a triple that was never cached (callers don't have to check).
func TestInvalidateCache_UnknownKeyIsNoop(t *testing.T) {
	// No panic, no error path — just call it.
	InvalidateCache("llm_chat", "ghost", "nowhere")
}

// TestPurgeAllCaches drops every cached entry across every calculator. Used
// for admin-wide pricing reload (e.g. bulk CSV import).
func TestPurgeAllCaches(t *testing.T) {
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": flatRule(1, 0.3, 0.6, 0.5, 1.0),
			"llm_chat|ali|qwen-plus":  flatRule(2, 0.8, 2.0, 1.5, 3.0),
		},
	}
	calc := NewCalculator(store)
	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-plus", 100, 50)
	if got := store.getPricingRuleCalls.Load(); got != 2 {
		t.Fatalf("warm: calls = %d, want 2", got)
	}

	PurgeAllCaches()

	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", 100, 50)
	_, _ = calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-plus", 100, 50)
	if got := store.getPricingRuleCalls.Load(); got != 4 {
		t.Errorf("after purge: calls = %d, want 4", got)
	}
}

// TestCalculateCost_TieredBillingMissingTiers confirms that a tiered rule with
// no tier sub-rows returns an error (data integrity violation) rather than
// silently billing ¥0.
func TestCalculateCost_TieredBillingMissingTiers(t *testing.T) {
	rule := &model.PricingRule{
		ID:          77,
		BillingMode: "tiered_token",
		IsActive:    true,
	}
	store := &stubPricingStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|broken|model": rule,
		},
		// No tiers configured for rule 77.
	}

	calc := NewCalculator(store)
	_, err := calc.CalculateCost(context.Background(), "llm_chat", "broken", "model", 1000, 500)
	if err == nil {
		t.Fatal("expected error for tiered rule without tiers, got nil")
	}
}

// TestCalculateCost_CreditMultiplier verifies that CreditMultiplier scales the
// credits charged to users independently of the raw cost_yuan computation.
func TestCalculateCost_CreditMultiplier(t *testing.T) {
	baseRule := func(multiplier float64) *model.PricingRule {
		r := flatRule(1, 1.0, 2.0, 0.0, 0.0) // inputPrice=1 yuan/MTok, outputPrice=2 yuan/MTok
		r.CreditMultiplier = multiplier
		return r
	}

	// 10k prompt + 5k completion, multiplier=1.0 (baseline)
	// cost = 10k/1M*1.0 + 5k/1M*2.0 = 0.01 + 0.01 = 0.02 yuan = 2 cents
	s := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|test|model": baseRule(1.0),
	}}
	calc := NewCalculator(s)
	cost, err := calc.CalculateCost(context.Background(), "llm_chat", "test", "model", 10_000, 5_000)
	if err != nil {
		t.Fatalf("multiplier=1.0: unexpected error %v", err)
	}
	if cost != 2 {
		t.Errorf("multiplier=1.0: cost = %d, want 2", cost)
	}

	// Same tokens, multiplier=0.5 → credits charged should be 1 cent (halved).
	s2 := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|test|model": baseRule(0.5),
	}}
	calc2 := NewCalculator(s2)
	cost2, err := calc2.CalculateCost(context.Background(), "llm_chat", "test", "model", 10_000, 5_000)
	if err != nil {
		t.Fatalf("multiplier=0.5: unexpected error %v", err)
	}
	if cost2 != 1 {
		t.Errorf("multiplier=0.5: cost = %d, want 1", cost2)
	}

	// multiplier=0 should fall back to 1.0 (guard against misconfiguration).
	s3 := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|test|model": baseRule(0),
	}}
	calc3 := NewCalculator(s3)
	cost3, err := calc3.CalculateCost(context.Background(), "llm_chat", "test", "model", 10_000, 5_000)
	if err != nil {
		t.Fatalf("multiplier=0 (fallback): unexpected error %v", err)
	}
	if cost3 != 2 {
		t.Errorf("multiplier=0 (fallback): cost = %d, want 2 (should behave as 1.0)", cost3)
	}
}
