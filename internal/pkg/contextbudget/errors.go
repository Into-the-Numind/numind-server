// Package contextbudget provides neutral utilities for context window management:
// fragment types, token estimation, budget math, and compression planning.
// This package must remain free of business-domain imports (aiservice, sop, chatbot, salesrag, numind/biz).
package contextbudget

import "errors"

// ErrContextConfigInvalid is returned when ModelCapability or BudgetPolicy contains invalid values.
var ErrContextConfigInvalid = errors.New("context budget: invalid config")

// ErrTokenProfileMissing is returned when a required TokenProfile is not provided.
var ErrTokenProfileMissing = errors.New("context budget: token profile missing")

// ErrContextTooLarge is returned when the total estimated tokens exceed the hard threshold
// and no further compression is possible.
var ErrContextTooLarge = errors.New("context budget: context too large")

// ErrCurrentInputTooLarge is returned when the current user input alone exceeds the safe budget.
var ErrCurrentInputTooLarge = errors.New("context budget: current input too large")

// ErrCompressionFailed is returned when the planner cannot produce a feasible plan.
var ErrCompressionFailed = errors.New("context budget: compression failed")
