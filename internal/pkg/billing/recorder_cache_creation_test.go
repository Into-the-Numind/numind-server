package billing

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// ----------------------------------------------------------------------------
// T1 (native-cache-adapters): computeRevenue 3-bucket sell carve
//
// recorder.computeRevenue must mirror the cost-side 3-bucket carve: cache-WRITE
// (creation) tokens are a PREMIUM and MUST be billed to the user at
// SellCacheCreationInputPricePerMTok, not at the discounted read rate and not at
// the flat input rate. Carve write FIRST, then read, so nonCached never goes
// negative.
//
// ZERO REGRESSION:
//   - record.CacheCreationTokens==0 (every non-Claude call) ⇒ cw=0 ⇒ collapses
//     to the current read-only carve byte-identically.
//   - SellCacheCreationInputPricePerMTok==nil ⇒ write bucket priced at
//     SellInputPricePerMTok (no premium, no over/under charge).
// ----------------------------------------------------------------------------

// creationPricingRule builds a flat rule carrying the read AND creation sell/cost
// price pairs so the recorder's 3-bucket revenue branch can be exercised.
func creationPricingRule(inputCost, outputCost, inputSell, outputSell float64,
	cachedCost, cachedSell, creationCost, creationSell *float64,
) *model.PricingRule {
	r := cachedPricingRule(inputCost, outputCost, inputSell, outputSell, cachedCost, cachedSell)
	r.CacheCreationInputPricePerMTok = creationCost
	r.SellCacheCreationInputPricePerMTok = creationSell
	return r
}

// newRevenueRecorder wires a recorder over a one-rule store. computeRevenue is
// called directly with a hand-built record (T1 does not yet copy
// CacheCreationTokens through buildRecord — that is T2).
func newRevenueRecorder(t *testing.T, rule *model.PricingRule) *UsageRecorder {
	t.Helper()
	clearPricingCache()
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|claude-native|claude-opus-4-6": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	return &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}
}

func creationRecord(prompt, comp, cachedRead, creation int) *model.UsageRecord {
	return &model.UsageRecord{
		ServiceType:         "llm_chat",
		Provider:            "claude-native",
		Model:               "claude-opus-4-6",
		PromptTokens:        prompt,
		CompletionTokens:    comp,
		CachedPromptTokens:  cachedRead,
		CacheCreationTokens: creation,
		CreatedAt:           time.Now(),
	}
}

// TestComputeRevenue_NoCreation_ByteIdentical (T1-f): CacheCreationTokens==0 ⇒ the
// 3-bucket carve collapses to the legacy read-only carve. Compared against a
// record with the creation column simply absent (same numbers).
func TestComputeRevenue_NoCreation_ByteIdentical(t *testing.T) {
	// sell input 20, sell output 0; cached read sell 2.0 (0.1x); creation sell 36 (premium).
	rule := creationPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0), ptrF(25.0), ptrF(36.0))
	r := newRevenueRecorder(t, rule)
	ctx := context.Background()

	// 1M prompt, 400k read, 0 creation.
	//   cr:        400_000/1e6 * 2.0  = 0.80
	//   nonCached: 600_000/1e6 * 20.0 = 12.00
	//   total = 12.8 yuan = 1280 cents (identical to pre-creation read-only carve).
	rev := r.computeRevenue(ctx, creationRecord(1_000_000, 0, 400_000, 0))
	if rev != 1280 {
		t.Errorf("no-creation revenue = %d, want 1280 (read-only carve unchanged)", rev)
	}
}

// TestComputeRevenue_CreationPremiumCharged (T1-g): CacheCreationTokens>0 with
// SellCacheCreationInputPricePerMTok set ⇒ the write bucket is charged the
// creation premium on the sell side.
//
// sell input 20, cached read sell 2.0, creation sell 36. completion 0.
// prompt=1M; cw=300k; cr=200k; nonCached=500k.
//
//	cw:        300_000/1e6 * 36  = 10.80
//	cr:        200_000/1e6 * 2.0 = 0.40
//	nonCached: 500_000/1e6 * 20  = 10.00
//	total = 21.2 yuan = 2120 cents.
func TestComputeRevenue_CreationPremiumCharged(t *testing.T) {
	rule := creationPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0), ptrF(25.0), ptrF(36.0))
	r := newRevenueRecorder(t, rule)
	ctx := context.Background()

	rev := r.computeRevenue(ctx, creationRecord(1_000_000, 0, 200_000, 300_000))
	const want = int64(2120)
	if rev != want {
		t.Errorf("creation-premium revenue = %d, want %d", rev, want)
	}

	// Control: misfiling the creation tokens as read would UNDER-charge — the bug
	// D3 sell-side carve guards against.
	wrong := r.computeRevenue(ctx, creationRecord(1_000_000, 0, 500_000, 0))
	if rev <= wrong {
		t.Errorf("creation premium revenue (%d) must exceed read-misfile (%d)", rev, wrong)
	}
}

// TestComputeRevenue_NullCreationSell_FullSellRate (T1-h): creation sell price
// NULL ⇒ the write bucket bills at SellInputPricePerMTok (no premium, no
// over/under charge). With read sell also NULL here, the whole prompt bills at
// the flat sell input rate.
func TestComputeRevenue_NullCreationSell_FullSellRate(t *testing.T) {
	// read + creation sell prices BOTH NULL.
	rule := creationPricingRule(14, 0, 20, 0, nil, nil, nil, nil)
	r := newRevenueRecorder(t, rule)
	ctx := context.Background()

	// 1M prompt: 300k creation + 200k read + 500k nonCached, all at sell input 20.
	//   1M/1e6 * 20 = 20 yuan = 2000 cents.
	rev := r.computeRevenue(ctx, creationRecord(1_000_000, 0, 200_000, 300_000))
	if rev != 2000 {
		t.Errorf("NULL creation sell must bill write at full sell input: rev=%d want 2000", rev)
	}
}

// TestComputeRevenue_CreationCarveOrderingNoNegative (T1): write carved FIRST,
// then read, so nonCached never goes negative on a provider over-report.
func TestComputeRevenue_CreationCarveOrderingNoNegative(t *testing.T) {
	rule := creationPricingRule(14, 0, 20, 0, ptrF(2.0), ptrF(2.0), ptrF(36.0), ptrF(36.0))
	r := newRevenueRecorder(t, rule)
	ctx := context.Background()

	// prompt=1M, cw=800k, cr=500k → cw+cr=1.3M > prompt.
	// Carve write FIRST: cw=800k. Remainder=200k → cr clamped to 200k. nonCached=0.
	//   cw: 800_000/1e6*36  = 28.80
	//   cr: 200_000/1e6*2.0 = 0.40
	//   total = 29.2 yuan = 2920 cents.
	rev := r.computeRevenue(ctx, creationRecord(1_000_000, 0, 500_000, 800_000))
	const want = int64(2920)
	if rev != want {
		t.Errorf("carve-order revenue = %d, want %d (no negative nonCached)", rev, want)
	}
}
