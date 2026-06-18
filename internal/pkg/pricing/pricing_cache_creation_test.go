package pricing

import (
	"context"
	"testing"

	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// T1 (native-cache-adapters): CalculateCostWithCacheRW — 3-bucket cost formula
//
// Claude returns THREE disjoint prompt-side buckets: uncached input
// (input_price), cache-READ (discounted cached_input_price), and cache-CREATION
// (a PREMIUM cache_creation_input_price). The flat-mode formula carves write
// FIRST, then read, so normal = prompt - cw - cr can never go negative.
//
// PRIME DIRECTIVE — ZERO REGRESSION:
//   - CalculateCostWithCacheRW(..., cacheWriteTokens=0) MUST be byte-identical
//     to the legacy CalculateCostWithCache(..., cachedTokens) for any inputs.
//   - CacheCreationInputPricePerMTok==nil (NULL) ⇒ creation tokens bill at the
//     full input price (no premium, no overcharge).
//   - tiered_token rules ignore BOTH cache buckets.
// ----------------------------------------------------------------------------

// flatRuleWithCreation extends flatRuleCached with the cache-CREATION price pair.
// Pass nil for either creation pointer to leave that column NULL.
func flatRuleWithCreation(id uint, inputPrice, outputPrice, sellIn, sellOut float64,
	cachedCost, cachedSell, creationCost, creationSell *float64,
) *model.PricingRule {
	r := flatRuleCached(id, inputPrice, outputPrice, sellIn, sellOut, cachedCost, cachedSell)
	r.CacheCreationInputPricePerMTok = creationCost
	r.SellCacheCreationInputPricePerMTok = creationSell
	return r
}

// TestCalculateCostWithCacheRW_ZeroWriteEqualsLegacy (T1-a): cacheWriteTokens=0
// MUST collapse to the legacy 3-arg CalculateCostWithCache for a battery of
// inputs — even when a creation price is configured (proving the write branch is
// inert at cw=0). This is the strongest zero-regression guard for the new method.
func TestCalculateCostWithCacheRW_ZeroWriteEqualsLegacy(t *testing.T) {
	ctx := context.Background()
	// Rule carries BOTH cached and creation prices, so the new branches exist but
	// must contribute nothing when cw==0.
	rule := flatRuleWithCreation(1, 14.0, 28.0, 20.0, 40.0, f64p(1.4), f64p(2.0), f64p(25.0), f64p(36.0))

	cases := []struct {
		name         string
		prompt, comp int
		cached       int
	}{
		{"no cache", 1_000_000, 500_000, 0},
		{"partial read", 1_000_000, 500_000, 400_000},
		{"all read", 1_000_000, 0, 1_000_000},
		{"read over-clamp", 1_000_000, 0, 2_000_000},
		{"small", 1234, 567, 321},
		{"zero tokens", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
				"llm_chat|claude-native|claude-opus-4-6": rule,
			}}
			calc := NewCalculator(store).(*calculator)

			legacy, err := calc.CalculateCostWithCache(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
				tc.prompt, tc.comp, tc.cached)
			if err != nil {
				t.Fatalf("legacy: %v", err)
			}
			rw, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
				tc.prompt, tc.comp, tc.cached, 0)
			if err != nil {
				t.Fatalf("RW: %v", err)
			}
			if rw != legacy {
				t.Errorf("cw=0 must equal legacy: RW=%d legacy=%d", rw, legacy)
			}
		})
	}
}

// TestCalculateCostWithCacheRW_NullCreationPriceFullRate (T1-b): cacheWriteTokens>0
// but CacheCreationInputPricePerMTok is NULL ⇒ creation tokens bill at the FULL
// input price (no premium). The result equals a plain full-price call on the same
// total prompt (since read price is also NULL here).
func TestCalculateCostWithCacheRW_NullCreationPriceFullRate(t *testing.T) {
	ctx := context.Background()
	// All cache prices NULL.
	rule := flatRuleWithCreation(1, 14.0, 0, 14.0, 0, nil, nil, nil, nil)
	store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|claude-native|claude-opus-4-6": rule,
	}}
	calc := NewCalculator(store).(*calculator)

	// 1M prompt: 300k creation + 200k read + 500k normal. All at full input (14).
	// cost = 1M/1e6*14 = 14 yuan = 1400 cents.
	cost, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 200_000, 300_000)
	if err != nil {
		t.Fatalf("RW: %v", err)
	}
	if cost != 1400 {
		t.Errorf("NULL creation price must bill creation at full input: cost=%d want 1400", cost)
	}
}

// TestCalculateCostWithCacheRW_CreationPremiumApplied (T1-c): creation price SET
// (a PREMIUM above input) ⇒ creation tokens bill at the premium, read at the
// discount, the remainder at full input.
//
// input=14, cachedRead=1.4 (0.1x), creation=25 (premium). completion=0.
// prompt=1,000,000; cacheWrite(cw)=300,000; cacheRead(cr)=200,000; normal=500,000.
//
//	cw:     300_000/1e6 * 25  = 7.50
//	cr:     200_000/1e6 * 1.4 = 0.28
//	normal: 500_000/1e6 * 14  = 7.00
//	total = 14.78 yuan = 1478 cents.
func TestCalculateCostWithCacheRW_CreationPremiumApplied(t *testing.T) {
	ctx := context.Background()
	rule := flatRuleWithCreation(1, 14.0, 0, 14.0, 0, f64p(1.4), f64p(1.4), f64p(25.0), f64p(25.0))
	store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|claude-native|claude-opus-4-6": rule,
	}}
	calc := NewCalculator(store).(*calculator)

	cost, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 200_000, 300_000)
	if err != nil {
		t.Fatalf("RW: %v", err)
	}
	const want = int64(1478)
	if cost != want {
		t.Errorf("creation premium cost = %d cents, want %d", cost, want)
	}

	// Sanity: must be MORE than billing the creation bucket as a read discount
	// (the under-bill bug D3 guards against).
	wrong, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 500_000, 0) // creation tokens misfiled as read
	if err != nil {
		t.Fatalf("RW (control): %v", err)
	}
	if cost <= wrong {
		t.Errorf("creation premium (%d) must exceed read-discount misfile (%d)", cost, wrong)
	}
}

