package billing

import (
	"testing"

	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// ----------------------------------------------------------------------------
// T2 (native-cache-adapters): buildRecord copies CacheCreationTokens
//
// recorder.buildRecord (the non-prebuilt path, after :296) must copy
// event.Usage.CacheCreationTokens into record.CacheCreationTokens so the
// 3-bucket cost (CalculateCostWithCacheRW) and revenue (computeRevenue) carves
// see the creation count. T1 wired the formulas; T2 wires the copy.
//
// ZERO REGRESSION: CacheCreationTokens==0 (every non-Claude event) ⇒ cost AND
// revenue byte-identical to the read-only Batch-A path.
// ----------------------------------------------------------------------------

// TestBuildRecord_CopiesCacheCreationTokens proves the copy site at recorder.go
// :296 threads event.Usage.CacheCreationTokens into the persisted record AND
// that the creation premium reaches BOTH cost (RW) and revenue (3-bucket carve).
func TestBuildRecord_CopiesCacheCreationTokens(t *testing.T) {
	clearPricingCache()

	// input cost 14 / output 0; sell input 20 / output 0.
	// read cost 1.4 (0.1x) / sell 2.0; creation cost 25.76 (1.84x) / sell 36.8.
	rule := creationPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0), ptrF(25.76), ptrF(36.8))
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|claude-native|claude-opus-4-6": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	r := &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}

	// 1,000,000 prompt: 200,000 read hits, 300,000 creation writes, 500,000 normal.
	event := &UsageEvent{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "claude-native",
		Model:       "claude-opus-4-6",
		Usage: &TokenUsage{
			PromptTokens:        1_000_000,
			CompletionTokens:    0,
			CachedPromptTokens:  200_000,
			CacheCreationTokens: 300_000,
		},
	}
	record := r.buildRecord(event)

	// The copy site: event.Usage.CacheCreationTokens must reach the record.
	if record.CacheCreationTokens != 300_000 {
		t.Fatalf("CacheCreationTokens not threaded into record: got %d, want 300000 (copy site at recorder.go:296 missing)", record.CacheCreationTokens)
	}
	if record.CachedPromptTokens != 200_000 {
		t.Errorf("CachedPromptTokens: got %d, want 200000", record.CachedPromptTokens)
	}

	// Cost (RW 3-bucket carve, write FIRST):
	// 300k write @25.76 = 7.728; 200k read @1.4 = 0.28; 500k normal @14 = 7.0
	// total = 15.008 yuan = 1500.8 → round 1501 cents.
	if record.CostCents != 1501 {
		t.Errorf("CostCents = %d, want 1501 (creation premium not applied via ...RW)", record.CostCents)
	}
	// Revenue (sell 3-bucket carve):
	// 300k write @36.8 = 11.04; 200k read @2.0 = 0.4; 500k normal @20 = 10.0
	// total = 21.44 yuan = 2144 cents.
	if record.RevenueCents != 2144 {
		t.Errorf("RevenueCents = %d, want 2144 (creation sell premium not applied)", record.RevenueCents)
	}
}

// TestBuildRecord_NoCreation_ByteIdentical is the zero-regression control:
// CacheCreationTokens==0 ⇒ the creation buckets contribute nothing, so cost and
// revenue collapse to the read-only carve byte-identically.
func TestBuildRecord_NoCreation_ByteIdentical(t *testing.T) {
	clearPricingCache()

	rule := creationPricingRule(14, 0, 20, 0, ptrF(1.4), ptrF(2.0), ptrF(25.76), ptrF(36.8))
	store := &stubUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm_chat|claude-native|claude-opus-4-6": rule,
		},
	}
	calc := pricing.NewCalculator(store)
	r := &UsageRecorder{store: store, calc: calc, ch: make(chan *UsageEvent, 1), done: make(chan struct{})}

	event := &UsageEvent{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "claude-native",
		Model:       "claude-opus-4-6",
		Usage: &TokenUsage{
			PromptTokens:        1_000_000,
			CompletionTokens:    0,
			CachedPromptTokens:  400_000,
			CacheCreationTokens: 0,
		},
	}
	record := r.buildRecord(event)

	if record.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want 0", record.CacheCreationTokens)
	}
	// Cost: 400k read @1.4 + 600k normal @14 = 0.56 + 8.4 = 8.96 yuan = 896 cents.
	if record.CostCents != 896 {
		t.Errorf("CostCents = %d, want 896 (creation=0 must collapse to read-only carve)", record.CostCents)
	}
	// Revenue: 400k read @2.0 + 600k normal @20 = 0.8 + 12.0 = 12.8 yuan = 1280 cents.
	if record.RevenueCents != 1280 {
		t.Errorf("RevenueCents = %d, want 1280 (creation=0 must collapse to read-only carve)", record.RevenueCents)
	}
}
