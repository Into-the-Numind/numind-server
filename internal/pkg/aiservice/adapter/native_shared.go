package adapter

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// native_shared.go holds scaffolding shared by the two provider-native adapters
// (anthropic_native.go, gemini_native.go). Both live in this package so they can
// reuse the two-client http split, the idle-watchdog stream machinery, the aierr
// wrappers, and InferModelFamily. Wire-format-specific code stays in each
// adapter's own file (D2).

// redactKeyParamRe matches a `key=<value>` query parameter (the Gemini auth
// param) for the fallback path when url.Parse fails. It captures up to the next
// `&` or end-of-string so it does not eat trailing query params.
var redactKeyParamRe = regexp.MustCompile(`([?&]key=)[^&]*`)

// redactGeminiURL returns a copy of u with the value of the `key` query param
// replaced by the literal `REDACTED`, so the live Gemini API key never reaches an
// error-wrap, log line, or Langfuse trace (spec finding #4 — P0 key-in-URL leak).
//
// The Gemini native path authenticates via `?key=<APIKey>` (NOT a Bearer header),
// and the httpclient/aierr error surface embeds the request URL in the error
// string. The adapter therefore builds two URLs: `fullURL` (with the real key,
// used ONLY as the http.NewRequestWithContext argument) and the result of this
// helper (used in EVERY error-wrap / log / trace). A URL with no `key` param is
// returned essentially unchanged; an unparseable string is still scrubbed via a
// regex fallback so the key can never leak even on a malformed input.
func redactGeminiURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		// Fallback: regex-scrub so a malformed URL still cannot leak the key.
		return redactKeyParamRe.ReplaceAllString(u, "${1}REDACTED")
	}
	q := parsed.Query()
	if q.Get("key") == "" {
		// No key param to redact — return unchanged (but normalised by url.Parse).
		return u
	}
	q.Set("key", "REDACTED")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// sanitizeGeminiTransportErr scrubs the live Gemini API key out of a
// transport-level error before it is wrapped/logged/traced (spec finding #4 — P0
// key leak). Go's net/http.Client.Do returns a *url.Error whose Error() string
// embeds the FULL request URL — including the `?key=<APIKey>` auth param — and
// httpclient.Do further wraps it with %w, so the live key reaches err.Error().
// redactGeminiURL only scrubs the URL we build for the error PREFIX; it cannot
// touch the URL Go itself folded into the transport error SUFFIX. This helper
// rewrites the whole error string, replacing every occurrence of the real key
// (and, defensively, any residual `key=<value>` query param) with REDACTED, and
// returns a fresh error so the sanitized form is what every downstream surface
// sees. apiKey "" ⇒ only the regex fallback runs (no-op replace of an empty key).
func sanitizeGeminiTransportErr(apiKey string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "REDACTED")
	}
	// Defensive: also scrub any `key=<value>` left in the string (e.g. a
	// proxy-rewritten URL whose key differs from route.Provider.APIKey).
	msg = redactKeyParamRe.ReplaceAllString(msg, "${1}REDACTED")
	return fmt.Errorf("%s", msg)
}

// assembleAnthropicPromptTokens computes the unified PromptTokens for an
// Anthropic-native response (D4). Anthropic reports input_tokens,
// cache_read_input_tokens, and cache_creation_input_tokens as DISJOINT buckets
// (input_tokens is ONLY the non-cached tail), whereas the unified billing model
// treats PromptTokens as the full prompt out of which the 3-bucket cost formula
// carves the read and write portions. So the unified prompt total is their sum.
// Centralised here so the non-stream parse and the streaming assembly stay in
// lockstep and a single unit test pins the arithmetic.
func assembleAnthropicPromptTokens(input, read, creation int) int {
	return input + read + creation
}

// maxNonZeroInt returns the larger of a and b. It is the defensive "last-largest"
// folding primitive for the Anthropic streaming usage accumulators (finding #3):
// a usage field may appear on message_start, be re-sent or corrected by a DMXAPI
// proxy wrapper in a later chunk, and a stray 0 must never clobber an earlier
// non-zero capture. Since token counts are non-negative, plain max() gives the
// desired "non-zero wins, largest wins" semantics.
func maxNonZeroInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
