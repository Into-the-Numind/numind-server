-- Clean Migrated Subscription Billing Records — Forward Migration
-- ============================================================================
-- Context
--   The 2026-04-30 credit_package → membership_event migration spread each
--   original subscription package into 1-record-per-month placeholder events
--   (idempotency_key LIKE 'migration-%'). A 12-month annual became 12 rows
--   (1 sub_granted + 11 future-dated sub_renewed), and the per-row `months`
--   field is unreliable (e.g. user 406's 12 rows carry random 1/2 summing to
--   19 for a genuine 12-month annual). This left the ledger in the OLD shape.
--
--   The new billing model is "1 record = N months". This migration collapses
--   each user's migration sub_granted/sub_renewed placeholders into ONE clean
--   sub_granted of months = COUNT(placeholders) — i.e. the original package
--   size under the old "1 record per month" convention — attributed to the
--   original grant date (earliest occurred_at), priced via PriceForMonths
--   (12 → ¥94900, else N × ¥9900). source / granter_user_id are preserved
--   (self_purchase rows stay self_purchase → still excluded from B2B settlement).
--   Boosters (booster_granted) and trials (trial_granted / trial_grant table)
--   are NOT touched.
--
-- Why COUNT (not SUM(months))
--   The per-row `months` is migration noise. COUNT of rows == original months
--   per the "1 record / month" old model, AND it reproduces the pre-cleanup
--   B2B billing report EXACTLY (the report already collapsed by row count),
--   so this migration is amount-INVARIANT: data shape changes, money does not.
--
-- Safety / operating procedure (MANUAL migration — NOT auto-applied)
--   1. Originals are archived to membership_event_migration_archive (with their
--      original id) BEFORE delete. Fully reversible via the _rollback.sql.
--   2. Idempotent: re-running is a no-op (merged key is UNIQUE; archive guarded
--      by NOT EXISTS; delete finds nothing on re-run).
--   3. AFTER running, VERIFY the B2B billing report total_amount_cents and
--      grants_count per month are UNCHANGED vs the pre-run snapshot. If they
--      differ, run _rollback.sql and investigate — do NOT proceed.
--   4. Validated on dev (prod clone) 2026-06-02: 73 rows → 51 clean records,
--      billing identical for every month (2026-04 ¥3858.20 / 05 ¥6164.90 /
--      06 ¥1265.80). Run on prod only with explicit approval.
-- ============================================================================

CREATE TABLE IF NOT EXISTS membership_event_migration_archive LIKE membership_event;

START TRANSACTION;

-- 1. Archive the migration sub_granted/sub_renewed placeholders (idempotent).
INSERT INTO membership_event_migration_archive
SELECT e.*
FROM membership_event e
WHERE e.idempotency_key LIKE 'migration-%'
  AND e.event_type IN ('sub_granted', 'sub_renewed')
  AND NOT EXISTS (
    SELECT 1 FROM membership_event_migration_archive a WHERE a.id = e.id
  );

-- 2. Insert ONE clean sub_granted per user (idempotent via UNIQUE idempotency_key).
INSERT IGNORE INTO membership_event
  (user_id, event_type, product_type, months, amount_cents, source, granter_user_id, idempotency_key, occurred_at)
SELECT
  g.user_id,
  'sub_granted',
  'monthly',
  g.mm,
  CASE WHEN g.mm = 12 THEN 94900 ELSE g.mm * 9900 END,
  g.src,
  g.granter,
  CONCAT('migcleaned-', g.user_id),
  g.occ
FROM (
  SELECT
    user_id,
    COUNT(*)              AS mm,
    MIN(occurred_at)      AS occ,
    MAX(source)           AS src,
    MAX(granter_user_id)  AS granter
  FROM membership_event
  WHERE idempotency_key LIKE 'migration-%'
    AND event_type IN ('sub_granted', 'sub_renewed')
  GROUP BY user_id
) g;

-- 3. Delete the original migration placeholders (archived in step 1).
DELETE FROM membership_event
WHERE idempotency_key LIKE 'migration-%'
  AND event_type IN ('sub_granted', 'sub_renewed');

COMMIT;

-- 4. Post-run sanity counts (expect migration_sub_left = 0).
SELECT 'archived'                        AS k, COUNT(*) AS v FROM membership_event_migration_archive
UNION ALL
SELECT 'merged_clean_records',           COUNT(*) FROM membership_event WHERE idempotency_key LIKE 'migcleaned-%'
UNION ALL
SELECT 'migration_sub_left(expect 0)',   COUNT(*) FROM membership_event WHERE idempotency_key LIKE 'migration-%' AND event_type IN ('sub_granted', 'sub_renewed');
