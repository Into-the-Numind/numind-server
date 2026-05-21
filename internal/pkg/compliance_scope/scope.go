// Package compliance_scope provides the shared ctx key for scope_validator
// bypass. Lives outside biz/compliance and store/ so both can import without
// forming a cycle. See feature #13/14 plan §3 M6.
//
// Design rationale: Go context.WithValue uses interface{} equality for key
// lookup, which requires the *same type* (not just same struct definition).
// Duplicating the key type in store/ and biz/compliance would create two
// distinct types that cannot identify each other's values. This shared
// micro-package ensures a single canonical key type.
package compliance_scope

import "context"

type skipScopeCtxKey struct{}

// WithSkipScope injects a reason that tells scope_validator's GORM
// Before-Query hook to skip the parent_user_id filter check.
// Used by store/compliance.go (self-queries) and admin SDK paths.
func WithSkipScope(ctx context.Context, reason string) context.Context {
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, skipScopeCtxKey{}, reason)
}

// SkipScopeFromCtx returns the skip reason and ok=true if present.
func SkipScopeFromCtx(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(skipScopeCtxKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
