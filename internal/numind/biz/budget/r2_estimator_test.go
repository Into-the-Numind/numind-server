package budget

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPricer implements pricing.ICalculator for tests.
// Returns a deterministic cost = (promptTokens + completionTokens) cents.
type mockPricer struct {
	calcErr error
}

func (m *mockPricer) CalculateCost(ctx context.Context, op, provider, model string,
	promptTokens, completionTokens int) (int64, error) {
	if m.calcErr != nil {
		return 0, m.calcErr
	}
	return int64(promptTokens + completionTokens), nil
}

// CalculateCostWithCache satisfies pricing.ICalculator. This mock ignores the
// cached-token argument and delegates to CalculateCost (estimation never has
// cache tokens pre-call).
func (m *mockPricer) CalculateCostWithCache(ctx context.Context, op, provider, model string,
	promptTokens, completionTokens, _ int) (int64, error) {
	return m.CalculateCost(ctx, op, provider, model, promptTokens, completionTokens)
}

func TestEstimateAgentTurn_NormalPath(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 200, 0)
	require.NoError(t, err)
	assert.Equal(t, 100, got.EstimatedPromptTokens)     // 200 chars / 2 = 100
	assert.Equal(t, 500, got.EstimatedCompletionTokens) // default
	assert.Equal(t, int64(600), got.EstimatedCredits)   // mock returns p+c
	assert.Equal(t, "ali", got.Provider)
	assert.Equal(t, "qwen-turbo", got.Model)
}

func TestEstimateAgentTurn_PromptCharCountZero(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, got.EstimatedPromptTokens) // clamped to 1
	assert.Equal(t, 500, got.EstimatedCompletionTokens)
}

func TestEstimateAgentTurn_PromptCharCountNegative(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", -10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, got.EstimatedPromptTokens) // clamped to 1 (negative / 2 = 0 → 1)
}

func TestEstimateAgentTurn_LargePromptCharCount(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 10000, 0)
	require.NoError(t, err)
	assert.Equal(t, 5000, got.EstimatedPromptTokens) // 10000 / 2
}

func TestEstimateAgentTurn_CustomCompletionEstimate(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 200, 1000)
	require.NoError(t, err)
	assert.Equal(t, 1000, got.EstimatedCompletionTokens)
	assert.Equal(t, int64(1100), got.EstimatedCredits) // 100 + 1000
}

func TestEstimateAgentTurn_CompletionEstimateZeroFallsToDefault(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 200, 0)
	require.NoError(t, err)
	assert.Equal(t, DefaultCompletionEstimate, got.EstimatedCompletionTokens)
}

func TestEstimateAgentTurn_CompletionEstimateNegativeFallsToDefault(t *testing.T) {
	pc := &mockPricer{}
	got, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 200, -5)
	require.NoError(t, err)
	assert.Equal(t, DefaultCompletionEstimate, got.EstimatedCompletionTokens)
}

func TestEstimateAgentTurn_NilPricer(t *testing.T) {
	_, err := EstimateAgentTurn(context.Background(), nil, "ali", "qwen-turbo", 200, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pricing calculator is nil")
}

func TestEstimateAgentTurn_PricerError(t *testing.T) {
	pc := &mockPricer{calcErr: errors.New("upstream pricing failure")}
	_, err := EstimateAgentTurn(context.Background(), pc, "ali", "qwen-turbo", 200, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CalculateCost")
	assert.Contains(t, err.Error(), "upstream pricing failure")
}

func TestDefaultCompletionEstimate(t *testing.T) {
	assert.Equal(t, 500, DefaultCompletionEstimate)
}
