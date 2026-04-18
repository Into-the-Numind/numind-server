// Package credit — credit_service_langfuse.go centralises the Langfuse span
// emission for the four credits-mode lifecycle events: credit-estimate,
// credit-reserve, credit-reconcile, credit-refund. See spec §5.1.
//
// These helpers are no-op when the caller's context has no TraceCtx (fresh
// unit tests) or when Langfuse is disabled globally (langfuse.C == nil). They
// MUST be safe to call unconditionally so biz code stays readable.
//
// Enforcement of this file being exercised: .claude/rules/ai-service.md §3
// (Span 与 Error 模式) — every non-LLM sub-operation in a traced flow MUST
// emit a span.
package credit

import (
	"context"

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

// emitCreditEstimateSpan emits the credit-estimate span per spec §5.1.1.
// Called after CheckAndEstimate has computed its result; safe to call with
// a nil PreCheckResult when the estimate itself failed.
func emitCreditEstimateSpan(
	ctx context.Context, user *model.User, op Operation, in EstimationInput,
	pre *PreCheckResult, coef *model.CreditEstimationCoefficient,
) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		return
	}
	spanID := langfuse.SpanID()

	input := map[string]interface{}{
		"operation":    string(op),
		"prompt_chars": in.PromptChars,
		"model":        in.Model,
		"provider":     in.Provider,
		"billing_mode": user.BillingMode,
	}
	output := map[string]interface{}{}
	if pre != nil {
		output["estimated_credits"] = pre.EstimatedCredits
		output["sufficient"] = pre.Sufficient
		output["skip_deduction"] = pre.SkipDeduction
		output["coefficient_id"] = pre.CoefficientID
		output["sub_remain_before"] = pre.Balance.SubRemain
		output["booster_remain_before"] = pre.Balance.BoosterRemain
	}
	if coef != nil {
		output["char_to_token_ratio"] = coef.CharToTokenRatio
		output["completion_prompt_ratio"] = coef.CompletionPromptRatio
		output["safety_buffer_pct"] = coef.SafetyBufferPct
	}

	langfuse.CreateSpan(tc.TraceID, spanID, "credit-estimate",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(input),
		langfuse.WithSpanOutput(output),
	)
	langfuse.EndSpan(spanID)
}

// emitCreditReserveSpan emits the credit-reserve span per spec §5.1.2.
// Called after Reserve commits its transaction.
func emitCreditReserveSpan(
	ctx context.Context, user *model.User, rsv *Reservation,
	subRemainAfter, boosterRemainAfter int64,
) {
	tc := langfuse.FromContext(ctx)
	if tc == nil || rsv == nil {
		return
	}
	spanID := langfuse.SpanID()

	packages := make([]map[string]interface{}, 0, len(rsv.Items))
	for _, item := range rsv.Items {
		packages = append(packages, map[string]interface{}{
			"package_id":   item.PackageID,
			"credits":      item.Credits,
			"package_type": item.PackageType,
			"seq":          item.Seq,
		})
	}

	input := map[string]interface{}{
		"reservation_id":   rsv.ID,
		"reserved_credits": rsv.ReservedCredits,
		"idempotency_key":  derefStr(rsv.IdempotencyKey),
		"user_id":          user.ID,
	}
	output := map[string]interface{}{
		"reserved_from_packages": packages,
		"sub_remain_after":       subRemainAfter,
		"booster_remain_after":   boosterRemainAfter,
	}

	langfuse.CreateSpan(tc.TraceID, spanID, "credit-reserve",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(input),
		langfuse.WithSpanOutput(output),
	)
	langfuse.EndSpan(spanID)
}

// emitCreditReconcileSpan emits the credit-reconcile span per spec §5.1.3.
// reconcileDirection is one of "refund" / "topup" / "noop".
func emitCreditReconcileSpan(
	ctx context.Context, reservationID uint64,
	reserved, actualCostCents, delta int64,
	actualPromptTokens, actualCompletionTokens int,
	reconcileDirection string,
	refundedPackages []map[string]interface{},
) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		return
	}
	spanID := langfuse.SpanID()

	input := map[string]interface{}{
		"reservation_id":           reservationID,
		"reserved_credits":         reserved,
		"actual_cost_cents":        actualCostCents,
		"actual_prompt_tokens":     actualPromptTokens,
		"actual_completion_tokens": actualCompletionTokens,
	}
	output := map[string]interface{}{
		"delta":                delta,
		"reconcile_direction":  reconcileDirection,
		"refunded_to_packages": refundedPackages,
		"final_status":         string(StatusReconciled),
	}

	langfuse.CreateSpan(tc.TraceID, spanID, "credit-reconcile",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(input),
		langfuse.WithSpanOutput(output),
	)
	langfuse.EndSpan(spanID)
}

// emitCreditRefundSpan emits the credit-refund span per spec §5.1.4.
// reason is one of the spec-level ENUM values: op_failed / user_cancelled /
// provider_timeout / no_actual_cost / expired_by_cron / manual_refund.
func emitCreditRefundSpan(
	ctx context.Context, reservationID uint64, reason string,
	refundedCredits int64, refundedItems []map[string]interface{},
) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		return
	}
	spanID := langfuse.SpanID()

	input := map[string]interface{}{
		"reservation_id": reservationID,
		"reason":         reason,
	}
	output := map[string]interface{}{
		"refunded_credits": refundedCredits,
		"refunded_items":   refundedItems,
		"final_status":     string(StatusRefunded),
	}

	langfuse.CreateSpan(tc.TraceID, spanID, "credit-refund",
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(input),
		langfuse.WithSpanOutput(output),
	)
	langfuse.EndSpan(spanID)
}

// updateTraceMetadataForCredits augments the existing SOP / SalesRAG trace
// root with billing-mode + balance snapshot per spec §5.1.5. Called from the
// top of CheckAndEstimate so the trace has the context before any span fires.
func updateTraceMetadataForCredits(ctx context.Context, user *model.User, balance BalanceBreakdown) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		return
	}
	metadata := map[string]string{
		"billing_mode":            user.BillingMode,
		"deducted_from":           classifyDeductedFrom(balance),
		"credit_balance_at_start": int64ToStr(balance.SubRemain + balance.BoosterRemain),
	}
	langfuse.UpdateTraceMetadata(tc.TraceID, metadata)
}

// classifyDeductedFrom categorises the current balance into one of:
//
//	"subscription"  — sub-only credits available
//	"booster"       — booster-only credits available
//	"mixed"         — both pools have credits
//	"none(legacy)"  — legacy_tier mode (no credit pools)
func classifyDeductedFrom(balance BalanceBreakdown) string {
	if balance.BillingMode == model.BillingModeLegacyTier {
		return "none(legacy)"
	}
	hasSub := balance.SubRemain > 0
	hasBooster := balance.BoosterRemain > 0
	switch {
	case hasSub && hasBooster:
		return "mixed"
	case hasSub:
		return "subscription"
	case hasBooster:
		return "booster"
	default:
		return "empty"
	}
}

// derefStr returns the string value or "" for a nil pointer.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// int64ToStr turns an int64 into a decimal string without pulling strconv
// into this file (the langfuse metadata map is map[string]string, a limitation
// of langfuse.TraceBody.Metadata which we preserve).
func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
