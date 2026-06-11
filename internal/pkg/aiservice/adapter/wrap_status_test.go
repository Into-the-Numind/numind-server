package adapter

import (
	"errors"
	"testing"

	"numind-server/internal/pkg/errno"
)

// TestWrapHTTPStatusErr_Retryability verifies the T3 change: 429/408 + 5xx map to
// errno.ErrAIProviderError (which middleware.retryableError treats as retryable,
// engaging Retry/Fallback), while other 4xx stay non-retryable plain errors.
func TestWrapHTTPStatusErr_Retryability(t *testing.T) {
	cases := []struct {
		status      int
		wantProvErr bool // true => errors.Is(err, ErrAIProviderError) => retryable/fail-over
	}{
		{429, true},  // rate limit — the whole point of T3 (free bge 5/min)
		{408, true},  // request timeout
		{500, true},  // server error (regression: pre-existing 5xx behavior)
		{503, true},  // service unavailable
		{400, false}, // bad request — genuine client error
		{401, false}, // unauthorized
		{403, false}, // forbidden
		{404, false}, // not found
		{422, false}, // unprocessable
	}
	for _, c := range cases {
		err := wrapHTTPStatusErr("op", c.status, []byte("body"))
		if err == nil {
			t.Fatalf("status %d: expected non-nil error", c.status)
		}
		got := errors.Is(err, errno.ErrAIProviderError)
		if got != c.wantProvErr {
			t.Errorf("status %d: errors.Is(ErrAIProviderError)=%v; want %v (err=%v)", c.status, got, c.wantProvErr, err)
		}
	}
}
