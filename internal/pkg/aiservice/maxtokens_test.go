package aiservice

import "testing"

// TestDefaultMaxTokensFromCapability guards the gateway max_tokens default: a
// resolved model's configured max_output_tokens is used verbatim, and an unset/0
// capability falls back to a non-zero floor. A 0 result is exactly the bug (provider
// default cap that strands a thinking model's answer in reasoning_content).
func TestDefaultMaxTokensFromCapability(t *testing.T) {
	cases := []struct {
		name     string
		declared int
		want     int
	}{
		{"unset falls back", 0, defaultMaxTokensFallback},
		{"negative falls back", -1, defaultMaxTokensFallback},
		{"agnes configured value verbatim", 65500, 65500},
		{"small declared verbatim", 8192, 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultMaxTokensFromCapability(tc.declared); got != tc.want {
				t.Errorf("defaultMaxTokensFromCapability(%d) = %d, want %d", tc.declared, got, tc.want)
			}
		})
	}
	if defaultMaxTokensFromCapability(0) == 0 {
		t.Fatal("defaultMaxTokensFromCapability must never return 0 (would reintroduce the empty-content bug)")
	}
}
