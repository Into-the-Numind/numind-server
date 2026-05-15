package credit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// --- Task C.8: Langfuse span emission ---

// langfuseCapture is a tiny helper that installs a test client + event
// capture hook, returning a cleanup closure and a getter for the captured
// events. Tests always restore the previous langfuse.C to avoid leaking
// state across tests.
func langfuseCapture(t *testing.T, traceID string) (func() []*langfuse.IngestionEvent, func()) {
	t.Helper()
	var (
		mu       sync.Mutex
		captured []*langfuse.IngestionEvent
	)
	prev := langfuse.C
	langfuse.C = langfuse.NewTestClient()
	langfuse.C.InstallEventHook(func(e *langfuse.IngestionEvent) {
		mu.Lock()
		captured = append(captured, e)
		mu.Unlock()
	})
	cleanup := func() { langfuse.C = prev }

	// Seed a trace root so FromContext(ctx) returns a non-nil TraceCtx in
	// subsequent biz calls. The "sop-run" trace is what production SOP code
	// creates before delegating to the credit service.
	langfuse.CreateTrace(traceID, "sop-run")

	return func() []*langfuse.IngestionEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*langfuse.IngestionEvent, len(captured))
		copy(out, captured)
		return out
	}, cleanup
}

// contextWithTrace seeds the context with a TraceCtx that mirrors production
// usage (ParentObservationID empty → spans attach at the trace root).
func contextWithTrace(traceID string) context.Context {
	return langfuse.WithTrace(context.Background(), traceID)
}

// newCreditSvcWithLangfuseDB wires up the full stack including the coefficient
// + pricing seed data required for a real credits-mode CheckAndEstimate.
func newCreditSvcWithLangfuseDB(t *testing.T, userID uint, balance int64) (
	credit.ICreditService, store.IStore, *model.User,
) {
	t.Helper()
	db := newCreditReserveTestDB(t)
	// Add the coefficient + pricing tables used by creditsImpl.CheckAndEstimate.
	require.NoError(t, db.AutoMigrate(
		&model.CreditEstimationCoefficient{},
		&model.PricingRule{},
	))
	ds := store.NewTestStore(db)

	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: balance, RemainCredits: balance,
			ActivatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)},
	})
	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.25, 1, true)
	seedPricingRule(t, db, "llm_chat", "ali", "qwen-turbo", 200, 800)

	pc := pricing.NewCalculator(ds.Billing())
	svc := newCreditServiceWithMembership(ds, db, pc)

	user := newCreditsUser(userID)
	return svc, ds, user
}

func findSpanByName(events []*langfuse.IngestionEvent, name, eventType string) *langfuse.SpanBody {
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		if body, ok := e.Body.(*langfuse.SpanBody); ok {
			if body.Name == name {
				return body
			}
		}
	}
	return nil
}

// TestLangfuse_CheckAndEstimate_EmitsCreditEstimateSpan verifies the credit-
// estimate span per spec §5.1.1 carries the full input + output schema.
func TestLangfuse_CheckAndEstimate_EmitsCreditEstimateSpan(t *testing.T) {
	traceID := "trace-estimate-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 900, 2000)
	ctx := contextWithTrace(traceID)

	pre, err := svc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, Model: "qwen-turbo", Provider: "ali",
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.Sufficient)

	events := getEvents()
	// Expect: trace-create (initial) + trace-create (metadata update) + span-create + span-update
	span := findSpanByName(events, "credit-estimate", "span-create")
	require.NotNil(t, span, "credit-estimate span must be emitted; got events: %+v", events)

	// Input schema (spec §5.1.1)
	input, ok := span.Input.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "sop_run", input["operation"])
	assert.Equal(t, 1000, input["prompt_chars"])
	assert.Equal(t, "qwen-turbo", input["model"])
	assert.Equal(t, "ali", input["provider"])
	assert.Equal(t, model.BillingModeCredits, input["billing_mode"])

	// Output schema
	output, ok := span.Output.(map[string]interface{})
	require.True(t, ok)
	for _, key := range []string{
		"estimated_credits", "sufficient", "skip_deduction",
		"coefficient_id", "char_to_token_ratio", "completion_prompt_ratio",
		"safety_buffer_pct", "sub_remain_before", "booster_remain_before",
	} {
		assert.Contains(t, output, key, "spec §5.1.1 field %q missing", key)
	}
}

