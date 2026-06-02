-- Clean Migrated Subscription Billing Records — ROLLBACK
-- ============================================================================
-- Reverses 20260602_120000_clean_migrated_billing_records.sql by restoring the
-- archived migration placeholders (with their original id) and removing the
-- merged 'migcleaned-%' records. Safe to run only while
-- membership_event_migration_archive still holds the originals.
-- ============================================================================

START TRANSACTION;

-- 1. Restore archived migration placeholders that are no longer present
--    (match by original id so we never create duplicates).
INSERT INTO membership_event
SELECT a.*
FROM membership_event_migration_archive a
WHERE a.event_type IN ('sub_granted', 'sub_renewed')
  AND a.idempotency_key LIKE 'migration-%'
  AND NOT EXISTS (
    SELECT 1 FROM membership_event e WHERE e.id = a.id
  );

-- 2. Remove the merged clean records produced by the forward migration.
DELETE FROM membership_event
WHERE idempotency_key LIKE 'migcleaned-%'
  AND event_type = 'sub_granted';

COMMIT;

-- Sanity: expect migration_sub_restored back to the archived count, merged = 0.
SELECT 'migration_sub_now' AS k, COUNT(*) AS v FROM membership_event WHERE idempotency_key LIKE 'migration-%' AND event_type IN ('sub_granted', 'sub_renewed')
UNION ALL
SELECT 'merged_left(expect 0)', COUNT(*) FROM membership_event WHERE idempotency_key LIKE 'migcleaned-%';
