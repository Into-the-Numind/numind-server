package middleware

import (
	"context"

	"numind-server/internal/pkg/aiservice/registry"
)

// ContextBudgetService prepares and finalizes context budget metadata.
// Task 6 implements the real Prepare/Finalize logic (spec §6, §5.1).
type ContextBudgetService interface {
	// ContextBudgetServiceMarker is a marker method that keeps this interface
	// non-empty. Task 6 replaces this with Prepare/Finalize method signatures
	// that accept PrepareInput / FinalizeInput structs.
	ContextBudgetServiceMarker()
}

// ContextBudgetCreditService is the credit-budget facade required by the
// ContextBudgetCredits middleware.
// Task 6 wires the real biz/credit binding (Reserve / Reconcile two-phase
// deduction) to this interface.
type ContextBudgetCreditService interface {
	// ContextBudgetCreditServiceMarker is a marker method that keeps this
	// interface non-empty. Task 6 expands it with Reserve / Reconcile
	// / streaming-finalize signatures.
	ContextBudgetCreditServiceMarker()
}

// ContextBudgetCredits returns a Middleware that, in the final system, will:
//  1. Estimate context token budget for the incoming ChatRequest fragments.
//  2. Reserve credits via CreditService.Reserve (pre-deduction).
//  3. After the response, Reconcile credits (multi-退-少补).
//  4. On streaming, finalize credits when the stream terminates.
//
// TODO(Task 6): implement budget validation, compression, Reserve/Reconcile,
// and streaming finalization. For now this is a passthrough so the chain can
// be wired and tested against the new middleware order.
func ContextBudgetCredits(_ Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			// TODO(Task 6): implement budget validation, compression,
			// Reserve / Reconcile, and streaming finalization.
			// For now this is a passthrough so the chain can be wired and tested.
			return next(ctx, route, req)
		}
	}
}