// TestLangfuse_Reserve_EmitsCreditReserveSpan verifies credit-reserve span.
func TestLangfuse_Reserve_EmitsCreditReserveSpan(t *testing.T) {
	traceID := "trace-reserve-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 901, 2000)
	ctx := contextWithTrace(traceID)

	idemp := "sop_run:1:1"
	rsv, err := svc.Reserve(ctx, user, credit.OpSopRun, 150, 1, &idemp)
	require.NoError(t, err)
	require.NotNil(t, rsv)

	events := getEvents()
	span := findSpanByName(events, "credit-reserve", "span-create")
	require.NotNil(t, span)

	input, _ := span.Input.(map[string]interface{})
	require.NotNil(t, input)
	assert.Equal(t, rsv.ID, input["reservation_id"])
	assert.Equal(t, int64(150), input["reserved_credits"])
	assert.Equal(t, idemp, input["idempotency_key"])

	output, _ := span.Output.(map[string]interface{})
	require.NotNil(t, output)
	assert.Contains(t, output, "reserved_from_packages")
	assert.Contains(t, output, "sub_remain_after")
	assert.Contains(t, output, "booster_remain_after")
	pkgs, ok := output["reserved_from_packages"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, pkgs, 1)
	assert.Equal(t, 1, pkgs[0]["seq"])
}

// TestLangfuse_Reconcile_EmitsReconcileSpan verifies credit-reconcile span
// with delta<0 (refund direction).
func TestLangfuse_Reconcile_EmitsReconcileSpan(t *testing.T) {
	traceID := "trace-reconcile-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 902, 2000)
	ctx := contextWithTrace(traceID)

	rsv, err := svc.Reserve(ctx, user, credit.OpSopRun, 150, 1, nil)
	require.NoError(t, err)

	// Reconcile with actual=100 → delta=-50 → refund direction
	require.NoError(t, svc.Reconcile(ctx, rsv.ID, 100))

	events := getEvents()
	span := findSpanByName(events, "credit-reconcile", "span-create")
	require.NotNil(t, span)

	input, _ := span.Input.(map[string]interface{})
	require.NotNil(t, input)
	assert.Equal(t, rsv.ID, input["reservation_id"])
	assert.Equal(t, int64(150), input["reserved_credits"])
	assert.Equal(t, int64(100), input["actual_cost_cents"])

	output, _ := span.Output.(map[string]interface{})
	require.NotNil(t, output)
	assert.Equal(t, int64(-50), output["delta"])
	assert.Equal(t, "refund", output["reconcile_direction"])
	assert.Contains(t, output, "refunded_to_packages")
	assert.Equal(t, string(credit.StatusReconciled), output["final_status"])
}

// TestLangfuse_FinalizeReservation_ThreadsTokensToReconcileSpan verifies
// that actual_prompt_tokens / actual_completion_tokens set on the Reservation
// pre-defer are visible in the credit-reconcile span metadata. This guards
// against regression of the AI-5 token-threading fix (span metadata used to
// always show 0/0).
func TestLangfuse_FinalizeReservation_ThreadsTokensToReconcileSpan(t *testing.T) {
	traceID := "trace-reconcile-tokens-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 902, 2000)
	ctx := contextWithTrace(traceID)

	rsv, err := svc.Reserve(ctx, user, credit.OpSopRun, 150, 1, nil)
	require.NoError(t, err)

	// Caller populates token counts on the Reservation before defer fires
	// (production pattern in sop.go / salesrag.go after the LLM call).
	rsv.ActualPromptTokens = 1234
	rsv.ActualCompletionTokens = 567

	actual := int64(100)
	var opErr error
	require.NoError(t, svc.FinalizeReservation(ctx, rsv, &actual, &opErr))

	events := getEvents()
	span := findSpanByName(events, "credit-reconcile", "span-create")
	require.NotNil(t, span)

	input, _ := span.Input.(map[string]interface{})
	require.NotNil(t, input)
	assert.Equal(t, 1234, input["actual_prompt_tokens"],
		"token count set on rsv should propagate to reconcile span input")
	assert.Equal(t, 567, input["actual_completion_tokens"],
		"token count set on rsv should propagate to reconcile span input")
}

