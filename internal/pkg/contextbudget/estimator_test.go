package contextbudget_test

import (
	"encoding/json"
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

// defaultTestProfile returns a reusable TokenProfile for estimator tests.
func defaultTestProfile() contextbudget.TokenProfile {
	return contextbudget.TokenProfile{
		Method:                 "weighted_char",
		MessageOverheadTokens:  4,
		FragmentOverheadTokens: 2,
		SafetyMultiplier:       1.0,
		CalibrationMultiplier:  1.0,
		Classes: map[string]contextbudget.TokenClass{
			"zh":             {TokenPerChar: 1.5},
			"en":             {TokenPerChar: 0.25},
			"code":           {TokenPerChar: 0.30},
			"json":           {TokenPerChar: 0.28},
			"markdown_table": {TokenPerChar: 0.30},
			"symbol":         {TokenPerChar: 0.20},
			"mixed":          {TokenPerChar: 0.50},
		},
	}
}

func TestEstimatorWeightedCharClassConservative(t *testing.T) {
	// TokenProfile with distinct per-class rates to verify weighted classification.
	profile := defaultTestProfile()

	tests := []struct {
		name        string
		content     string
		wantAtLeast int // conservative: estimate >= this
	}{
		{
			name:        "pure chinese",
			content:     "你好世界",
			wantAtLeast: 6, // 4 chars * 1.5 = 6
		},
		{
			name:        "pure english",
			content:     "hello world",
			wantAtLeast: 2, // 11 chars * 0.25 = 2.75 -> ceil 3, but at least 2
		},
		{
			name:        "code block",
			content:     "```go\nfunc main() {}\n```",
			wantAtLeast: 1,
		},
		{
			name:        "json text",
			content:     `{"key": "value", "num": 42}`,
			wantAtLeast: 1,
		},
		{
			name:        "markdown table",
			content:     "| col1 | col2 |\n|------|------|\n| a    | b    |",
			wantAtLeast: 1,
		},
		{
			// Mixed Chinese + English: content has both CJK and ASCII letters,
			// so no single class dominates; classified as mixed/weighted split.
			// zh chars get 1.5 t/c, en chars get 0.25 t/c — the combined estimate
			// should be at least 1 token above overhead-only.
			name:        "mixed Chinese English",
			content:     "你好 hello 世界 world 测试 test",
			wantAtLeast: 5, // conservatively: 6 zh chars * 1.5 = 9 tokens alone
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fragment := contextbudget.ContextFragment{
				ID:          "f1",
				Role:        contextbudget.RoleWorking,
				Content:     tc.content,
				ContentType: contextbudget.ContentText,
			}
			result := contextbudget.EstimateFragments(
				[]contextbudget.ContextFragment{fragment},
				profile,
				0, // no fixed overhead
				1, // 1 message
			)
			if result.PromptTokens < tc.wantAtLeast {
				t.Errorf("content %q: got PromptTokens=%d, want >= %d",
					tc.content, result.PromptTokens, tc.wantAtLeast)
			}
			// Per-fragment map must have the fragment's ID
			if _, ok := result.PerFragmentMap["f1"]; !ok {
				t.Errorf("PerFragmentMap missing fragment ID 'f1'")
			}
			// Conservative: per-fragment tokens >= 1
			if result.PerFragmentMap["f1"] < 1 {
				t.Errorf("fragment token estimate must be >= 1, got %d", result.PerFragmentMap["f1"])
			}
		})
	}
}

// TestEstimatorSafetyMultiplierClampsToAtLeastOne verifies that a SafetyMultiplier
// below 1.0 is clamped to 1.0, preserving the conservative-estimate guarantee.
// (Per TokenProfile doc: SafetyMultiplier should be >= 1.0; values below violate the
// invariant and must not cause the estimator to under-estimate.)
func TestEstimatorSafetyMultiplierClampsToAtLeastOne(t *testing.T) {
	content := "hello world this is a test fragment for budget estimation"

	fragment := contextbudget.ContextFragment{
		ID:          "f-clamp",
		Role:        contextbudget.RoleWorking,
		Content:     content,
		ContentType: contextbudget.ContentText,
	}

	baseProfile := defaultTestProfile()
	baseProfile.SafetyMultiplier = 1.0

	// A profile with SafetyMultiplier=0.5 — violates the doc invariant.
	// The estimator must clamp this to 1.0, so the result should equal the safety=1.0 result.
	lowSafetyProfile := defaultTestProfile()
	lowSafetyProfile.SafetyMultiplier = 0.5

	resultBase := contextbudget.EstimateFragments([]contextbudget.ContextFragment{fragment}, baseProfile, 0, 0)
	resultLow := contextbudget.EstimateFragments([]contextbudget.ContextFragment{fragment}, lowSafetyProfile, 0, 0)

	if resultLow.PromptTokens < resultBase.PromptTokens {
		t.Errorf("SafetyMultiplier=0.5 should be clamped to 1.0: got PromptTokens=%d (base with safety=1.0: %d); estimator is under-estimating",
			resultLow.PromptTokens, resultBase.PromptTokens)
	}
	// Specifically, the clamped result should equal the safety=1.0 result.
	if resultLow.PromptTokens != resultBase.PromptTokens {
		t.Errorf("SafetyMultiplier=0.5 should produce identical result to safety=1.0 after clamping: got %d, want %d",
			resultLow.PromptTokens, resultBase.PromptTokens)
	}
}

func TestEstimatorUsesCalibrationBucketForRawPromptSize(t *testing.T) {
	var profile contextbudget.TokenProfile
	if err := json.Unmarshal([]byte(`{
		"method": "bucketed",
		"message_overhead_tokens": 0,
		"fragment_overhead_tokens": 0,
		"safety_multiplier": 1.0,
		"calibration_multiplier": 1.0,
		"classes": {
			"en": {"token_per_char": 1.0}
		},
		"calibration_buckets": [
			{"max_raw_tokens": 20, "multiplier": 1.10},
			{"min_raw_tokens": 21, "multiplier": 2.00}
		]
	}`), &profile); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}

	short := contextbudget.ContextFragment{ID: "short", Content: "abcdefghij"}
	long := contextbudget.ContextFragment{ID: "long", Content: "abcdefghijklmnopqrstuvwxyz1234"}

	shortResult := contextbudget.EstimateFragments([]contextbudget.ContextFragment{short}, profile, 0, 0)
	if shortResult.PromptTokens != 11 {
		t.Fatalf("short prompt should use first bucket multiplier: got %d, want 11", shortResult.PromptTokens)
	}

	longResult := contextbudget.EstimateFragments([]contextbudget.ContextFragment{long}, profile, 0, 0)
	if longResult.PromptTokens != 60 {
		t.Fatalf("long prompt should use second bucket multiplier: got %d, want 60", longResult.PromptTokens)
	}
}
