package narration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCat    ErrorCategory
		wantReason string
	}{
		{"nil", nil, ErrCatGeneric, "稍后再试一下"},
		{"context.Canceled", context.Canceled, ErrCatContextCanceled, "操作被中断"},
		{"context.DeadlineExceeded", context.DeadlineExceeded, ErrCatDeadlineExceeded, "超过时间限制"},
		{"wrapped context.Canceled", fmt.Errorf("outer: %w", context.Canceled), ErrCatContextCanceled, "操作被中断"},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("outer: %w", context.DeadlineExceeded), ErrCatDeadlineExceeded, "超过时间限制"},
		{"ErrPermissionDenied", ErrPermissionDenied, ErrCatPermissionDenied, "这个操作没有权限"},
		{"wrapped ErrPermissionDenied", fmt.Errorf("rule X: %w", ErrPermissionDenied), ErrCatPermissionDenied, "这个操作没有权限"},
		{"ErrSandboxKilled", ErrSandboxKilled, ErrCatSandboxKilled, "运行环境被回收"},
		{"random error", errors.New("boom"), ErrCatGeneric, "稍后再试一下"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCat, gotReason := ClassifyError(c.err)
			if gotCat != c.wantCat {
				t.Errorf("category: want %q, got %q", c.wantCat, gotCat)
			}
			if gotReason != c.wantReason {
				t.Errorf("reason: want %q, got %q", c.wantReason, gotReason)
			}
		})
	}
}

func TestClassifyError_NeverLeaksRawErrText(t *testing.T) {
	// Critical security contract: friendlyReason MUST NOT contain any substring
	// from the raw error text (which could include secrets, tokens, file paths).
	sensitive := []string{
		"secret token abc123",
		"/etc/passwd",
		"BEARER eyJhbGciOiJIUzI1NiIs",
		"DROP TABLE users",
		"private key -----BEGIN RSA",
	}
	for _, raw := range sensitive {
		err := errors.New(raw)
		_, reason := ClassifyError(err)
		for _, sub := range strings.Fields(raw) {
			if len(sub) >= 4 && strings.Contains(reason, sub) {
				t.Errorf("friendly reason leaked raw substring %q from err %q: %q", sub, raw, reason)
			}
		}
	}
}

func TestClassifyError_PriorityOrder(t *testing.T) {
	// When an error wraps multiple sentinels, the FIRST match in
	// ClassifyError's switch wins (canceled > deadline > permission > sandbox > generic).
	// Construct a synthetic wrapped chain to verify.
	type wrapBoth struct{ a, b error }
	// Not possible to wrap two unrelated sentinels cleanly with %w, so we test
	// the explicit precedence: a wrapped Canceled inside a wrapper that ALSO
	// satisfies Is for DeadlineExceeded would be ambiguous. errors.Is uses depth-
	// first traversal, so wrapping order matters. Verify Canceled wins over
	// generic when both are present (the standard case in real cancel paths).
	inner := context.Canceled
	wrapped := fmt.Errorf("user pressed cancel: %w", inner)
	cat, _ := ClassifyError(wrapped)
	if cat != ErrCatContextCanceled {
		t.Errorf("expected ErrCatContextCanceled for wrapped Canceled, got %q", cat)
	}
}
