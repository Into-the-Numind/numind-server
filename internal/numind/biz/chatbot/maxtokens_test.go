package chatbot

import "testing"

// TestResolveChatbotMaxTokens guards the fix for the thinking-model empty-content
// bug: a chatbot stream that sends no explicit max_tokens runs at the provider
// default, and a thinking model (agnes) exhausts that budget during reasoning,
// stranding the answer in reasoning_content with empty content. resolveChatbotMaxTokens
// must always yield a non-zero cap so the chatbot stream sends an explicit limit.
func TestResolveChatbotMaxTokens(t *testing.T) {
	cases := []struct {
		name     string
		declared int
		want     int
	}{
		{"unset falls back", 0, chatbotMaxOutputTokensFallback},
		{"negative falls back", -1, chatbotMaxOutputTokensFallback},
		{"declared used verbatim", 64000, 64000},
		{"declared small used verbatim", 4096, 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveChatbotMaxTokens(tc.declared); got != tc.want {
				t.Errorf("resolveChatbotMaxTokens(%d) = %d, want %d", tc.declared, got, tc.want)
			}
		})
	}
	// The cap must never be 0 — a 0 here is exactly the bug (provider default).
	if resolveChatbotMaxTokens(0) == 0 {
		t.Fatal("resolveChatbotMaxTokens must never return 0 (would reintroduce the empty-content bug)")
	}
}
