package billing

import (
	"testing"
)

// TestTokenUsage_Normalize_CachedPromptTokens verifies that Normalize() flattens
// the provider cache-hit wire shapes into the flat CachedPromptTokens field with
// never-overwrite semantics: nested (prompt_tokens_details.cached_tokens) is
// preferred, then DeepSeek-native flat (prompt_cache_hit_tokens), and a value
// already present in CachedPromptTokens is never clobbered. Absence of every
// cache field leaves CachedPromptTokens at 0 (zero-regression).
func TestTokenUsage_Normalize_CachedPromptTokens(t *testing.T) {
	tests := []struct {
		name     string
		usage    TokenUsage
		expected int
	}{
		{
			name: "nested_to_flat",
			usage: TokenUsage{
				PromptTokens:        100,
				PromptTokensDetails: promptTokensDetails{CachedTokens: 40},
			},
			expected: 40,
		},
		{
			name: "deepseek_native_flat_to_flat",
			usage: TokenUsage{
				PromptTokens:         100,
				PromptCacheHitTokens: 30,
			},
			expected: 30,
		},
		{
			name: "none_stays_zero",
			usage: TokenUsage{
				PromptTokens: 100,
			},
			expected: 0,
		},
		{
			name: "preset_flat_preserved_not_overwritten",
			usage: TokenUsage{
				PromptTokens:         100,
				CachedPromptTokens:   55,
				PromptTokensDetails:  promptTokensDetails{CachedTokens: 40},
				PromptCacheHitTokens: 30,
			},
			expected: 55,
		},
		{
			name: "nested_preferred_over_native_flat",
			usage: TokenUsage{
				PromptTokens:         100,
				PromptTokensDetails:  promptTokensDetails{CachedTokens: 40},
				PromptCacheHitTokens: 30,
			},
			expected: 40,
		},
		{
			name: "nested_zero_falls_back_to_native_flat",
			usage: TokenUsage{
				PromptTokens:         100,
				PromptTokensDetails:  promptTokensDetails{CachedTokens: 0},
				PromptCacheHitTokens: 30,
			},
			expected: 30,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := tt.usage
			u.Normalize()
			if u.CachedPromptTokens != tt.expected {
				t.Errorf("Normalize() CachedPromptTokens = %d, want %d", u.CachedPromptTokens, tt.expected)
			}
		})
	}
}

// TestTokenUsage_Normalize_NilSafe ensures Normalize() on a nil receiver does not panic.
func TestTokenUsage_Normalize_NilSafe(t *testing.T) {
	var u *TokenUsage
	u.Normalize() // must not panic
}

// TestExtractUsageFromSSEData_CachedTokens proves the legacy SSE-parse path
// (used by ali/volc executor inline unmarshal and ExtractUsageFromSSEData)
// decodes provider cache-hit wire keys into CachedPromptTokens via Normalize().
// This is the carrier T4 relies on; without these fields the value would be 0.
func TestExtractUsageFromSSEData_CachedTokens(t *testing.T) {
	tests := []struct {
		name           string
		data           string
		wantCachedToks int
	}{
		{
			name:           "openai_nested_cached_tokens",
			data:           `{"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40}}}`,
			wantCachedToks: 40,
		},
		{
			name:           "deepseek_native_flat_cache_hit",
			data:           `{"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":30}}`,
			wantCachedToks: 30,
		},
		{
			name:           "no_cache_fields_stays_zero",
			data:           `{"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`,
			wantCachedToks: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := ExtractUsageFromSSEData(tt.data)
			if u == nil {
				t.Fatalf("ExtractUsageFromSSEData returned nil for %q", tt.data)
			}
			if u.CachedPromptTokens != tt.wantCachedToks {
				t.Errorf("CachedPromptTokens = %d, want %d", u.CachedPromptTokens, tt.wantCachedToks)
			}
		})
	}
}
