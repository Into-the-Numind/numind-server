-- F-9 Fix: extend credit_reservation.finalize_reason ENUM with values
-- that code was already passing but MySQL was silently rejecting.
--
-- Root cause: when an LLM provider call fails, FinalizeReservation calls
-- Refund() with reason='provider_err'. MySQL rejects it because 'provider_err'
-- was never in the ENUM, returning Error 1265 (Data truncated for column
-- 'finalize_reason'). The Refund SQL silently fails and the reservation stays
-- permanently in status='reserved', over-charging the user by reserved_credits.
--
-- Audit of ALL finalize_reason string literals passed by code:
--
--   Already in ENUM (no change needed):
--     'normal'            → Reconcile() hard-codes this (credit_service.go:705)
--     'op_failed'         → classifyReason() default branch (credit_service.go:926)
--     'user_cancelled'    → classifyReason() on context.Canceled (credit_service.go:922)
--     'provider_timeout'  → classifyReason() on context.DeadlineExceeded (credit_service.go:924)
--     'no_actual_cost'    → FinalizeReservation() when actualCost is nil/0 (credit_service.go:911)
--     'expired_by_cron'   → reserved for cron sweeper (not yet implemented)
--     'manual_refund'     → reserved for ops tooling (not yet implemented)
--
--   ❌ MISSING — being added by this migration:
--     'provider_err'           → context_budget.go:576,862,868 sets fi.ErrorCode="provider_err"
--                                 → finalizeReservationIfNeeded passes it as reason to Refund()
--                                 → Bug: MySQL rejects 'provider_err', Refund SQL fails
--                                 → Reservation #55 orphaned in status='reserved'
--     'context_budget_refund'  → context_budget.go:623 passed as reason to Refund() when
--                                 fi.Refund=true and fi.ErrorCode="" (no error code set)
--     'nil_stream'             → context_budget.go:751 sets fi.ErrorCode="nil_stream"
--                                 → finalizeReservationIfNeeded passes it as reason to Refund()
--
-- Note on 'user_cancelled' in context_budget.go:795,810,826,847:
--   These set fi.ErrorCode="user_cancelled" but go through Refund() directly,
--   not through the ENUM path — confirmed 'user_cancelled' is already in ENUM.
--
-- Discovered during P1.2.1 over-budget compression test on dev
-- (reservation #55 left orphaned with finalize SQL failure in logs).

ALTER TABLE credit_reservation
    MODIFY COLUMN finalize_reason
        ENUM(
            'normal',
            'op_failed',
            'user_cancelled',
            'provider_timeout',
            'no_actual_cost',
            'expired_by_cron',
            'manual_refund',
            'provider_err',
            'context_budget_refund',
            'nil_stream'
        )
        DEFAULT NULL;
