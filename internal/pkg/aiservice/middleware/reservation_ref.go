package middleware

import "context"

// ctxKeyReservationRef carries a self-describing business reference id (e.g.
// "sop_run:5:9") to be written into credit_reservation.reference_id at reserve
// time. It is a distinct unexported key type in package middleware — separate
// from billing.billingCtxKey (package internal/pkg/billing) — so a downstream
// billing.WithBilling call cannot overwrite it (different packages → distinct
// context key identities).
type ctxKeyReservationRef struct{}

// WithReservationRef injects a business reference id into ctx. "" = no-op.
func WithReservationRef(ctx context.Context, refID string) context.Context {
	if refID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyReservationRef{}, refID)
}

// ReservationRefFromCtx returns the injected reference id, or "" if absent.
func ReservationRefFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyReservationRef{}).(string); ok {
		return v
	}
	return ""
}
