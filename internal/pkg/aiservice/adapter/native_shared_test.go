package adapter

import (
	"strings"
	"testing"
)

// TestRedactGeminiURL asserts that the live ?key= query param is stripped to
// key=REDACTED before any URL reaches an error-wrap / log / trace surface
// (spec finding #4 — P0 key-in-URL leak). The fake key must NEVER appear in
// the returned string; key=REDACTED MUST appear.
func TestRedactGeminiURL(t *testing.T) {
	const fakeKey = "AIzaSyFAKEKEY1234567890abcDEF"

	cases := []struct {
		name string
		in   string
	}{
		{
			name: "non-stream generateContent",
			in:   "https://www.dmxapi.cn/v1beta/models/gemini-2.5-flash:generateContent?key=" + fakeKey,
		},
		{
			name: "stream with alt=sse after key",
			in:   "https://www.dmxapi.cn/v1beta/models/gemini-2.5-flash:streamGenerateContent?key=" + fakeKey + "&alt=sse",
		},
		{
			name: "key not the first query param",
			in:   "https://www.dmxapi.cn/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse&key=" + fakeKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactGeminiURL(tc.in)
			if strings.Contains(got, fakeKey) {
				t.Fatalf("redactGeminiURL leaked the key: %q", got)
			}
			if !strings.Contains(got, "key=REDACTED") {
				t.Fatalf("redactGeminiURL did not produce key=REDACTED: %q", got)
			}
			// The non-key path of the URL must survive intact.
			if !strings.Contains(got, "/v1beta/models/gemini-2.5-flash:") {
				t.Fatalf("redactGeminiURL mangled the path: %q", got)
			}
		})
	}
}

// TestRedactGeminiURL_NoKey is a defensive no-op case: a URL without a key
// query param must be returned essentially unchanged (and never panic).
func TestRedactGeminiURL_NoKey(t *testing.T) {
	in := "https://www.dmxapi.cn/v1beta/models/gemini-2.5-flash:generateContent"
	got := redactGeminiURL(in)
	if strings.Contains(got, "key=REDACTED") {
		t.Fatalf("redactGeminiURL injected REDACTED into a keyless URL: %q", got)
	}
	if !strings.Contains(got, "/v1beta/models/gemini-2.5-flash:generateContent") {
		t.Fatalf("redactGeminiURL mangled a keyless URL: %q", got)
	}
}

// TestRedactGeminiURL_Unparseable feeds a non-URL string; the helper must
// degrade safely (redact via fallback, never leak, never panic).
func TestRedactGeminiURL_Unparseable(t *testing.T) {
	const fakeKey = "SECRET_THAT_MUST_NOT_LEAK"
	in := "::not-a-url::?key=" + fakeKey
	got := redactGeminiURL(in)
	if strings.Contains(got, fakeKey) {
		t.Fatalf("redactGeminiURL leaked key from an unparseable string: %q", got)
	}
}

// TestAssembleAnthropicPromptTokens locks the D4 normalization arithmetic:
// Anthropic reports input/cache_read/cache_creation DISJOINT, so the unified
// PromptTokens MUST be their sum (the 3-bucket cost formula carves read+write
// back out of this total). T5 relies on this helper for both the non-stream
// and the streaming assembly.
func TestAssembleAnthropicPromptTokens(t *testing.T) {
	cases := []struct {
		input, read, creation, want int
	}{
		{0, 0, 0, 0},
		{100, 0, 0, 100},
		{37, 2763, 0, 2800},   // pure read hit
		{37, 0, 2763, 2800},   // pure creation
		{37, 1000, 763, 1800}, // mixed
	}
	for _, c := range cases {
		if got := assembleAnthropicPromptTokens(c.input, c.read, c.creation); got != c.want {
			t.Errorf("assembleAnthropicPromptTokens(%d,%d,%d)=%d want %d",
				c.input, c.read, c.creation, got, c.want)
		}
	}
}

// TestMaxNonZeroInt locks the defensive max() capture helper used by the
// Anthropic streaming usage accumulators (finding #3): a later 0 must never
// overwrite an earlier non-zero value, and the largest non-zero wins.
func TestMaxNonZeroInt(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 0, 0},
		{5, 0, 5},
		{0, 5, 5},
		{3, 7, 7},
		{7, 3, 7},
	}
	for _, c := range cases {
		if got := maxNonZeroInt(c.a, c.b); got != c.want {
			t.Errorf("maxNonZeroInt(%d,%d)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
