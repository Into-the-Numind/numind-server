// Package errors provides shared error-classification helpers for the aiservice
// layer. It intentionally lives in the biz sub-tree so that business-layer
// callers (e.g. biz/agent) can import it without creating import cycles with
// the internal/pkg/aiservice package.
package errors

import (
	"errors"
	"regexp"
)

// ErrMultimodalNotSupported is the canonical sentinel for provider-specific
// structured errors. Providers that ship typed errors should wrap this sentinel
// so IsMultimodalNotSupportedError can detect them via errors.Is chain.
// TODO: implement once a provider ships a typed multimodal error.
var ErrMultimodalNotSupported = errors.New("multimodal not supported")

// multimodalNotSupportedPatterns is the ordered list of regular expressions that
// match "this model does not accept image input" error messages across all
// supported providers.
//
// Pattern authorship / provider mapping:
//   - Pattern 0: OpenAI-compatible (DMXAPI / Ali DashScope compatible-mode)
//   - Pattern 1: Ali DashScope native API
//   - Pattern 2: Volc Ark / generic
//   - Pattern 3: DMXAPI aggregated / generic
//   - Pattern 4: generic "does not support vision"
//   - Pattern 5: generic "image input not supported"
//   - Pattern 6: OpenAI-compatible 422 body
//   - Pattern 7: generic "vision not enabled"
//
// When adding a new provider, append a new entry here AND add a unit test case
// in multimodal_test.go. This is an integration-checklist item (spec §R2).
var multimodalNotSupportedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Invalid value:\s*'image_url'`),
	regexp.MustCompile(`(?i)model does not support image`),
	regexp.MustCompile(`(?i)unsupported.*modality.*image`),
	regexp.MustCompile(`(?i)multimodal.*not.*support`),
	regexp.MustCompile(`(?i)does not support.*vision`),
	regexp.MustCompile(`(?i)image.*input.*not.*support`),
	regexp.MustCompile(`(?i)image_url.*not.*allowed`),
	regexp.MustCompile(`(?i)vision.*not.*enabled`),
}

// statusCoder is satisfied by any error that exposes its HTTP status code.
// This lets IsMultimodalNotSupportedError detect structured provider errors
// without importing provider-specific types.
type statusCoder interface {
	StatusCode() int
}

// bodyProvider is satisfied by structured provider errors that carry the raw
// HTTP response body alongside the status code. Used for secondary keyword
// matching when the error message alone is not conclusive.
type bodyProvider interface {
	Body() string
}

// IsMultimodalNotSupportedError reports whether err indicates that the upstream
// provider rejected the request because the model does not accept image inputs.
//
// Detection strategy (in order of preference):
//  1. err.Error() text is matched against 8 regex patterns covering all known
//     provider error formats.
//  2. If err (or any wrapped error) implements StatusCode() int and the code is
//     400 or 422, the Body() string is additionally matched against the same
//     patterns (catches providers that put the message in the body rather than
//     the error string).
//  3. errors.Is chain lookup for future provider-specific sentinels (extension
//     point — currently no-op).
//
// Returns false for nil errors and for any non-multimodal errors (rate limits,
// auth failures, 5xx, etc.).
func IsMultimodalNotSupportedError(err error) bool {
	if err == nil {
		return false
	}

	// Priority 1: match the error string.
	msg := err.Error()
	for _, pat := range multimodalNotSupportedPatterns {
		if pat.MatchString(msg) {
			return true
		}
	}

	// Priority 2: if the error carries an HTTP status, match the body too.
	var sc statusCoder
	if errors.As(err, &sc) {
		code := sc.StatusCode()
		if code == 400 || code == 422 {
			var bp bodyProvider
			if errors.As(err, &bp) {
				body := bp.Body()
				for _, pat := range multimodalNotSupportedPatterns {
					if pat.MatchString(body) {
						return true
					}
				}
			}
		}
	}

	// Priority 3: errors.Is chain (future extension point).
	// Providers that wrap ErrMultimodalNotSupported in their typed errors will be
	// detected here once they adopt the sentinel. Currently always false because no
	// provider ships a typed multimodal error yet.
	if errors.Is(err, ErrMultimodalNotSupported) {
		return true
	}

	return false
}

// MultimodalStripRetryMetric captures observability data for a single
// strip-and-retry event. It is emitted as a Langfuse trace event and logged
// via Zap so that operators can identify which models need capability-matrix
// corrections.
type MultimodalStripRetryMetric struct {
	// ModelKey is the model identifier used for the failing call (e.g. "glm-4-7-251222").
	ModelKey string `json:"model_key"`
	// ProviderID is the numeric DB ID of the AI service provider that returned the error.
	// Set to 0 when provider context is not available (see TODO in runner_strip_retry.go).
	ProviderID int64 `json:"provider_id"`
	// StrippedCount is the number of image MessageParts removed from the messages.
	StrippedCount int `json:"stripped_count"`
	// OrigPromptKB is the rough prompt size in kilobytes before stripping.
	OrigPromptKB int `json:"orig_prompt_kb"`
	// RetrySucceeded is true when the second call (after stripping) succeeded.
	RetrySucceeded bool `json:"retry_succeeded"`
}
