package contextbudget_test

import (
	"math"
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

func TestBudgetPolicyValidatesReservedOutputAndSafeRatio(t *testing.T) {
	tests := []struct {
		name    string
		cap     contextbudget.ModelCapability
		policy  contextbudget.BudgetPolicy
		wantErr bool
	}{
		{
			name: "valid config produces positive budget",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.85,
				FixedOverheadTokens:  500,
				SoftThresholdRatio:   0.7,
				HardThresholdRatio:   0.9,
			},
			wantErr: false,
		},
		{
			name: "safe_ratio below 0.50 is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.49,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "safe_ratio above 0.95 is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.96,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "reserved_output_tokens exceeds max_output_tokens is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 2000,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 3000,
				SafeRatio:            0.85,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "reserved_output_tokens zero is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 0,
				SafeRatio:            0.85,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "context_window zero is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   0,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.85,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "max_output_tokens equals context_window is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 128000,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.85,
				FixedOverheadTokens:  500,
			},
			wantErr: true,
		},
		{
			name: "reserved + overhead >= context_window is invalid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   4096,
				MaxOutputTokens: 4000,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 3000,
				SafeRatio:            0.85,
				FixedOverheadTokens:  2000,
			},
			wantErr: true,
		},
		{
			name: "safe_ratio exactly 0.50 is valid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.50,
				FixedOverheadTokens:  500,
				SoftThresholdRatio:   0.7,
				HardThresholdRatio:   0.9,
			},
			wantErr: false,
		},
		{
			name: "safe_ratio exactly 0.95 is valid",
			cap: contextbudget.ModelCapability{
				ContextWindow:   128000,
				MaxOutputTokens: 4096,
			},
			policy: contextbudget.BudgetPolicy{
				ReservedOutputTokens: 2048,
				SafeRatio:            0.95,
				FixedOverheadTokens:  500,
				SoftThresholdRatio:   0.7,
				HardThresholdRatio:   0.9,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			budget, err := contextbudget.ComputeBudget(tc.cap, tc.policy)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error but got nil; budget=%+v", budget)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error but got: %v", err)
				}
				if budget.SafeInputBudget <= 0 {
					t.Errorf("SafeInputBudget must be > 0, got %d", budget.SafeInputBudget)
				}
			}
		})
	}
}

func TestComputeBudgetUsesDefaultHardRatio0_85(t *testing.T) {
	// When policy.HardThresholdRatio==0, HardThreshold should equal floor(SafeInputBudget * 0.85).
	cap := contextbudget.ModelCapability{ContextWindow: 100000, MaxOutputTokens: 16384}
	policy := contextbudget.BudgetPolicy{
		Operation:            "test",
		ReservedOutputTokens: 8192,
		SafeRatio:            0.85,
		FixedOverheadTokens:  512,
		SoftThresholdRatio:   0.70,
		// HardThresholdRatio: 0 (not set — should default to 0.85)
	}
	b, err := contextbudget.ComputeBudget(cap, policy)
	if err != nil {
		t.Fatal(err)
	}
	want := int(math.Floor(float64(b.SafeInputBudget) * 0.85))
	if b.HardThreshold != want {
		t.Fatalf("HardThreshold = %d, want %d (floor(safe*0.85))", b.HardThreshold, want)
	}
}
