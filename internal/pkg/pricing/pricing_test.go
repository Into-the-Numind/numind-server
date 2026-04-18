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
		"llm_chat|ali|qwen-turbo":               flatRule(1, 0.3, 0.6, 0.5, 1.0),
		"llm_chat|ali|qwen-plus":                flatRule(2, 0.8, 2.0, 1.5, 3.0),
		"llm_chat|volc|deepseek-v3-2-251201":    flatRule(3, 2.0, 8.0, 3.0, 10.0),
		"llm_chat|dmxapi|qwen-turbo-latest":     flatRule(4, 0.5, 1.0, 0.8, 1.5),
		"embedding|ali|text-embedding-v4":       flatRule(5, 0.07, 0, 0.1, 0),
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
	// 10k/1M*1.0 + 2k/1M*4.0 = 0.01 + 0.008 = 0.018 yuan = 1.8 cents → rounds to 2.
	if cost != 2 {
		t.Errorf("tier 1 cost = %d, want 2", cost)
	}

	// Case 2: prompt in tier 2 bracket (100k tokens).
	cost, err = calc.CalculateCost(context.Background(), "llm_chat", "gemini", "gemini-2.5-flash",
		100_000, 20_000)
	if err != nil {
		t.Fatalf("tier 2: unexpected error %v", err)
	}
	// 100k/1M*2.5 + 20k/1M*10.0 = 0.25 + 0.2 = 0.45 yuan = 45 cents.
	if cost != 45 {
		t.Errorf("tier 2 cost = %d, want 45", cost)
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
