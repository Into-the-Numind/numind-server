package pricing

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/model"
)

// TestIsFreeModel covers the free-model truth table (feature free-model-member-only):
// a model is "free" iff a pricing rule exists, is flat, and all cost components
// (input/output per-MTok + per-call) are 0. A missing rule is NOT free.
func TestIsFreeModel(t *testing.T) {
	ctx := context.Background()
	sentinelErr := errors.New("db exploded")

	const st, prov = "llm_chat", "youshu"

	rule := func(in, out, perCall float64, mode string) *model.PricingRule {
		return &model.PricingRule{
			ID:                 1,
			ServiceType:        st,
			Provider:           prov,
			BillingMode:        mode,
			InputPricePerMTok:  in,
			OutputPricePerMTok: out,
			PricePerCall:       perCall,
			CreditMultiplier:   1.0,
			IsActive:           true,
		}
	}

	tests := []struct {
		name     string
		model    string
		rule     *model.PricingRule
		storeErr error
		wantFree bool
		wantErr  bool
	}{
		{name: "all-zero flat → free", model: "agnes", rule: rule(0, 0, 0, "flat"), wantFree: true},
		{name: "nonzero input → not free", model: "qwen", rule: rule(2, 0, 0, "flat"), wantFree: false},
		{name: "nonzero output → not free", model: "qwen2", rule: rule(0, 3, 0, "flat"), wantFree: false},
		{name: "nonzero per-call → not free", model: "imgmodel", rule: rule(0, 0, 5, "flat"), wantFree: false},
		{name: "tiered rule never free (even all-zero)", model: "tieredfree", rule: rule(0, 0, 0, "tiered_token"), wantFree: false},
		{name: "nonzero PricePerGB, zero LLM prices → still free (GB not examined, LLM-only gate)", model: "gbmodel", rule: &model.PricingRule{ID: 2, ServiceType: st, Provider: prov, BillingMode: "flat", PricePerGB: 1.5, CreditMultiplier: 1.0, IsActive: true}, wantFree: true},
		{name: "no rule (not found) → not free, no err", model: "unpriced", rule: nil, wantFree: false},
		{name: "db error → not free, err propagated", model: "anything", rule: nil, storeErr: sentinelErr, wantFree: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubPricingStore{
				pricingRules:     map[string]*model.PricingRule{},
				pricingErr:       tc.storeErr,
				providerModelIDs: map[string]string{},
			}
			if tc.rule != nil {
				store.pricingRules[st+"|"+prov+"|"+tc.model] = tc.rule
			}
			calc := NewCalculator(store)

			gotFree, err := calc.IsFreeModel(ctx, st, prov, tc.model)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, sentinelErr) {
					t.Fatalf("expected sentinel error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotFree != tc.wantFree {
				t.Fatalf("IsFreeModel = %v, want %v", gotFree, tc.wantFree)
			}
		})
	}
}
