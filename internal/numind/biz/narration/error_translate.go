package narration

import (
	"context"
	"errors"
)

// ErrorCategory classifies an error encountered during tool execution.
// Categories drive friendly-reason templating in the error narration path.
type ErrorCategory string

const (
	ErrCatContextCanceled  ErrorCategory = "context_canceled"
	ErrCatDeadlineExceeded ErrorCategory = "deadline_exceeded"
	ErrCatPermissionDenied ErrorCategory = "permission_denied"
	ErrCatSandboxKilled    ErrorCategory = "sandbox_killed"
	ErrCatGeneric          ErrorCategory = "generic"
)

// friendlyReasons is the locked Chinese mapping (S1-D11). Modification
// requires a manifest decision update. context.Canceled wording is neutral
// "操作被中断" because v1 cannot reliably distinguish user-cancel from
// runtime-cancel via ctx.Err() alone (S1-D11 / s1-4 ADR).
var friendlyReasons = map[ErrorCategory]string{
	ErrCatContextCanceled:  "操作被中断",
	ErrCatDeadlineExceeded: "超过时间限制",
	ErrCatPermissionDenied: "这个操作没有权限",
	ErrCatSandboxKilled:    "运行环境被回收",
	ErrCatGeneric:          "稍后再试一下",
}

// ErrPermissionDenied and ErrSandboxKilled are sentinels reserved for #6
// (permission-pipeline) and #13 (sandbox compliance) to wrap their own errors.
// v1 ClassifyError matches these via errors.Is; downstream wrap with %w.
var (
	ErrPermissionDenied = errors.New("narration: permission denied")
	ErrSandboxKilled    = errors.New("narration: sandbox killed")
)

// ClassifyError takes a (possibly nil) error and returns (category, friendlyReason).
// friendlyReason is the ONLY text the error_template will ever see — err.Error()
// raw text is never rendered to learners (security-critical contract; tested).
//
// Nil contract: ClassifyError(nil) returns (ErrCatGeneric, generic-reason).
// Callers SHOULD gate on err != nil before calling, but the nil path is defined
// so adapter goroutines never panic on a defensive ClassifyError invocation
// where the path's err is statically known to be non-nil but TypeScript-style
// strictness can't prove it.
//
// Classification order (first match wins):
//  1. errors.Is(err, context.Canceled)         → context_canceled
//  2. errors.Is(err, context.DeadlineExceeded) → deadline_exceeded
//  3. errors.Is(err, ErrPermissionDenied)      → permission_denied  (#6 placeholder)
//  4. errors.Is(err, ErrSandboxKilled)         → sandbox_killed     (#13 placeholder)
//  5. default                                  → generic
func ClassifyError(err error) (ErrorCategory, string) {
	if err == nil {
		return ErrCatGeneric, friendlyReasons[ErrCatGeneric]
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrCatContextCanceled, friendlyReasons[ErrCatContextCanceled]
	case errors.Is(err, context.DeadlineExceeded):
		return ErrCatDeadlineExceeded, friendlyReasons[ErrCatDeadlineExceeded]
	case errors.Is(err, ErrPermissionDenied):
		return ErrCatPermissionDenied, friendlyReasons[ErrCatPermissionDenied]
	case errors.Is(err, ErrSandboxKilled):
		return ErrCatSandboxKilled, friendlyReasons[ErrCatSandboxKilled]
	default:
		return ErrCatGeneric, friendlyReasons[ErrCatGeneric]
	}
}
