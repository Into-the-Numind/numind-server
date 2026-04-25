package contextbudget_test

import (
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

func TestEstimatorWeightedCharClassConservative(t *testing.T) {
	// TokenProfile with distinct per-class rates to verify weighted classification.
	profile := contextbudget.TokenProfile{
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
