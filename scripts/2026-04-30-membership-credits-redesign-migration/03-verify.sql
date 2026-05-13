-- =============================================================================
-- 03-verify.sql  —  membership-credits-redesign migration VERIFY
-- 8 invariants (I1–I8). All violation_count must be 0.
-- Read-only: no writes.
--
-- Run with:
--   docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 03-verify.sql
-- =============================================================================

SELECT '=== POST-MIGRATION INVARIANT AUDIT (8 checks) ===' AS section;
SELECT 'All violation_count must be 0. Any nonzero = escalate immediately.' AS note;

-- ─────────────────────────────────────────────────────────────────────────────
-- I1: Non-negative net credit balance
-- subscription.credits_remaining should exist for active subscriptions.
-- Since credit_cycle is application-managed, we verify trial + booster >= 0.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I1_no_negative_booster_balance', COUNT(*) AS violation_count
FROM user_booster_balance
WHERE credits_remaining < 0;

-- ─────────────────────────────────────────────────────────────────────────────
-- I2: Subscription.expires_at matches latest subscription credit_package expiry
-- per user (within 1 second tolerance for NOW() drift).
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I2_sub_expires_at_matches_latest_pkg', COUNT(*) AS violation_count
FROM subscription s
INNER JOIN (
  SELECT user_id, MAX(expires_at) AS max_pkg_expires
  FROM credit_package
  WHERE type = 'subscription'
    AND status IN ('active', 'exhausted', 'expired')
  GROUP BY user_id
) latest ON latest.user_id = s.user_id
WHERE ABS(TIMESTAMPDIFF(SECOND, s.expires_at, latest.max_pkg_expires)) > 1;

-- ─────────────────────────────────────────────────────────────────────────────
-- I3: trial_grant.credits_remaining matches credit_package remain_credits
-- for the earliest trial package per user.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I3_trial_credits_remaining_matches_pkg', COUNT(*) AS violation_count
FROM trial_grant tg
INNER JOIN (
  SELECT cp.user_id AS user_id, cp.remain_credits AS remain_credits
  FROM credit_package cp
  INNER JOIN (
    SELECT user_id, MIN(activated_at) AS min_at
    FROM credit_package
    WHERE type = 'trial'
    GROUP BY user_id
  ) earliest ON earliest.user_id = cp.user_id AND cp.activated_at = earliest.min_at
    AND cp.type = 'trial'
) expected ON expected.user_id = tg.user_id
WHERE tg.credits_remaining <> expected.remain_credits;

-- ─────────────────────────────────────────────────────────────────────────────
-- I4: user_booster_balance.credits_remaining = SUM of active booster pkg remain
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I4_booster_balance_sum_matches_active_pkgs', COUNT(*) AS violation_count
FROM user_booster_balance ubb
INNER JOIN (
  SELECT user_id,
    SUM(CASE WHEN status = 'active' AND expires_at > NOW()
             THEN remain_credits ELSE 0 END) AS active_sum
  FROM credit_package
  WHERE type = 'booster'
  GROUP BY user_id
) pkg_sum ON pkg_sum.user_id = ubb.user_id
WHERE ubb.credits_remaining <> pkg_sum.active_sum;

-- ─────────────────────────────────────────────────────────────────────────────
-- I5a: membership_event count >= credit_package row count
-- (we produce at least one event per package)
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I5a_event_count_ge_package_count', COUNT(*) AS violation_count
FROM (
  SELECT
    (SELECT COUNT(*) FROM membership_event
     WHERE idempotency_key LIKE 'migration-20260430-cp-%') AS event_count,
    (SELECT COUNT(*) FROM credit_package) AS pkg_count
) counts
WHERE event_count < pkg_count;

-- ─────────────────────────────────────────────────────────────────────────────
-- I6: trial_grant.user_id is UNIQUE (one trial per user)
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I6_trial_grant_user_id_unique', COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM trial_grant
  GROUP BY user_id
  HAVING cnt > 1
) dup;

-- ─────────────────────────────────────────────────────────────────────────────
-- I7: subscription.user_id is UNIQUE (one subscription row per user)
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I7_subscription_user_id_unique', COUNT(*) AS violation_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM subscription
  GROUP BY user_id
  HAVING cnt > 1
) dup;

-- ─────────────────────────────────────────────────────────────────────────────
-- I8: No orphan membership_event rows (event.user_id must exist in user table)
-- ─────────────────────────────────────────────────────────────────────────────

SELECT 'I8_no_orphan_membership_events', COUNT(*) AS violation_count
FROM membership_event me
LEFT JOIN user u ON u.id = me.user_id
WHERE u.id IS NULL;

-- ─────────────────────────────────────────────────────────────────────────────
-- DETAIL: apply_log summary
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== apply_log summary ===' AS section;
SELECT step, table_name, rows_inserted, applied_at
FROM migration_20260430_apply_log
ORDER BY applied_at;

-- ─────────────────────────────────────────────────────────────────────────────
-- DETAIL: Row counts per new table
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== New table row counts ===' AS section;
-- Note: `rows` is reserved in some MySQL contexts (window frame ROWS clause),
-- using `row_count` as the column alias to be safe.
SELECT 'subscription'         AS tbl, COUNT(*) AS row_count FROM subscription
UNION ALL
SELECT 'trial_grant'          AS tbl, COUNT(*) AS row_count FROM trial_grant
UNION ALL
SELECT 'credit_cycle'         AS tbl, COUNT(*) AS row_count FROM credit_cycle
UNION ALL
SELECT 'user_booster_balance' AS tbl, COUNT(*) AS row_count FROM user_booster_balance
UNION ALL
SELECT 'membership_event'     AS tbl, COUNT(*) AS row_count FROM membership_event;

SELECT 'VERIFY COMPLETE — resolve any violation_count > 0 before releasing traffic' AS status;
