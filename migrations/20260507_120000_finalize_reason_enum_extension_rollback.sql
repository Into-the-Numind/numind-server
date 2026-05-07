-- Rollback: revert credit_reservation.finalize_reason ENUM to pre-F-9 definition.
--
-- ⚠️  WARNING: This rollback will FAIL with "Data truncated for column
-- 'finalize_reason'" if ANY row in credit_reservation already carries one of
-- the new values:
--   'provider_err', 'context_budget_refund', 'nil_stream'
--
-- Before running this rollback, verify no rows use new values:
--   SELECT id, finalize_reason FROM credit_reservation
--   WHERE finalize_reason IN ('provider_err','context_budget_refund','nil_stream');
--
-- If rows exist, you must NULL them out first:
--   UPDATE credit_reservation SET finalize_reason = NULL
--   WHERE finalize_reason IN ('provider_err','context_budget_refund','nil_stream');
--
-- Then run this file.

ALTER TABLE credit_reservation
    MODIFY COLUMN finalize_reason
        ENUM(
            'normal',
            'op_failed',
            'user_cancelled',
            'provider_timeout',
            'no_actual_cost',
            'expired_by_cron',
            'manual_refund'
        )
        DEFAULT NULL;
