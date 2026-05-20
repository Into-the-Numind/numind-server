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

// multiErr lets us wrap two unrelated sentinels (via Unwrap []error, Go 1.20+)
// to actually exercise the priority order in ClassifyError's switch.
type multiErr struct{ errs []error }

func (m *multiErr) Error() string   { return "multi" }
func (m *multiErr) Unwrap() []error { return m.errs }

func TestClassifyError_PriorityOrder_CanceledBeatsPermission(t *testing.T) {
	// Construct an error that satisfies BOTH errors.Is(context.Canceled) AND
	// errors.Is(ErrPermissionDenied). ClassifyError's switch checks Canceled
	// FIRST, so it must win.
	multi := &multiErr{errs: []error{context.Canceled, ErrPermissionDenied}}
	cat, _ := ClassifyError(multi)
	if cat != ErrCatContextCanceled {
		t.Errorf("expected ErrCatContextCanceled (canceled-first priority), got %q", cat)
	}
}

func TestClassifyError_PriorityOrder_DeadlineBeatsSandbox(t *testing.T) {
	multi := &multiErr{errs: []error{context.DeadlineExceeded, ErrSandboxKilled}}
	cat, _ := ClassifyError(multi)
	if cat != ErrCatDeadlineExceeded {
		t.Errorf("expected ErrCatDeadlineExceeded (deadline-before-sandbox), got %q", cat)
	}
}

func TestClassifyError_LeakGuard_SentinelWrapMessage(t *testing.T) {
	// Strengthen the raw-leak test by also asserting that ANY wrap text around
	// a sentinel does not appear in the friendly reason.
	wrapped := fmt.Errorf("user input leaked %w", ErrPermissionDenied)
	_, reason := ClassifyError(wrapped)
	if strings.Contains(reason, "leaked") || strings.Contains(reason, "user input") {
		t.Errorf("friendly reason leaked wrap text: %q", reason)
	}
}
