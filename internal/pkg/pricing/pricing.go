// Package pricing centralises cost calculation for AI service calls (LLM chat,
// vision, embedding, rerank, storage-per-GB, per-call). The recorder (async
// batch usage persistence) and biz/sop (synchronous cost lookup for Reserve /
// Reconcile) share this single source of truth so that cost computed at LLM
// return time matches cost persisted on the usage_record row for the same call.
//
// Design notes (spec §3.0):
//
//   - CalculateCost is a pure function over pricing_rule lookup → formula.
//   - pricing_rule rows change at operator speed (hours-to-days), so lookups
//     are served from an in-process LRU with a 5-minute TTL — expected hit
//     rate in production is 99%+.
//   - Admin-driven rule edits fire pubsub "pricing_rule_changed"; subscribers
//     call InvalidateCache() to evict stale entries across all app replicas.
package pricing

import (
	"context"
	"errors"
	"fmt"
	"math"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// PricingStore is the subset of the store layer that this package needs. It is
// defined locally (rather than importing store.IStore) to keep the package
// free of the full store graph — any implementation that satisfies these three
// methods (the existing billingStore does) can be passed to NewCalculator.
type PricingStore interface {
	// GetPricingRule looks up the active pricing_rule row for the given
	// (service_type, provider, model) triple. Returns gorm.ErrRecordNotFound
	// when no row matches.
	GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error)

	// GetPricingRuleTiers returns the tiered_token sub-rows for a rule. Called
	// only when PricingRule.BillingMode == "tiered_token".
	GetPricingRuleTiers(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error)

	// GetProviderModelID resolves the provider-specific model ID for a logical
	// model key (joined through ai_service + ai_service_route).
	// Returns ("", gorm.ErrRecordNotFound) when no mapping exists.
	GetProviderModelID(ctx context.Context, modelKey, providerName string) (string, error)
}

// ICalculator is the public contract for cost calculation.
type ICalculator interface {
	// CalculateCost returns the cost in cents (int64) for a single call with
	// the given (service_type, provider, model) and token counts. Prompt /
	// completion tokens are the primary inputs for LLM calls; for embedding
	// calls where only total_tokens is known, callers pass the total value as
	// promptTokens and leave completionTokens at 0.
	//
	// Errors:
	//   - gorm.ErrRecordNotFound if no pricing rule is configured
	//   - any other error indicates a real DB / lookup failure (callers
	//     should bubble this up — do NOT silently bill at ¥0)
	CalculateCost(ctx context.Context, serviceType, provider, model string,
		promptTokens, completionTokens int) (costCents int64, err error)
}

// calculator is the default ICalculator implementation. Thread-safe.
type calculator struct {
	store PricingStore
	cache *ruleCache
}

// NewCalculator builds a Calculator backed by the given store. The LRU cache
// size (500 entries) is sized to cover the cross product of service_type ×
// provider × model (~dozens in prod); oversizing is cheap because values are
// small pointer-to-PricingRule entries.
//
// The internal cache is registered globally so that InvalidateCache(...) can
// evict stale entries across every live Calculator (see cache.go).
func NewCalculator(store PricingStore) ICalculator {
	c := &calculator{
		store: store,
		cache: newRuleCache(cacheSize, cacheTTL),
	}
	registerCache(c.cache)
	return c
}

// Close unregisters the calculator's cache from the global invalidation
// registry. Primarily useful in tests that construct many short-lived
// Calculators; long-running production singletons do not need to call this.
func (c *calculator) Close() {
	unregisterCache(c.cache)
}

