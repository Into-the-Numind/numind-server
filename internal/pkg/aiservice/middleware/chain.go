// Package middleware implements the AI Gateway middleware chain.
//
// Execution order (request → response):
//
//	Tracing → Fallback → ContextBudgetCredits → Billing → Retry → Adapter
//
// Each middleware wraps the next handler in the classic functional-composition
// style used throughout Go HTTP middleware. The outermost middleware (Tracing)
// sees the request first and the response last.
//
// Billing sits inside Fallback so that when Fallback switches to a backup
// provider, the Billing middleware records the fallback provider/model/pricing
// rather than the primary route.
package middleware

import (
	"context"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
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

	// GetPricingRule returns the matching pricing_rule for a
	// (serviceType, provider, modelName) triple, with exact-model match preferred
	// over the default empty-model fallback.
	// Returns gorm.ErrRecordNotFound when no rule exists (billing falls back to
	// leaving snapshots nil — cost recorded as 0, non-fatal).
	GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error)
}

// CompletionEstimator returns a historical per-(provider, model) completion-token
// estimate used to replace the conservative max_tokens default in
// ContextBudgetCredits.doReserveBudget. Implementations MUST return hasData=false
// when no usable historical data exists so the caller can fall back to the
// policy default (ReservedOutputTokens).
//
// The concrete implementation lives in internal/numind/biz/credit; this
// interface is here so the middleware package has no cross-package dependency.
type CompletionEstimator interface {
	Estimate(ctx context.Context, provider, model string) (tokens int, hasData bool)
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

	// ContextBudget is the context-budget service used by ContextBudgetCredits.
	// When nil ContextBudgetCredits becomes a passthrough (no budget checks).
	// Task 6 wires the real implementation.
	ContextBudget ContextBudgetService

	// CreditService is the credit-budget facade used by ContextBudgetCredits.
	// When nil ContextBudgetCredits becomes a passthrough (no credit reservation).
	// Task 6 wires the real biz/credit binding.
	CreditService ContextBudgetCreditService

	// CompletionEstimator provides a per-model historical completion-token
	// estimate that replaces the conservative policy.ReservedOutputTokens
	// (= max_tokens worst case) inside ContextBudgetCredits.doReserveBudget.
	// When nil, or when Estimate() returns hasData=false, the middleware
	// falls back to ReservedOutputTokens — preserves pre-existing behaviour
	// and the zero-regression guarantee for cold-start models.
	CompletionEstimator CompletionEstimator

	// PricingCalc is used by the Billing middleware to compute cost_cents
	// synchronously after the provider call. When set, Billing writes the
	// computed cost into a *finalCostHolder in ctx so that the outer
	// ContextBudgetCredits can pass the real value to FinalizeReservation
	// instead of the pre-call EstimatedCredits placeholder.
	// When nil, the cost handoff is skipped and FinalizeReservation falls
	// back to EstimatedCredits (pre-existing behaviour).
	PricingCalc pricing.ICalculator

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
//	Tracing → Fallback → ContextBudgetCredits → Billing → Retry → (Adapter — not wrapped here)
//
// Billing is inside Fallback so that when Fallback switches to a backup
// provider, Billing records the fallback provider/model/pricing rather than the
// primary route.
//
// ContextBudgetCredits implements the route-aware context budget gateway:
// estimates token counts, runs the compression planner if needed, reserves
// credits (ChargeUser=true ops), and reconciles/refunds after the provider call.
//
// The caller wraps the adapter handler (innermost) with the returned Middleware.
func BuildDefault(deps Deps) Middleware {
	return Chain(
		Tracing(deps),
		Fallback(deps),
		ContextBudgetCredits(deps),
		Billing(deps),
		Retry(deps),
	)
}

// defaultMiddlewareNames returns the names of middlewares assembled by BuildDefault,
// in execution order (outermost first). It is used by tests to assert the chain
// composition without requiring spy injection into every Deps field.
//
// Must be kept in sync with BuildDefault whenever the middleware order changes.
func defaultMiddlewareNames() []string {
	return []string{"tracing", "fallback", "context_budget", "billing", "retry"}
}

// AsGatewayChain converts a Middleware into an aiservice.MiddlewareChainFunc.
//
// Because aiservice.GatewayHandler and middleware.Handler have identical
// underlying function types, this conversion is valid and zero-cost at runtime.
// The conversion is isolated here so that the aiservice package (parent) does
// not need to import its own sub-packages.
func AsGatewayChain(m Middleware) aiservice.MiddlewareChainFunc {
	return func(next aiservice.GatewayHandler) aiservice.GatewayHandler {
		// Convert aiservice.GatewayHandler → middleware.Handler (same underlying type).
		innerHandler := Handler(next)
		// Apply the middleware chain.
		wrapped := m(innerHandler)
		// Convert the result back.
		return aiservice.GatewayHandler(wrapped)
	}
}

// ----------------------------------------------------------------------------
// DB-backed UsageStore (defined here to keep billing wiring in one place)
// ----------------------------------------------------------------------------

// dbUsageStore is a thin GORM-backed implementation of UsageStore used in
// production.  It is created by NewDBUsageStore.
type dbUsageStore struct {
	db *gorm.DB
}

// CreateUsageRecord persists a single usage record. Failures are non-fatal.
func (s *dbUsageStore) CreateUsageRecord(ctx context.Context, r *model.UsageRecord) error {
	return s.db.WithContext(ctx).Create(r).Error
}

// GetPricingRule returns the best-matching pricing_rule row, preferring an
// exact model match over the provider-level default (empty model string).
func (s *dbUsageStore) GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	var rule model.PricingRule
	err := s.db.WithContext(ctx).
		Where("service_type = ? AND provider = ? AND model IN (?, '') AND is_active = ?",
			serviceType, provider, modelName, true).
		Order("CASE WHEN model = '' THEN 1 ELSE 0 END").
		First(&rule).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

// Ensure dbUsageStore satisfies the interface.
var _ UsageStore = (*dbUsageStore)(nil)

// NewDBUsageStore creates a UsageStore backed by the given *gorm.DB.
func NewDBUsageStore(db *gorm.DB) UsageStore {
	return &dbUsageStore{db: db}
}
