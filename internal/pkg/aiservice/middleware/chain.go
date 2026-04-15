// Package middleware implements the AI Gateway middleware chain.
//
// Execution order (request → response):
//
//	Tracing → Billing → Fallback → Retry → Adapter
//
// Each middleware wraps the next handler in the classic functional-composition
// style used throughout Go HTTP middleware. The outermost middleware (Tracing)
// sees the request first and the response last.
package middleware

import (
	"context"
	"time"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Core types
// ----------------------------------------------------------------------------

// Handler is the central function type threading through the middleware chain.
// It receives a resolved route (the selected AI service) and the caller's request,
// and returns an opaque response plus any error.
//
// The concrete request and response types depend on the capability being exercised
// (Chat, Embed, OCR, …).  Adapters (Task 5/6) will type-assert the request and
// return the appropriate typed response; middleware operates on the opaque interface
// without inspecting the concrete type.
type Handler func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error)

// Middleware wraps a Handler to add a cross-cutting concern.
type Middleware func(next Handler) Handler

// ----------------------------------------------------------------------------
// Chain
// ----------------------------------------------------------------------------

// Chain composes multiple Middleware into one.  Given [a, b, c], the resulting
// middleware applies them as a(b(c(next))), so 'a' is the outermost layer
// (receives the request first and the response last).
func Chain(mws ...Middleware) Middleware {
	return func(next Handler) Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}

// ----------------------------------------------------------------------------
// Dependency injection
// ----------------------------------------------------------------------------

// Clock is a thin abstraction over time.Now() so tests can inject a fixed clock.
type Clock interface {
	Now() time.Time
}

// realClock delegates to time.Now.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RealClock is the production Clock implementation.
var RealClock Clock = realClock{}

// UsageStore defines the minimal persistence interface needed by the Billing
// middleware.  The concrete implementation (Task 8) will be a thin GORM wrapper;
// tests can inject a mock.
type UsageStore interface {
	// CreateUsageRecord persists a single usage record.  Failures are non-fatal;
	// callers should log the error but must not propagate it to the user.
	CreateUsageRecord(ctx context.Context, r *model.UsageRecord) error
}

// Deps carries the injected dependencies required by the standard middleware set.
// Zero values are safe: if Langfuse or UsageStore is nil the corresponding
// middleware degrades gracefully (Tracing skips SDK calls; Billing logs only).
type Deps struct {
	// Langfuse is the Langfuse global client used by the Tracing middleware.
	// When nil or disabled the Tracing middleware is a no-op.
	Langfuse *langfuse.Client

	// UsageStore is used by the Billing middleware to persist UsageRecord rows.
	// When nil Billing still prepares the record but logs "no UsageStore configured".
	UsageStore UsageStore

	// Resolver is used by the Fallback middleware to obtain the fallback route.
	// When nil Fallback becomes a passthrough (no fallback logic).
	Resolver registry.Registry

	// Clock is used by the Billing middleware for record timestamps.
	// When nil RealClock is used.
	Clock Clock

	// Logger is the structured logger used across middlewares.  When nil the
	// package-level log.Warnw / log.Errorw functions are used directly.
	Logger interface {
		Warnw(msg string, keysAndValues ...interface{})
		Errorw(msg string, keysAndValues ...interface{})
	}
}

func (d Deps) clock() Clock {
	if d.Clock != nil {
		return d.Clock
	}
	return RealClock
}

func (d Deps) warnw(msg string, kv ...interface{}) {
	if d.Logger != nil {
		d.Logger.Warnw(msg, kv...)
		return
	}
	log.Warnw(msg, kv...)
}

func (d Deps) errorw(msg string, kv ...interface{}) {
	if d.Logger != nil {
		d.Logger.Errorw(msg, kv...)
		return
	}
	log.Errorw(msg, kv...)
}

// ----------------------------------------------------------------------------
// BuildDefault
// ----------------------------------------------------------------------------

// BuildDefault assembles the standard middleware stack in the order specified by
// spec §6.1:
//
//	Tracing → Billing → Fallback → Retry → (Adapter — not wrapped here)
//
// The caller wraps the adapter handler (innermost) with the returned Middleware.
func BuildDefault(deps Deps) Middleware {
	return Chain(
		Tracing(deps),
		Billing(deps),
		Fallback(deps),
		Retry(deps),
	)
}
