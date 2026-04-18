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
