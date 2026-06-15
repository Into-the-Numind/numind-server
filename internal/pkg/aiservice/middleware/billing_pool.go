package middleware

import "context"

// ctxKeyBillingPool selects WHICH credit pool a reservation draws from:
//   - ""           → default three-pool (trial→cycle→booster), unchanged behaviour
//   - "admin_test" → parent-account B2B "试聊" pool (credit_admin_test_grant)
//
// Distinct unexported key type in package middleware — separate from
// billing.billingCtxKey (package internal/pkg/billing) and ctxKeyReservationRef —
// so a downstream billing.WithBilling call cannot overwrite it (different
// packages → distinct context key identities). Mirrors WithReservationRef.
type ctxKeyBillingPool struct{}

// BillingPoolAdminTest is the pool identifier for the parent-account Builder
// 试聊 (test-chat) credit pool. Empty string means the default three-pool.
const BillingPoolAdminTest = "admin_test"

// WithBillingPool injects a billing-pool selector into ctx. "" = no-op (default
// three-pool).
func WithBillingPool(ctx context.Context, pool string) context.Context {
	if pool == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyBillingPool{}, pool)
}

// BillingPoolFromCtx returns the injected pool selector, or "" (default
// three-pool) if absent.
func BillingPoolFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyBillingPool{}).(string); ok {
		return v
	}
	return ""
}

// ctxKeyGatewayBillingOnly marks a request as "bill, but do NOT compress or
// replace Messages". Used by the agent runner: agent mode manages its own
// context (adapter_compactv2) and its ReAct messages carry tool_calls /
// tool_call_id / reasoning_content that the fragment renderer would drop —
// running them through Prepare's render-and-replace breaks tool-calling
// (HTTP 400). In bill-only mode ContextBudgetCredits estimates tokens directly
// from chatReq.Messages and runs Reserve/Reconcile, leaving Messages untouched.
type ctxKeyGatewayBillingOnly struct{}

// WithGatewayBillingOnly flags ctx so ContextBudgetCredits bills without
// compressing/replacing Messages.
func WithGatewayBillingOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyGatewayBillingOnly{}, true)
}

// GatewayBillingOnlyFromCtx reports whether bill-only mode is active.
func GatewayBillingOnlyFromCtx(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyGatewayBillingOnly{}).(bool)
	return v
}

// WithoutGatewayBillingOnly clears the bill-only flag (sets it to false),
// overriding any inherited WithGatewayBillingOnly. Used by system-internal,
// non-user-billed LLM calls (e.g. session-title generation) whose ctx may be
// derived (context.WithoutCancel) from an agent request that set bill-only:
// without this, ContextBudgetCredits would skip its no-fragment pass-through
// and run the bill-only Reserve path, billing the user. Pair it with a request
// that carries no ContextFragments and a zeroed userID so the gateway takes the
// pass-through branch and never reserves.
func WithoutGatewayBillingOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyGatewayBillingOnly{}, false)
}
