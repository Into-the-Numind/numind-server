package aierr

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		code       string
		typ        string
		message    string
		want       SemanticCode
	}{
		{
			name: "structured code context_length_exceeded",
			code: "context_length_exceeded",
			want: CodeContextLengthExceeded,
		},
		{
			name: "structured code substring context_length",
			code: "model_context_length_error",
			want: CodeContextLengthExceeded,
		},
		{
			name:       "httpStatus 429 -> rate_limited",
			httpStatus: 429,
			want:       CodeRateLimited,
		},
		{
			name:       "httpStatus 401 -> auth",
			httpStatus: 401,
			want:       CodeAuthError,
		},
		{
			name:       "httpStatus 403 -> auth",
			httpStatus: 403,
			want:       CodeAuthError,
		},
		{
			name:       "httpStatus 504 -> provider_timeout",
			httpStatus: 504,
			want:       CodeProviderTimeout,
		},
		{
			name:    "max_tokens message -> max_output",
			message: "This model's maximum response length is exceeded: max_tokens too small",
			want:    CodeMaxOutputTokens,
		},
		{
			name:    "image_decode message -> image",
			message: "image_decode failed: corrupt PNG",
			want:    CodeImageError,
		},
		{
			name:    "type invalid_request_error + context-length message -> still PTL (not invalid_parameter)",
			typ:     "invalid_request_error",
			message: "context window exceeded: 12000 > 8192",
			want:    CodeContextLengthExceeded,
		},
		{
			name: "type invalid_request_error with no other signal -> invalid_parameter",
			typ:  "invalid_request_error",
			want: CodeInvalidParameter,
		},
		{
			name:    "content_filter message -> content_filter",
			message: "content_filter triggered",
			want:    CodeContentFilter,
		},
		{
			name: "structured content_filter type",
			typ:  "content_filter",
			want: CodeContentFilter,
		},
		{
			name:    "rate limit message -> rate_limited",
			message: "you have hit the rate limit",
			want:    CodeRateLimited,
		},
		{
			name: "unknown -> empty",
			want: CodeUnknown,
		},
		{
			name:    "unrelated message -> empty",
			message: "some unrelated upstream hiccup",
			want:    CodeUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.httpStatus, c.code, c.typ, c.message)
			if got != c.want {
				t.Errorf("Classify(%d, %q, %q, %q) = %q, want %q",
					c.httpStatus, c.code, c.typ, c.message, got, c.want)
			}
		})
	}
}

func TestNew_ClassifiesAndStoresFields(t *testing.T) {
	pe := New(429, "rate_limit_exceeded", "", "slow down", nil)
	if pe.Semantic != CodeRateLimited {
		t.Errorf("Semantic = %q, want %q", pe.Semantic, CodeRateLimited)
	}
	if pe.HTTPStatus != 429 || pe.ProviderCode != "rate_limit_exceeded" || pe.Message != "slow down" {
		t.Errorf("fields not stored verbatim: %+v", pe)
	}
}

func TestCodeOf(t *testing.T) {
	// direct *ProviderError
	pe := New(0, "context_length_exceeded", "", "", nil)
	if got := CodeOf(pe); got != CodeContextLengthExceeded {
		t.Errorf("CodeOf(direct) = %q, want %q", got, CodeContextLengthExceeded)
	}

	// wrapped via fmt.Errorf %w -> errors.As must traverse
	wrapped := fmt.Errorf("dmxapi.Chat: %w", pe)
	if got := CodeOf(wrapped); got != CodeContextLengthExceeded {
		t.Errorf("CodeOf(wrapped) = %q, want %q", got, CodeContextLengthExceeded)
	}

	// non-ProviderError -> CodeUnknown
	if got := CodeOf(errors.New("plain error")); got != CodeUnknown {
		t.Errorf("CodeOf(plain) = %q, want %q", got, CodeUnknown)
	}

	// nil -> CodeUnknown
	if got := CodeOf(nil); got != CodeUnknown {
		t.Errorf("CodeOf(nil) = %q, want %q", got, CodeUnknown)
	}
}

func TestProviderError_UnwrapPreservesWrapped(t *testing.T) {
	inner := errors.New("inner errno")
	pe := New(429, "", "", "body", inner)
	if !errors.Is(pe, inner) {
		t.Errorf("errors.Is should find the wrapped inner error through Unwrap")
	}
}
