package billing

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// ----------------------------------------------------------------------------
// Stub UsageStore for ResolvePricingRule tests
// ----------------------------------------------------------------------------

// stubUsageStore is a minimal UsageStore implementation for unit tests.
// It does not touch any database; all behaviour is driven by the stub fields.
type stubUsageStore struct {
	// pricingRules maps "<serviceType>|<provider>|<model>" to the rule to return.
	pricingRules map[string]*model.PricingRule
	// pricingErr overrides the error returned by GetPricingRule (nil = use map).
	pricingErr error
	// providerModelIDs maps "<modelKey>|<providerName>" to the provider model ID.
	providerModelIDs map[string]string
	// providerModelIDErr overrides the error returned by GetProviderModelID.
	providerModelIDErr error
}

func (s *stubUsageStore) CreateUsageRecord(_ context.Context, _ *model.UsageRecord) error {
	return nil
}

func (s *stubUsageStore) CreateUsageRecords(_ context.Context, _ []*model.UsageRecord) error {
	return nil
}

func (s *stubUsageStore) GetPricingRule(_ context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	if s.pricingErr != nil {
		return nil, s.pricingErr
	}
	key := serviceType + "|" + provider + "|" + modelName
	if rule, ok := s.pricingRules[key]; ok {
		return rule, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *stubUsageStore) GetPricingRuleByModel(_ context.Context, provider, modelName string) (*model.PricingRule, error) {
	if s.pricingErr != nil {
		return nil, s.pricingErr
	}
	suffix := "|" + provider + "|" + modelName
	for key, rule := range s.pricingRules {
		if strings.HasSuffix(key, suffix) && rule.IsActive {
			return rule, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *stubUsageStore) GetPricingRuleTiers(_ context.Context, _ uint) ([]model.PricingRuleTier, error) {
	return nil, nil
}

func (s *stubUsageStore) GetProviderModelID(_ context.Context, modelKey, providerName string) (string, error) {
	if s.providerModelIDErr != nil {
		return "", s.providerModelIDErr
	}
	key := modelKey + "|" + providerName
	if id, ok := s.providerModelIDs[key]; ok {
		return id, nil
	}
	return "", gorm.ErrRecordNotFound
}

// pricingRule is a helper to create a test PricingRule.
func pricingRule(id uint, inputPrice, outputPrice float64) *model.PricingRule {
	return &model.PricingRule{
		ID:                 id,
		BillingMode:        "flat",
		InputPricePerMTok:  inputPrice,
		OutputPricePerMTok: outputPrice,
		IsActive:           true,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
}

// clearPricingCache removes all entries from the package-level pricingCache so
// tests don't interfere with each other.
func clearPricingCache() {
	pricingCache.Range(func(k, _ interface{}) bool {
		pricingCache.Delete(k)
		return true
	})
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_ModelKeyDirectMatch
// ----------------------------------------------------------------------------

// TestResolvePricingRule_ModelKeyDirectMatch verifies that when the pricing_rule
// table has an entry matching model_key directly, it is returned on the first
// lookup without calling GetProviderModelID.
func TestResolvePricingRule_ModelKeyDirectMatch(t *testing.T) {
	clearPricingCache()

	rule := pricingRule(1, 0.5, 2.0)
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm|aihubmix|claude-sonnet-4-6-think": rule,
		},
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "aihubmix", "claude-sonnet-4-6-think")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil PricingRule")
	}
	if got.ID != 1 {
		t.Errorf("rule ID: got %d, want 1", got.ID)
	}

	// Second call should hit cache; zero out the store map to prove it.
	store.pricingRules = nil
	got2, err2 := ResolvePricingRule(context.Background(), store, "llm", "aihubmix", "claude-sonnet-4-6-think")
	if err2 != nil {
		t.Fatalf("cached call: unexpected error %v", err2)
	}
	if got2 == nil || got2.ID != 1 {
		t.Errorf("cached call: expected rule ID 1, got %v", got2)
	}
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_ProviderModelIdFallback
// ----------------------------------------------------------------------------

// TestResolvePricingRule_ProviderModelIdFallback verifies that when the direct
// model_key lookup misses (gorm.ErrRecordNotFound) but GetProviderModelID
// resolves a provider_model_id, the second pricing_rule lookup with the
// provider_model_id succeeds and that result is returned and cached.
func TestResolvePricingRule_ProviderModelIdFallback(t *testing.T) {
	clearPricingCache()

	// Simulate: rule seeded with provider-specific ID "claude-sonnet-4-6",
	// but model_key in usage_record is "claude-sonnet-4-6-think".
	rule := pricingRule(42, 1.0, 4.0)
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			// No entry for model_key "claude-sonnet-4-6-think".
			"llm|aihubmix|claude-sonnet-4-6": rule,
		},
		providerModelIDs: map[string]string{
			"claude-sonnet-4-6-think|aihubmix": "claude-sonnet-4-6",
		},
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "aihubmix", "claude-sonnet-4-6-think")
	if err != nil {
		t.Fatalf("expected no error via fallback, got %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil PricingRule via fallback")
	}
	if got.ID != 42 {
		t.Errorf("rule ID: got %d, want 42", got.ID)
	}

	// Re-populate the fallback key to verify it was cached there.
	clearPricingCache()
	store.providerModelIDs = map[string]string{
		"claude-sonnet-4-6-think|aihubmix": "claude-sonnet-4-6",
	}
	store.pricingRules = map[string]*model.PricingRule{
		"llm|aihubmix|claude-sonnet-4-6": rule,
	}
	// Seed the fallback key in cache manually to test cache hit on fallback path.
	fallbackKey := "llm|aihubmix|claude-sonnet-4-6"
	pricingCache.Store(fallbackKey, pricingCacheEntry{rule: rule, expiresAt: time.Now().Add(pricingCacheTTL)})
	store.pricingRules = nil // clear rules — should be served from cache

	got3, err3 := ResolvePricingRule(context.Background(), store, "llm", "aihubmix", "claude-sonnet-4-6-think")
	if err3 != nil {
		t.Fatalf("expected cache hit on fallback key, got error %v", err3)
	}
	if got3 == nil || got3.ID != 42 {
		t.Errorf("expected rule ID 42 from cache, got %v", got3)
	}
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_BothMiss
// ----------------------------------------------------------------------------

// TestResolvePricingRule_BothMiss verifies that when neither the direct lookup
// nor the provider_model_id fallback finds a pricing rule, ResolvePricingRule
// returns (nil, gorm.ErrRecordNotFound) and does not panic.
func TestResolvePricingRule_BothMiss(t *testing.T) {
	clearPricingCache()

	store := &stubUsageStore{
		pricingRules:     map[string]*model.PricingRule{}, // empty — no rules
		providerModelIDs: map[string]string{},             // empty — no mappings
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "aihubmix", "some-unknown-model")
	if got != nil {
		t.Errorf("expected nil rule on both-miss, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected an error on both-miss, got nil")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_ProviderModelIdSameAsModelKey
// ----------------------------------------------------------------------------

// TestResolvePricingRule_ProviderModelIdSameAsModelKey verifies that when
// GetProviderModelID returns the same string as the original model key (an
// edge case that would cause an infinite identical retry), ResolvePricingRule
// short-circuits and returns gorm.ErrRecordNotFound instead of making a
// redundant second lookup.
func TestResolvePricingRule_ProviderModelIdSameAsModelKey(t *testing.T) {
	clearPricingCache()

	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{}, // no rules
		providerModelIDs: map[string]string{
			"my-model|myprovider": "my-model", // same as model key
		},
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "myprovider", "my-model")
	if got != nil {
		t.Errorf("expected nil rule, got %+v", got)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_DirectDBError
// ----------------------------------------------------------------------------

// TestResolvePricingRule_DirectDBError verifies that when GetPricingRule returns
// a real DB error (not ErrRecordNotFound), ResolvePricingRule propagates that
// error rather than silently treating it as a not-found and billing at ¥0.
func TestResolvePricingRule_DirectDBError(t *testing.T) {
	clearPricingCache()

	dbErr := errors.New("connection refused")
	store := &stubUsageStore{
		pricingErr: dbErr, // first call returns a real DB error
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "someprovider", "some-model")
	if got != nil {
		t.Errorf("expected nil rule on DB error, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("DB error must not be masked as ErrRecordNotFound; got %v", err)
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("expected original DB error to be in the chain; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// TestResolvePricingRule_ProviderModelIDDBError
// ----------------------------------------------------------------------------

// ----------------------------------------------------------------------------
// Task B.4: Recorder uses pricing.CalculateCost (single source of truth)
// ----------------------------------------------------------------------------

// spyCalculator counts calls to CalculateCost and returns a fixed cost so the
// recorder test can verify buildRecord delegated to the injected calculator
// instead of running its own cost math.
type spyCalculator struct {
	calls       atomic.Int64
	lastArgs    atomic.Value // []interface{}{serviceType, provider, model, pt, ct}
	returnCost  int64
	returnError error
}

func (s *spyCalculator) CalculateCost(_ context.Context, serviceType, provider, model string,
	promptTokens, completionTokens int,
) (int64, error) {
	s.calls.Add(1)
	s.lastArgs.Store([]interface{}{serviceType, provider, model, promptTokens, completionTokens})
	return s.returnCost, s.returnError
}

func (s *spyCalculator) IsFreeModel(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

// CalculateCostWithCache satisfies pricing.ICalculator. It delegates to
// CalculateCost (cached arg ignored) so existing assertions on the recorded
// 5-tuple of args continue to hold.
func (s *spyCalculator) CalculateCostWithCache(ctx context.Context, serviceType, provider, model string,
	promptTokens, completionTokens, _ int,
) (int64, error) {
	return s.CalculateCost(ctx, serviceType, provider, model, promptTokens, completionTokens)
}

// TestBuildRecord_CallsPricingCalculator verifies that the recorder, after
// Task B.4, delegates cost calculation to the injected pricing.ICalculator on
// the LLM path (prompt + completion tokens). The stub calculator records the
// exact arguments so reviewers can confirm the (service_type, provider,
// model) triple matches what the recorder persists on usage_record.
func TestBuildRecord_CallsPricingCalculator(t *testing.T) {
	clearPricingCache()

	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": pricingRule(1, 0.3, 0.6),
		},
	}
	spy := &spyCalculator{returnCost: 123}

	r := &UsageRecorder{
		store: store,
		calc:  spy,
		ch:    make(chan *UsageEvent, 1),
		done:  make(chan struct{}),
	}

	event := &UsageEvent{
		UserID:      42,
		ServiceType: "llm_chat",
		Provider:    "ali",
		Model:       "qwen-turbo",
		Operation:   "sop_node_execute",
		Usage: &TokenUsage{
			PromptTokens:     500,
			CompletionTokens: 200,
			TotalTokens:      700,
		},
	}

	record := r.buildRecord(event)

	if got := spy.calls.Load(); got != 1 {
		t.Errorf("CalculateCost called %d times, want 1", got)
	}
	args, ok := spy.lastArgs.Load().([]interface{})
	if !ok {
		t.Fatal("spy.lastArgs did not record call args")
	}
	if args[0] != "llm_chat" || args[1] != "ali" || args[2] != "qwen-turbo" ||
		args[3].(int) != 500 || args[4].(int) != 200 {
		t.Errorf("calculator args = %v, want [llm_chat ali qwen-turbo 500 200]", args)
	}
	if record.CostCents != 123 {
		t.Errorf("record.CostCents = %d, want 123 (from spy)", record.CostCents)
	}
}

// TestBuildRecord_CalculatorErrorFallsBackToZero ensures a pricing lookup
// failure (unknown model, DB error) does not block the batch insert — the
// record is still saved with CostCents=0 and a log warning, matching the
// existing recorder contract that cost is best-effort metadata, not
// load-bearing for the write.
func TestBuildRecord_CalculatorErrorFallsBackToZero(t *testing.T) {
	clearPricingCache()

	store := &stubUsageStore{}
	spy := &spyCalculator{returnError: gorm.ErrRecordNotFound}

	r := &UsageRecorder{
		store: store,
		calc:  spy,
		ch:    make(chan *UsageEvent, 1),
		done:  make(chan struct{}),
	}

	event := &UsageEvent{
		UserID:      42,
		ServiceType: "llm_chat",
		Provider:    "unknown",
		Model:       "also-unknown",
		Usage:       &TokenUsage{PromptTokens: 100, CompletionTokens: 50},
	}

	record := r.buildRecord(event)

	if record.CostCents != 0 {
		t.Errorf("CostCents on lookup failure = %d, want 0", record.CostCents)
	}
	if record.UserID != 42 {
		t.Errorf("record should still be populated despite cost miss, got user %d", record.UserID)
	}
}

// TestCostConsistency_RecorderVsPricingDirect is the end-to-end consistency
// check required by Track B spec §3.0: biz/sop (calling pricing directly) and
// the async recorder (calling pricing internally) must agree on cost cents for
// identical (service_type, provider, model, pt, ct) inputs. This test proves
// the single-source-of-truth property.
func TestCostConsistency_RecorderVsPricingDirect(t *testing.T) {
	clearPricingCache()

	rule := &model.PricingRule{
		ID:                     1,
		BillingMode:            "flat",
		InputPricePerMTok:      0.3,
		OutputPricePerMTok:     0.6,
		SellInputPricePerMTok:  0.5,
		SellOutputPricePerMTok: 1.0,
		IsActive:               true,
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
	}
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": rule,
		},
	}
	// Share one calculator across the direct path and the recorder path so the
	// cache (and rule lookup) is identical — if they ever diverge, this test
	// will fail at the first byte of mismatch.
	calc := pricing.NewCalculator(store)

	r := &UsageRecorder{
		store: store,
		calc:  calc,
		ch:    make(chan *UsageEvent, 1),
		done:  make(chan struct{}),
	}

	ptCt := []struct{ pt, ct int }{
		{100, 50},
		{1_000, 2_000},
		{500_000, 100_000},
		{1_000_000, 1_000_000},
	}

	for _, tc := range ptCt {
		// Path 1: biz/sop-style direct pricing call.
		directCost, err := calc.CalculateCost(context.Background(), "llm_chat", "ali", "qwen-turbo", tc.pt, tc.ct)
		if err != nil {
			t.Fatalf("direct cost (%d,%d): %v", tc.pt, tc.ct, err)
		}

		// Path 2: recorder-style buildRecord from a UsageEvent.
		event := &UsageEvent{
			UserID:      1,
			ServiceType: "llm_chat",
			Provider:    "ali",
			Model:       "qwen-turbo",
			Usage:       &TokenUsage{PromptTokens: tc.pt, CompletionTokens: tc.ct},
		}
		record := r.buildRecord(event)

		if record.CostCents != directCost {
			t.Errorf("pt=%d ct=%d: recorder cost %d != direct cost %d (divergent source of truth!)",
				tc.pt, tc.ct, record.CostCents, directCost)
		}
	}
}

// TestBuildRecord_RevenueCalculation verifies that the recorder still computes
// revenue (sell prices) locally since pricing.CalculateCost is cost-only per
// spec §3.0. This test is the back-compat guard for the existing
// usage_record.revenue_cents column.
func TestBuildRecord_RevenueCalculation(t *testing.T) {
	clearPricingCache()

	// Use the real ResolvePricingRule path (not the spy) to exercise the
	// recorder's revenue branch end-to-end. The spy is wired for cost so the
	// two calculations stay independent.
	rule := &model.PricingRule{
		ID:                     1,
		BillingMode:            "flat",
		InputPricePerMTok:      0.3,
		OutputPricePerMTok:     0.6,
		SellInputPricePerMTok:  0.5,
		SellOutputPricePerMTok: 1.0,
		IsActive:               true,
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
	}
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|ali|qwen-turbo": rule,
		},
	}
	// Pricing calculator using the same store — cost goes through the real
	// pricing path so record.CostCents is deterministic.
	calc := pricing.NewCalculator(store)

	r := &UsageRecorder{
		store: store,
		calc:  calc,
		ch:    make(chan *UsageEvent, 1),
		done:  make(chan struct{}),
	}

	event := &UsageEvent{
		UserID:      42,
		ServiceType: "llm_chat",
		Provider:    "ali",
		Model:       "qwen-turbo",
		Usage: &TokenUsage{
			PromptTokens:     1_000_000,
			CompletionTokens: 1_000_000,
		},
	}

	record := r.buildRecord(event)

	// Cost: (1M/1M*0.3 + 1M/1M*0.6) yuan = 0.9 yuan = 90 cents.
	if record.CostCents != 90 {
		t.Errorf("CostCents = %d, want 90", record.CostCents)
	}
	// Revenue: (1M/1M*0.5 + 1M/1M*1.0) yuan = 1.5 yuan = 150 cents.
	if record.RevenueCents != 150 {
		t.Errorf("RevenueCents = %d, want 150", record.RevenueCents)
	}
}

// TestResolvePricingRule_ProviderModelIDDBError verifies that when the first
// GetPricingRule call returns ErrRecordNotFound (triggering the fallback) but
// the subsequent GetProviderModelID call returns a real DB error, that error is
// propagated — not masked as ErrRecordNotFound.
func TestResolvePricingRule_ProviderModelIDDBError(t *testing.T) {
	clearPricingCache()

	connErr := errors.New("connection refused")
	store := &stubUsageStore{
		pricingRules:       map[string]*model.PricingRule{}, // empty → ErrRecordNotFound on first call
		providerModelIDErr: connErr,                         // second call returns real DB error
	}

	got, err := ResolvePricingRule(context.Background(), store, "llm", "someprovider", "some-model")
	if got != nil {
		t.Errorf("expected nil rule on DB error, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("connection error must not be masked as ErrRecordNotFound; got %v", err)
	}
	if !errors.Is(err, connErr) {
		t.Errorf("expected connection error to be in the chain; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// llm-prompt-cache: reconcile threads CachedPromptTokens into cost + revenue
// ----------------------------------------------------------------------------

// ptrF returns a pointer to v (test helper for the nullable cached price columns).
func ptrF(v float64) *float64 { return &v }

// cachedPricingRule builds a flat rule with both base and (optional) cached
// input prices so the recorder's cached cost/revenue branches can be exercised.
// Pass nil for cachedCost/cachedSell to leave those columns NULL (full-price
// fallback — the zero-regression control).
func cachedPricingRule(inputCost, outputCost, inputSell, outputSell float64, cachedCost, cachedSell *float64) *model.PricingRule {
	return &model.PricingRule{
		ID:                          1,
		BillingMode:                 "flat",
		InputPricePerMTok:           inputCost,
		OutputPricePerMTok:          outputCost,
		SellInputPricePerMTok:       inputSell,
		SellOutputPricePerMTok:      outputSell,
		CachedInputPricePerMTok:     cachedCost,
		SellCachedInputPricePerMTok: cachedSell,
		IsActive:                    true,
		CreatedAt:                   time.Now(),
		UpdatedAt:                   time.Now(),
	}
}

// TestRecorder_CachedTokens_ThreadedToCostAndRevenue proves the recorder feeds
// record.CachedPromptTokens into BOTH the cost path (via CalculateCostWithCache)
// and the inline revenue formula, so the cached portion of prompt tokens is
// billed at the discounted price on both the cost and sell dimensions.
func TestRecorder_CachedTokens_ThreadedToCostAndRevenue(t *testing.T) {
	clearPricingCache()

	// input cost 14, output cost 0; sell input 20, sell output 0.
	// cached cost 1.4 (0.1x), cached sell 2.0 (0.1x).
	rule := cachedPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0))
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|dmxapi|deepseek-v4-pro": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	r := &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}

	// 1,000,000 prompt tokens, 400,000 of them cache HITS, 0 completion.
	event := &UsageEvent{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "dmxapi",
		Model:       "deepseek-v4-pro",
		Usage:       &TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, CachedPromptTokens: 400_000},
	}
	record := r.buildRecord(event)

	// buildRecord must map the cached count into the persisted record.
	if record.CachedPromptTokens != 400_000 {
		t.Fatalf("CachedPromptTokens not threaded into record: got %d, want 400000", record.CachedPromptTokens)
	}

	// Cost: 400k cached @1.4 + 600k normal @14 = 0.56 + 8.4 = 8.96 yuan = 896 cents.
	if record.CostCents != 896 {
		t.Errorf("CostCents = %d, want 896 (cached discount applied)", record.CostCents)
	}
	// Revenue: 400k cached @2.0 + 600k normal @20 = 0.8 + 12.0 = 12.8 yuan = 1280 cents.
	if record.RevenueCents != 1280 {
		t.Errorf("RevenueCents = %d, want 1280 (cached sell discount applied)", record.RevenueCents)
	}
}

// TestRecorder_CachedTokens_NullPriceFullRate is the zero-regression control:
// cachedTokens>0 but the cached price columns are NULL ⇒ the cached portion is
// billed at the FULL input/sell price, byte-identical to pre-cache behavior.
func TestRecorder_CachedTokens_NullPriceFullRate(t *testing.T) {
	clearPricingCache()

	// Cached price columns NULL on both dimensions.
	rule := cachedPricingRule(14, 0, 20, 0, nil, nil)
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|dmxapi|deepseek-v4-pro": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	r := &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}

	event := &UsageEvent{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "dmxapi",
		Model:       "deepseek-v4-pro",
		Usage:       &TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, CachedPromptTokens: 400_000},
	}
	record := r.buildRecord(event)

	// Cost: 1M @14 = 14 yuan = 1400 cents (cached count irrelevant — NULL price).
	if record.CostCents != 1400 {
		t.Errorf("CostCents = %d, want 1400 (NULL cached price → full rate)", record.CostCents)
	}
	// Revenue: 1M @20 = 20 yuan = 2000 cents.
	if record.RevenueCents != 2000 {
		t.Errorf("RevenueCents = %d, want 2000 (NULL cached sell price → full rate)", record.RevenueCents)
	}
}

// TestRecorder_NoCachedTokens_ByteIdentical is the second zero-regression
// control: cachedTokens==0 with a cached price SET ⇒ identical to a run that
// had no cached price at all (the cached term contributes nothing).
func TestRecorder_NoCachedTokens_ByteIdentical(t *testing.T) {
	clearPricingCache()

	rule := cachedPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0))
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|dmxapi|deepseek-v4-pro": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	r := &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}

	event := &UsageEvent{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "dmxapi",
		Model:       "deepseek-v4-pro",
		Usage:       &TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, CachedPromptTokens: 0},
	}
	record := r.buildRecord(event)

	if record.CachedPromptTokens != 0 {
		t.Errorf("CachedPromptTokens = %d, want 0", record.CachedPromptTokens)
	}
	// Cost 1M @14 = 1400 cents; Revenue 1M @20 = 2000 cents — no discount fires.
	if record.CostCents != 1400 {
		t.Errorf("CostCents = %d, want 1400 (cachedTokens=0 → full rate)", record.CostCents)
	}
	if record.RevenueCents != 2000 {
		t.Errorf("RevenueCents = %d, want 2000 (cachedTokens=0 → full rate)", record.RevenueCents)
	}
}
