package budget

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/pricing"
)

// AgentTurnEstimate is the per-turn R2 estimate result.
type AgentTurnEstimate struct {
	EstimatedPromptTokens     int
	EstimatedCompletionTokens int
	EstimatedCredits          int64 // cost cents == credits in this system (1:1)
	Model                     string
	Provider                  string
}

// DefaultCompletionEstimate is the conservative upper-bound completion tokens
// used when caller doesn't supply completionEstimate.
const DefaultCompletionEstimate = 500

// EstimateAgentTurn estimates a single Agent turn's credit cost.
//
// promptCharCount is the user input + system prompt char count.
// completionEstimate is the conservative upper-bound completion tokens
// (default DefaultCompletionEstimate=500 when <= 0).
//
// Algorithm:
//  1. CN/EN mixed: 1 token ≈ 2 chars (conservative).
//     estPromptTokens = promptCharCount / 2 (clamped >= 1).
//  2. estCompletionTokens = completionEstimate (default 500).
//  3. Cost via pricing.ICalculator.CalculateCost.
//
// nil pricing → error (caller must wire a calculator).
func EstimateAgentTurn(ctx context.Context, pc pricing.ICalculator,
	provider, model string, promptCharCount, completionEstimate int) (*AgentTurnEstimate, error) {
	if pc == nil {
		return nil, fmt.Errorf("EstimateAgentTurn: pricing calculator is nil")
	}
	if completionEstimate <= 0 {
		completionEstimate = DefaultCompletionEstimate
	}

	// CN/EN mixed: 1 token ≈ 2 chars (conservative)
	estPromptTokens := promptCharCount / 2
	if estPromptTokens < 1 {
		estPromptTokens = 1
	}

	costCents, err := pc.CalculateCost(ctx, "llm_chat", provider, model, estPromptTokens, completionEstimate)
	if err != nil {
		return nil, fmt.Errorf("EstimateAgentTurn: CalculateCost: %w", err)
	}
	return &AgentTurnEstimate{
		EstimatedPromptTokens:     estPromptTokens,
		EstimatedCompletionTokens: completionEstimate,
		EstimatedCredits:          costCents,
		Model:                     model,
		Provider:                  provider,
	}, nil
}