// TestCalculateCostWithCacheRW_CarveOrderingNoNegative (T1-d): write is carved
// FIRST, then read, so normal = prompt - cw - cr never goes negative even when
// cw + cr exceed prompt (provider over-report / retry). cw is clamped to prompt,
// then cr to the remainder.
func TestCalculateCostWithCacheRW_CarveOrderingNoNegative(t *testing.T) {
	ctx := context.Background()
	// Distinct prices so any mis-carve produces a different number.
	rule := flatRuleWithCreation(1, 14.0, 0, 14.0, 0, f64p(1.4), f64p(1.4), f64p(25.0), f64p(25.0))
	store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|claude-native|claude-opus-4-6": rule,
	}}
	calc := NewCalculator(store).(*calculator)

	// prompt=1M, cw=800k, cr=500k → cw+cr=1.3M > prompt.
	// Carve write FIRST: cw=800k. Remainder=200k. cr clamped to 200k. normal=0.
	//   cw: 800_000/1e6*25  = 20.0
	//   cr: 200_000/1e6*1.4 = 0.28
	//   normal: 0
	//   total = 20.28 yuan = 2028 cents.
	cost, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 500_000, 800_000)
	if err != nil {
		t.Fatalf("RW: %v", err)
	}
	const want = int64(2028)
	if cost != want {
		t.Errorf("carve-order cost = %d cents, want %d (no negative normal)", cost, want)
	}

	// cw alone exceeds prompt → cw clamped to prompt, cr=0, normal=0.
	//   1M/1e6*25 = 25 yuan = 2500 cents.
	all, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 0, 2_000_000)
	if err != nil {
		t.Fatalf("RW (cw over-clamp): %v", err)
	}
	if all != 2500 {
		t.Errorf("cw over-clamp cost = %d, want 2500 (whole prompt at creation)", all)
	}

	// Negative cw → treated as 0 → collapses to read-only legacy carve.
	neg, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 400_000, -7)
	if err != nil {
		t.Fatalf("RW (neg cw): %v", err)
	}
	legacy, err := calc.CalculateCostWithCache(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 400_000)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if neg != legacy {
		t.Errorf("negative cw must equal read-only legacy: neg=%d legacy=%d", neg, legacy)
	}
}

// TestCalculateCostWithCacheRW_TieredIgnoresBoth (T1-e): tiered_token rules are
// NOT cache-aware — a cacheWriteTokens>0 (and cachedTokens>0) call must produce
// the SAME cost as the plain tiered path (byte-identical).
func TestCalculateCostWithCacheRW_TieredIgnoresBoth(t *testing.T) {
	ctx := context.Background()
	rule := &model.PricingRule{ID: 99, BillingMode: "tiered_token", IsActive: true}
	maxTier1 := uint(32_000)
	tiers := []model.PricingRuleTier{
		{RuleID: 99, TokenType: "input", MinTokens: 0, MaxTokens: &maxTier1, CostPerMTok: 1.0, SellPerMTok: 1.5},
		{RuleID: 99, TokenType: "output", MinTokens: 0, MaxTokens: &maxTier1, CostPerMTok: 4.0, SellPerMTok: 6.0},
	}
	store := &stubPricingStore{
		pricingRules:     map[string]*model.PricingRule{"llm_chat|aihubmix|gpt-5.4": rule},
		pricingRuleTiers: map[uint][]model.PricingRuleTier{99: tiers},
	}
	calc := NewCalculator(store).(*calculator)

	plain, err := calc.CalculateCost(ctx, "llm_chat", "aihubmix", "gpt-5.4", 10_000, 2_000)
	if err != nil {
		t.Fatalf("plain tiered: %v", err)
	}
	rw, err := calc.CalculateCostWithCacheRW(ctx, "llm_chat", "aihubmix", "gpt-5.4", 10_000, 2_000, 4_000, 3_000)
	if err != nil {
		t.Fatalf("tiered RW: %v", err)
	}
	if rw != plain {
		t.Errorf("tiered must ignore both cache buckets: RW=%d plain=%d", rw, plain)
	}
}

// TestCalculateCostWithCache_DelegatesToRW (T1): the legacy 3-arg method must
// delegate to ...RW with cacheWriteTokens=0 — single source of truth. Verified by
// equality across the same battery (covered above) plus an explicit creation-set
// rule where the legacy method must NOT charge any creation premium.
func TestCalculateCostWithCache_DelegatesToRW(t *testing.T) {
	ctx := context.Background()
	// Creation price set, but legacy method passes cw=0 → no creation cost.
	rule := flatRuleWithCreation(1, 14.0, 0, 14.0, 0, nil, nil, f64p(25.0), f64p(25.0))
	store := &stubPricingStore{pricingRules: map[string]*model.PricingRule{
		"llm_chat|claude-native|claude-opus-4-6": rule,
	}}
	calc := NewCalculator(store).(*calculator)

	// 1M prompt all at full input (no read price, no write tokens) = 1400 cents.
	cost, err := calc.CalculateCostWithCache(ctx, "llm_chat", "claude-native", "claude-opus-4-6",
		1_000_000, 0, 0)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if cost != 1400 {
		t.Errorf("legacy method must not charge creation premium: cost=%d want 1400", cost)
	}
}
