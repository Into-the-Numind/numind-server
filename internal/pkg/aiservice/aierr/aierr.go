// Package aierr carries a semantic classification for upstream LLM provider
// failures, so consumers (agent error recovery, user-facing messages) switch on a
// stable code instead of matching raw provider message substrings.
package aierr

import (
	"errors"
	"fmt"
	"strings"
)

type SemanticCode string

const (
	CodeUnknown               SemanticCode = ""
	CodeContextLengthExceeded SemanticCode = "context_length_exceeded"
	CodeMaxOutputTokens       SemanticCode = "max_output_tokens"
	CodeImageError            SemanticCode = "image_error"
	CodeContentFilter         SemanticCode = "content_filter"
	CodeRateLimited           SemanticCode = "rate_limited"
	CodeAuthError             SemanticCode = "auth_error"
	CodeInvalidParameter      SemanticCode = "invalid_parameter"
	CodeProviderTimeout       SemanticCode = "provider_timeout"
)

// ProviderError wraps an upstream provider failure with a semantic code classified
// from structured fields (code/type/httpStatus) first, message substrings as fallback.
type ProviderError struct {
	Semantic     SemanticCode
	HTTPStatus   int
	ProviderCode string
	ProviderType string
	Message      string
	Err          error // wrapped underlying error (errno / network err); may be nil
}

func (e *ProviderError) Error() string {
	// Keep the raw provider code in the string (not just the semantic code) so logs
	// and substring assertions can see the upstream's own code.
	code := string(e.Semantic)
	if e.ProviderCode != "" {
		code += "/" + e.ProviderCode
	}
	if e.Err != nil {
		return fmt.Sprintf("provider error [%s]: %s: %v", code, e.Message, e.Err)
	}
	return fmt.Sprintf("provider error [%s]: %s", code, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// New builds a ProviderError, classifying the semantic code from its inputs.
func New(httpStatus int, providerCode, providerType, message string, wrapped error) *ProviderError {
	return &ProviderError{
		Semantic:     Classify(httpStatus, providerCode, providerType, message),
		HTTPStatus:   httpStatus,
		ProviderCode: providerCode,
		ProviderType: providerType,
		Message:      message,
		Err:          wrapped,
	}
}

// CodeOf returns the semantic code if err (or anything it wraps) is a *ProviderError,
// else CodeUnknown.
func CodeOf(err error) SemanticCode {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Semantic
	}
	return CodeUnknown
}

// Classify maps provider signals to a semantic code. Structured fields (code/type/
// httpStatus) take priority; message substrings are the legacy fallback. Order
// matters: context-length is checked before max-output (both can mention "max_tokens").
func Classify(httpStatus int, code, typ, message string) SemanticCode {
	c := strings.ToLower(strings.TrimSpace(code))
	t := strings.ToLower(strings.TrimSpace(typ))
	m := strings.ToLower(message)
	// structured-first
	switch {
	case c == "context_length_exceeded" || strings.Contains(c, "context_length"):
		return CodeContextLengthExceeded
	case c == "invalid_image" || strings.Contains(c, "image_decode") || strings.Contains(c, "image_format"):
		return CodeImageError
	case c == "content_filter" || t == "content_filter" || strings.Contains(c, "content_policy"):
		return CodeContentFilter
	case c == "rate_limit_exceeded" || c == "rate_limited" || c == "requests_rate_limit" || httpStatus == 429:
		return CodeRateLimited
	// exact auth codes only — avoid a broad contains("auth") matching unrelated codes.
	case httpStatus == 401 || httpStatus == 403 || c == "invalid_api_key" ||
		c == "authentication_error" || c == "auth_failed" || c == "unauthorized":
		return CodeAuthError
	case httpStatus == 408 || httpStatus == 504:
		return CodeProviderTimeout
	}
	// substring fallback on the message (legacy provider phrasings)
	switch {
	case strings.Contains(m, "context_length_exceeded") || strings.Contains(m, "prompt_too_long") ||
		strings.Contains(m, "context window") || strings.Contains(m, "token limit exceeded") ||
		strings.Contains(m, "maximum context"):
		return CodeContextLengthExceeded
	case strings.Contains(m, "max_tokens") || strings.Contains(m, "max_output") || strings.Contains(m, "output_too_long"):
		return CodeMaxOutputTokens
	case strings.Contains(m, "image_decode") || strings.Contains(m, "image_format") || strings.Contains(m, "invalid_image"):
		return CodeImageError
	case strings.Contains(m, "content_filter") || strings.Contains(m, "content policy"):
		return CodeContentFilter
	case strings.Contains(m, "rate limit") || strings.Contains(m, "too many requests"):
		return CodeRateLimited
	}
	// last: structured invalid_parameter (after substrings so a context-length
	// message still classifies as PTL even when type=invalid_request_error). Use
	// exact codes — a broad contains("invalid") would swallow e.g. invalid_image.
	if t == "invalid_request_error" || c == "invalid_parameter" || c == "invalid_request_error" {
		return CodeInvalidParameter
	}
	return CodeUnknown
}
