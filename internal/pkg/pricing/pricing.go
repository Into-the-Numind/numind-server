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

	// GetPricingRuleByModel looks up the active pricing_rule for a (provider,
	// model) pair IGNORING service_type. It is the service_type-agnostic
	// fallback for resolvePricingRule: a unified model (e.g. claude-opus-4-6)
	// carries a single per-token price regardless of modality, so an image
	// request classified "llm_vision" must still resolve to that model's row
	// (registered under "llm_chat"). Returns gorm.ErrRecordNotFound when no row
	// matches. When multiple active rows exist across service_types the newest
	// (highest id) wins.
	GetPricingRuleByModel(ctx context.Context, provider, modelName string) (*model.PricingRule, error)
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

	// CalculateCostWithCache is CalculateCost plus prompt-cache awareness.
	// cachedTokens is the subset of promptTokens served from the provider's
	// prompt cache (0 ⇒ identical to CalculateCost). cachedTokens is clamped to
	// [0, promptTokens]. The cached portion is billed at the rule's cached input
	// price when set; when NULL it falls back to the full input price, making the
	// result byte-identical to CalculateCost. Tiered-billing rules ignore the
	// cache argument entirely (not cache-aware in Batch A).
	CalculateCostWithCache(ctx context.Context, serviceType, provider, model string,
		promptTokens, completionTokens, cachedTokens int) (costCents int64, err error)

	// IsFreeModel reports whether (serviceType, provider, model) resolves to a
	// zero-priced model — a pricing rule exists and all of its COST components
	// are 0 (input/output per-MTok and per-call), so a call charges the user 0
	// credits. Used by the free-model member gate (feature free-model-member-only).
	//
	//   - rule found, flat, all-zero          → (true, nil)
	//   - rule found but any cost price != 0   → (false, nil)
	//   - rule found but tiered_token          → (false, nil)  (0-price models are flat)
	//   - no rule (gorm.ErrRecordNotFound)     → (false, nil)  ("unpriced" is NOT "free")
	//   - any other lookup error               → (false, err)
	//
	// It performs a single cache-backed lookup and never multiplies by tokens, so
	// it is a cheap synchronous check suitable for per-call gating.
	IsFreeModel(ctx context.Context, serviceType, provider, model string) (isFree bool, err error)
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

// CalculateCost implements ICalculator. It is a thin delegate to
// CalculateCostWithCache with cachedTokens=0, so the formula lives in exactly one
// place and the no-cache path is provably identical to the cache-aware path.
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
	return c.CalculateCostWithCache(ctx, serviceType, provider, model, promptTokens, completionTokens, 0)
}

// CalculateCostWithCache implements ICalculator with prompt-cache awareness.
//
// Flat billing_mode formula (cache-aware):
//
//	cachedPrice = cached_input_price_per_mtok (if set) else input_price_per_mtok
//	costYuan    = cached/1e6*cachedPrice + (prompt-cached)/1e6*input_price_per_mtok
//	              + completion/1e6*output_price_per_mtok
//
// Zero-regression guarantees (PRIME DIRECTIVE):
//   - cachedTokens==0 ⇒ the cached term is 0 and (prompt-cached)==prompt, so the
//     formula collapses exactly to the legacy prompt/1e6*input + completion/1e6*output.
//   - CachedInputPricePerMTok==nil (column NULL) ⇒ cachedPrice==input_price_per_mtok,
//     so cached + non-cached portions sum to prompt/1e6*input regardless of split.
//
// cachedTokens is clamped to [0, promptTokens]. Tiered rules are billed at full
// price (not cache-aware in Batch A). CreditMultiplier scaling is preserved.
func (c *calculator) CalculateCostWithCache(ctx context.Context, serviceType, provider, model string,
	promptTokens, completionTokens, cachedTokens int,
) (int64, error) {
	rule, err := c.resolvePricingRule(ctx, serviceType, provider, model)
	if err != nil {
		return 0, err
	}

	// Clamp cachedTokens defensively — providers can in theory report a cache
	// count larger than the prompt (e.g. on retries) and negatives are nonsense.
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}

	var costYuan float64
	switch {
	case promptTokens > 0 || completionTokens > 0:
		if rule.BillingMode == "tiered_token" {
			// Tiered mode is NOT cache-aware in Batch A — bill full price
			// (byte-identical to today; cachedTokens is intentionally ignored).
			costYuan, err = c.calculateTieredCost(ctx, rule.ID, promptTokens, completionTokens)
			if err != nil {
				return 0, err
			}
		} else {
			cachedPrice := rule.InputPricePerMTok
			if rule.CachedInputPricePerMTok != nil {
				cachedPrice = *rule.CachedInputPricePerMTok
			}
			nonCached := promptTokens - cachedTokens
			costYuan = float64(cachedTokens)/1_000_000*cachedPrice +
				float64(nonCached)/1_000_000*rule.InputPricePerMTok +
				float64(completionTokens)/1_000_000*rule.OutputPricePerMTok
		}
	default:
		// Zero-token call: cost is 0. Caller gets (0, nil); the rule was
		// resolved successfully so this is not a pricing miss.
		return 0, nil
	}

	multiplier := rule.CreditMultiplier
	if multiplier <= 0 {
		multiplier = 1.0
	}
	return int64(math.Round(costYuan * multiplier * 100)), nil
}