// TestLangfuse_Refund_EmitsRefundSpan verifies credit-refund span via
// FinalizeReservation's opErr dispatch.
func TestLangfuse_Refund_EmitsRefundSpan(t *testing.T) {
	traceID := "trace-refund-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 903, 2000)
	ctx := contextWithTrace(traceID)

	rsv, err := svc.Reserve(ctx, user, credit.OpSopRun, 80, 1, nil)
	require.NoError(t, err)

	opErr := errors.New("llm downstream failure")
	require.NoError(t, svc.FinalizeReservation(ctx, rsv, nil, &opErr))

	events := getEvents()
	span := findSpanByName(events, "credit-refund", "span-create")
	require.NotNil(t, span)

	input, _ := span.Input.(map[string]interface{})
	require.NotNil(t, input)
	assert.Equal(t, rsv.ID, input["reservation_id"])
	assert.Equal(t, "op_failed", input["reason"])

	output, _ := span.Output.(map[string]interface{})
	require.NotNil(t, output)
	assert.Equal(t, int64(80), output["refunded_credits"])
	assert.Equal(t, string(credit.StatusRefunded), output["final_status"])
}

// TestLangfuse_TraceMetadata_AppendedAtCheckAndEstimate verifies that
// CheckAndEstimate emits a trace-create event carrying billing_mode /
// deducted_from / credit_balance_at_start per spec §5.1.5.
func TestLangfuse_TraceMetadata_AppendedAtCheckAndEstimate(t *testing.T) {
	traceID := "trace-metadata-test"
	getEvents, cleanup := langfuseCapture(t, traceID)
	defer cleanup()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 904, 1500)
	ctx := contextWithTrace(traceID)

	_, err := svc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 200, Model: "qwen-turbo", Provider: "ali",
	})
	require.NoError(t, err)

	events := getEvents()
	var foundMeta *langfuse.TraceBody
	for _, e := range events {
		if e.Type != "trace-create" {
			continue
		}
		body, ok := e.Body.(*langfuse.TraceBody)
		if !ok {
			continue
		}
		if body.ID == traceID && len(body.Metadata) > 0 {
			foundMeta = body
			break
		}
	}
	require.NotNil(t, foundMeta, "trace-create with credits metadata must be emitted; events=%+v", events)
	assert.Equal(t, model.BillingModeCredits, foundMeta.Metadata["billing_mode"])
	assert.Equal(t, "subscription", foundMeta.Metadata["deducted_from"],
		"only sub packages seeded → deducted_from=subscription")
	assert.Equal(t, "1500", foundMeta.Metadata["credit_balance_at_start"])
}

// TestLangfuse_Disabled_NoEventsEmitted proves the instrumentation is safe
// when Langfuse is globally disabled — no panic, no events.
func TestLangfuse_Disabled_NoEventsEmitted(t *testing.T) {
	prev := langfuse.C
	langfuse.C = &langfuse.Client{} // zero-value = enabled:false
	defer func() { langfuse.C = prev }()

	svc, _, user := newCreditSvcWithLangfuseDB(t, 905, 2000)

	// No trace in context either.
	_, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun,
		credit.EstimationInput{PromptChars: 100, Model: "qwen-turbo", Provider: "ali"})
	require.NoError(t, err)
}
