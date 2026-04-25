package contextbudget

import (
	"fmt"
	"math"
)

// ModelCapability describes the token limits of a specific AI model.
type ModelCapability struct {
	// ContextWindow is the maximum total tokens (input + output) the model supports.
	ContextWindow int `json:"context_window"`
	// MaxOutputTokens is the maximum number of tokens the model can generate in one response.
	MaxOutputTokens int `json:"max_output_tokens"`
}

// BudgetPolicy describes how the caller wants to allocate the context window.
type BudgetPolicy struct {
	// Operation is a human-readable name for the operation (used in error messages).
	Operation string `json:"operation"`
	// ReservedOutputTokens is the number of tokens reserved for model output.
	// Must be > 0 and <= MaxOutputTokens.
	ReservedOutputTokens int `json:"reserved_output_tokens"`
	// SafeRatio [0.50, 0.95] is applied to (ContextWindow - ReservedOutputTokens - FixedOverheadTokens)
	// to derive the safe input budget.
	SafeRatio float64 `json:"safe_ratio"`
	// FixedOverheadTokens is a constant overhead (e.g., chat formatting, tool definitions).
	FixedOverheadTokens int `json:"fixed_overhead_tokens"`
	// SoftThresholdRatio is the fraction of SafeInputBudget that triggers advisory warnings.
	// Defaults to 0.7 if zero.
	SoftThresholdRatio float64 `json:"soft_threshold_ratio"`
	// HardThresholdRatio is the fraction of SafeInputBudget that triggers active compression.
	// Defaults to 0.85 if zero.
	HardThresholdRatio float64 `json:"hard_threshold_ratio"`
	// ChargeUser, when true, indicates that this operation is billable.
	ChargeUser bool `json:"charge_user"`
}

// Budget contains the computed token budget thresholds for an operation.
type Budget struct {
	// SafeInputBudget is the maximum tokens for the input side of the prompt.
	SafeInputBudget int `json:"safe_input_budget"`
	// SoftThreshold is the token count at which the caller should begin monitoring.
	SoftThreshold int `json:"soft_threshold"`
	// HardThreshold is the token count at which active compression is required.
	HardThreshold int `json:"hard_threshold"`
}

// ComputeBudget validates the policy and computes the token budget thresholds.
//
// Spec §2.4 formula:
//
//	safe_input_budget = floor((context_window - reserved_output_tokens - fixed_overhead_tokens) * safe_ratio)
//	soft_threshold    = floor(safe_input_budget * soft_threshold_ratio)
//	hard_threshold    = floor(safe_input_budget * hard_threshold_ratio)
func ComputeBudget(cap ModelCapability, policy BudgetPolicy) (Budget, error) {
	if err := validateBudgetPolicy(cap, policy); err != nil {
		return Budget{}, err
	}

	available := cap.ContextWindow - policy.ReservedOutputTokens - policy.FixedOverheadTokens
	safeInputBudget := int(math.Floor(float64(available) * policy.SafeRatio))

	if safeInputBudget <= 0 {
		return Budget{}, fmt.Errorf("%w: safe_input_budget=%d <= 0 (operation=%s)",
			ErrContextConfigInvalid, safeInputBudget, policy.Operation)
	}

	softRatio := policy.SoftThresholdRatio
	if softRatio <= 0 {
		softRatio = 0.7
	}
	hardRatio := policy.HardThresholdRatio
	if hardRatio <= 0 {
		hardRatio = 0.85
	}

	softThreshold := int(math.Floor(float64(safeInputBudget) * softRatio))
	hardThreshold := int(math.Floor(float64(safeInputBudget) * hardRatio))

	return Budget{
		SafeInputBudget: safeInputBudget,
		SoftThreshold:   softThreshold,
		HardThreshold:   hardThreshold,
	}, nil
}

// validateBudgetPolicy enforces the 8 spec §2.4 constraints.
func validateBudgetPolicy(cap ModelCapability, policy BudgetPolicy) error {
	op := policy.Operation

	// 1. context_window > 0
	if cap.ContextWindow <= 0 {
		return fmt.Errorf("%w: context_window must be > 0 (operation=%s)", ErrContextConfigInvalid, op)
	}
	// 2. max_output_tokens > 0
	if cap.MaxOutputTokens <= 0 {
		return fmt.Errorf("%w: max_output_tokens must be > 0 (operation=%s)", ErrContextConfigInvalid, op)
	}
	// 3. max_output_tokens < context_window
	if cap.MaxOutputTokens >= cap.ContextWindow {
		return fmt.Errorf("%w: max_output_tokens (%d) must be < context_window (%d) (operation=%s)",
			ErrContextConfigInvalid, cap.MaxOutputTokens, cap.ContextWindow, op)
	}
	// 4. reserved_output_tokens > 0
	if policy.ReservedOutputTokens <= 0 {
		return fmt.Errorf("%w: reserved_output_tokens must be > 0 (operation=%s)", ErrContextConfigInvalid, op)
	}
	// 5. reserved_output_tokens <= max_output_tokens
	if policy.ReservedOutputTokens > cap.MaxOutputTokens {
		return fmt.Errorf("%w: reserved_output_tokens (%d) must be <= max_output_tokens (%d) (operation=%s)",
			ErrContextConfigInvalid, policy.ReservedOutputTokens, cap.MaxOutputTokens, op)
	}
	// 6. reserved_output_tokens + fixed_overhead_tokens < context_window
	if policy.ReservedOutputTokens+policy.FixedOverheadTokens >= cap.ContextWindow {
		return fmt.Errorf("%w: reserved_output_tokens (%d) + fixed_overhead_tokens (%d) must be < context_window (%d) (operation=%s)",
			ErrContextConfigInvalid,
			policy.ReservedOutputTokens, policy.FixedOverheadTokens, cap.ContextWindow, op)
	}
	// 7. 0.50 <= safe_ratio <= 0.95
	if policy.SafeRatio < 0.50 || policy.SafeRatio > 0.95 {
		return fmt.Errorf("%w: safe_ratio (%.2f) must be in [0.50, 0.95] (operation=%s)",
			ErrContextConfigInvalid, policy.SafeRatio, op)
	}

	return nil
}
