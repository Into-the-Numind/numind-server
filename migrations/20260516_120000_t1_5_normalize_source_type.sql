-- ============================================================================
-- T1.5 — Normalize credit_transaction.source_type 'subscription' → 'cycle'
-- ============================================================================
-- Purpose: T1 backfill used `credit_package.type` as the source_type value
--          (which uses {trial, subscription, booster} vocabulary). But new
--          code (cycle.go after T6) writes 'cycle' for monthly subscription
--          deductions. This creates a vocabulary drift in credit_transaction.
--
--          Prod-clone validation discovered ~2547 historical rows with
--          source_type='subscription' that need to be normalized to 'cycle'
--          to match the new vocabulary used by T8 calibration SQL + new code.
--
-- Discovery context: Found during prod-data dry-run on 2026-05-16. T8 cycle
--          calibration filters by `source_type='cycle'` which would have
--          missed all 2547 'subscription' rows on prod, causing wildly
--          incorrect calibration. This migration prevents that.
--
-- Prerequisites: T1 (source_type column + backfill) must be deployed first.
--
-- Idempotent: Yes — second run finds 0 rows to update (WHERE clause guards).
--
-- Rollback: Not provided. Reverting 'cycle' → 'subscription' would re-introduce
--          the vocabulary drift bug. Just don't roll back this one.
-- ============================================================================

USE `numind-prod`;

-- Pre-check: how many rows will be updated?
SELECT 'pre_check_subscription_rows' AS check_name,
       COUNT(*) AS rows_to_normalize
  FROM credit_transaction
 WHERE source_type = 'subscription';
-- On prod (estimate): ~2547 rows.
-- On dev fresh setup with T1+T1.5 deployed: 0 rows (T1 backfill alone tagged subscription rows).

-- Apply: rename 'subscription' to 'cycle' to match new code vocabulary.
START TRANSACTION;

UPDATE credit_transaction
   SET source_type = 'cycle'
 WHERE source_type = 'subscription';

COMMIT;

-- Post-check: should be 0 remaining 'subscription' rows.
SELECT 'post_check_remaining_subscription_rows' AS check_name,
       COUNT(*) AS should_be_0
  FROM credit_transaction
 WHERE source_type = 'subscription';

-- Sanity: final source_type distribution.
SELECT 'final_distribution' AS check_name, source_type, COUNT(*) AS n
  FROM credit_transaction
 GROUP BY source_type
 ORDER BY n DESC;
-- Expected: cycle (most), trial, booster, NULL (debt rows package_id=0)
