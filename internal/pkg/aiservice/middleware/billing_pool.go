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