// calculateTieredCost looks up input/output tiers and computes the sell-price
// cost charged to users. Tiers are selected by prompt-token bracket per current
// pricing policy. Returns a business error when a rule is marked tiered but has
// no tiers configured (data integrity issue — do NOT silently bill ¥0).
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
				return tier.SellPerMTok
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

	// Fallback 1: resolve provider-specific model ID and retry by
	// (serviceType, provider, providerModelID). A non-NotFound resolve error is
	// surfaced (don't silently bill ¥0); a NotFound resolve just leaves
	// providerModelID empty and lets fallback 2 run with modelKey alone.
	providerModelID := ""
	if pmID, resolveErr := c.store.GetProviderModelID(ctx, modelKey, provider); resolveErr != nil {
		if !errors.Is(resolveErr, gorm.ErrRecordNotFound) {
			return nil, resolveErr
		}
	} else {
		providerModelID = pmID
	}
	if providerModelID != "" && providerModelID != modelKey {
		fallbackKey := cacheKey(serviceType, provider, providerModelID)
		if rule, ok := c.cache.Get(fallbackKey); ok {
			return rule, nil
		}
		rule, err = c.store.GetPricingRule(ctx, serviceType, provider, providerModelID)
		if err == nil {
			c.cache.Put(fallbackKey, rule)
			return rule, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	// Fallback 2 (fix ①, service_type-agnostic): a unified model (e.g.
	// claude-opus-4-6) carries ONE per-token price regardless of modality, so an
	// image request the gateway classified "llm_vision" must still resolve to the
	// model's row registered under another service_type ("llm_chat"). Only
	// reached AFTER the exact (serviceType, …) lookups missed → a dedicated
	// vision model with its own llm_vision row keeps precedence (no regression).
	return c.resolveAgnostic(ctx, provider, modelKey, providerModelID)
}

// resolveAgnostic is the service_type-agnostic last-resort lookup (fix ①). It
// tries (provider, modelKey) then (provider, providerModelID), caching hits
// under agnosticCacheKey() — a dedicated prefix that never collides with the
// "serviceType|provider|model" keys used by the exact lookups above.
//
// Returns gorm.ErrRecordNotFound when no candidate matches. A non-NotFound DB
// error (timeout, connection refused) is PROPAGATED — consistent with the rest
// of resolvePricingRule and the ICalculator contract: a real lookup failure must
// bubble up, never be silently downgraded to "not found" (which would bill ¥0).
//
// follow-up: two sibling resolvers (billing.ResolvePricingRule for usage_record
// snapshots, middleware/billing.go inline) duplicate this 2-step logic and do
// NOT yet get the agnostic fallback — their pricing_*_snapshot columns stay NULL
// for vision→unified-model calls. Cost/reconcile are unaffected (both flow
// through this resolver via calc). Consolidating the three resolvers is tracked
// separately.
func (c *calculator) resolveAgnostic(ctx context.Context, provider, modelKey, providerModelID string) (*model.PricingRule, error) {
	candidates := []string{modelKey}
	if providerModelID != "" && providerModelID != modelKey {
		candidates = append(candidates, providerModelID)
	}
	for _, m := range candidates {
		if m == "" {
			continue
		}
		aKey := agnosticCacheKey(provider, m)
		if rule, ok := c.cache.Get(aKey); ok {
			return rule, nil
		}
		rule, err := c.store.GetPricingRuleByModel(ctx, provider, m)
		if err == nil {
			c.cache.Put(aKey, rule)
			return rule, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err // real DB error — propagate, do not silently bill ¥0
		}
		// NotFound on this candidate → try the next.
	}
	return nil, gorm.ErrRecordNotFound
}

// IsFreeModel implements ICalculator — see the interface doc for the full
// contract. It reuses the cache-backed resolvePricingRule and inspects COST
// price components only. For an LLM chat call the credit charge comes from the
// token formula (InputPricePerMTok / OutputPricePerMTok); PricePerCall is
// additionally required to be 0 as a conservative guard so a per-call-billed
// model never slips through as "free". PricePerGB (COS storage billing) is
// intentionally NOT examined — this gate is LLM-call specific. A missing rule is
// deliberately NOT free, so an unpriced/misconfigured model never becomes a
// member-only bypass.
func (c *calculator) IsFreeModel(ctx context.Context, serviceType, provider, model string) (bool, error) {
	rule, err := c.resolvePricingRule(ctx, serviceType, provider, model)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	// Tiered rules are never treated as free: zero-priced models are flat.
	if rule.BillingMode == "tiered_token" {
		return false, nil
	}
	return rule.InputPricePerMTok == 0 && rule.OutputPricePerMTok == 0 && rule.PricePerCall == 0, nil
}