// CalculateCost implements ICalculator.
//
// Formula (flat billing_mode):
//
//	costYuan = prompt/1e6 * input_price_per_mtok + completion/1e6 * output_price_per_mtok
//	costCents = round(costYuan * 100)
//
// Tiered billing_mode dispatches to calculateTieredCost which looks up input /
// output tiers by the prompt-token count (current policy: prompt tokens select
// the bracket for both input and output pricing).
//
// This function is the single source of truth — recorder.buildRecord and
// biz/sop both call it so that cost persisted on usage_record matches the
// actualCost used for credit reconciliation.
func (c *calculator) CalculateCost(ctx context.Context, serviceType, provider, model string,
	promptTokens, completionTokens int,
) (int64, error) {
	rule, err := c.resolvePricingRule(ctx, serviceType, provider, model)
	if err != nil {
		return 0, err
	}

	var costYuan float64
	switch {
	case promptTokens > 0 || completionTokens > 0:
		if rule.BillingMode == "tiered_token" {
			costYuan, err = c.calculateTieredCost(ctx, rule.ID, promptTokens, completionTokens)
			if err != nil {
				return 0, err
			}
		} else {
			costYuan = float64(promptTokens)/1_000_000*rule.InputPricePerMTok +
				float64(completionTokens)/1_000_000*rule.OutputPricePerMTok
		}
	default:
		// Zero-token call: cost is 0. Caller gets (0, nil); the rule was
		// resolved successfully so this is not a pricing miss.
		return 0, nil
	}

	return int64(math.Round(costYuan * 100)), nil
}

// calculateTieredCost looks up input/output tiers and computes cost. Tiers are
// selected by prompt-token bracket per current pricing policy. Returns a
// business error when a rule is marked tiered but has no tiers configured
// (data integrity issue — do NOT silently bill ¥0).
func (c *calculator) calculateTieredCost(ctx context.Context, ruleID uint, promptTokens, completionTokens int) (float64, error) {
	tiers, err := c.store.GetPricingRuleTiers(ctx, ruleID)
	if err != nil {
		return 0, fmt.Errorf("pricing: GetPricingRuleTiers(%d): %w", ruleID, err)
	}
	if len(tiers) == 0 {
		return 0, fmt.Errorf("pricing: tiered_token rule %d has no tiers configured", ruleID)
	}

	lookup := func(tokenType string) float64 {
		for _, tier := range tiers {
			if tier.TokenType != tokenType {
				continue
			}
			if uint(promptTokens) >= tier.MinTokens &&
				(tier.MaxTokens == nil || uint(promptTokens) <= *tier.MaxTokens) {
				return tier.CostPerMTok
			}
		}
		return 0
	}

	inputCost := lookup("input")
	outputCost := lookup("output")
	return float64(promptTokens)/1_000_000*inputCost +
		float64(completionTokens)/1_000_000*outputCost, nil
}

// resolvePricingRule implements the two-step lookup: direct by model_key, then
// fallback via GetProviderModelID. Both steps are cache-backed (same LRU,
// separate keys — a hit on either path avoids the DB round-trip).
func (c *calculator) resolvePricingRule(ctx context.Context, serviceType, provider, modelKey string) (*model.PricingRule, error) {
	directKey := cacheKey(serviceType, provider, modelKey)
	if rule, ok := c.cache.Get(directKey); ok {
		return rule, nil
	}

	rule, err := c.store.GetPricingRule(ctx, serviceType, provider, modelKey)
	if err == nil {
		c.cache.Put(directKey, rule)
		return rule, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Fallback: resolve provider-specific model ID and retry.
	providerModelID, resolveErr := c.store.GetProviderModelID(ctx, modelKey, provider)
	if resolveErr != nil {
		if errors.Is(resolveErr, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, resolveErr
	}
	if providerModelID == modelKey {
		return nil, gorm.ErrRecordNotFound
	}

	fallbackKey := cacheKey(serviceType, provider, providerModelID)
	if rule, ok := c.cache.Get(fallbackKey); ok {
		return rule, nil
	}
	rule, err = c.store.GetPricingRule(ctx, serviceType, provider, providerModelID)
	if err == nil {
		c.cache.Put(fallbackKey, rule)
	}
	return rule, err
}
