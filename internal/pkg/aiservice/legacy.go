package aiservice

import "context"

// Legacy billing skip flag support.
//
// This file provides context key utilities to signal that the new Gateway billing
// middleware should be skipped for a given call. This is needed during the incremental
// migration period (Tasks 9-11) when business code is migrated from legacy billing
// (direct UsageRecord writes) to Gateway-managed billing.
//
// Usage pattern in Task 7:
//
//	ctx := aiservice.WithSkipLegacyBilling(ctx)
//	// ... make the AI call via legacy path (which writes UsageRecord) ...
//	// The new Gateway, if also called, checks ShouldSkipLegacyBilling(ctx)
//	// to avoid double-billing.

// skipLegacyBillingKeyType is an unexported struct used as the context key type.
// Using an opaque struct (instead of a built-in type like int) prevents collisions
// with context keys defined in other packages.
type skipLegacyBillingKeyType struct{}

// ctxKeySkipLegacyBilling is the singleton key instance; unexported to prevent misuse.
var ctxKeySkipLegacyBilling = skipLegacyBillingKeyType{}

// WithSkipLegacyBilling returns a new context with the skip-legacy-billing flag set.
// Call this before delegating to a legacy billing path that has already been migrated
// to Gateway billing, to prevent double-billing during the transition period.
func WithSkipLegacyBilling(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySkipLegacyBilling, true)
}

// ShouldSkipLegacyBilling reports whether the current context carries the
// skip-legacy-billing flag, indicating that the legacy UsageRecord write should be omitted.
func ShouldSkipLegacyBilling(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySkipLegacyBilling).(bool)
	return v
}
