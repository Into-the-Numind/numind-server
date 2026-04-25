package model

import "testing"

func TestContextBudgetModelsHaveTableNames(t *testing.T) {
	cases := map[string]string{
		"token profile": TokenEstimationProfile{}.TableName(),
		"budget policy": ContextBudgetPolicy{}.TableName(),
		"summary":       ContextSummary{}.TableName(),
		"event":         ContextBudgetEvent{}.TableName(),
	}
	want := map[string]string{
		"token profile": "token_estimation_profile",
		"budget policy": "context_budget_policy",
		"summary":       "context_summary",
		"event":         "context_budget_event",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Fatalf("%s table name = %q, want %q", name, got, want[name])
		}
	}
}
