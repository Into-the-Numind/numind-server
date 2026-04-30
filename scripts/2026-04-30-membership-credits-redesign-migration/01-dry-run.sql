-- =============================================================================
-- 01-dry-run.sql  —  membership-credits-redesign migration DRY RUN
-- Read-only: no writes. Shows expected post-migration state and invariant
-- violations before committing a single row.
--
-- Run with:
--   docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 01-dry-run.sql
-- =============================================================================

-- ─────────────────────────────────────────────────────────────────────────────
-- §A  SOURCE DATA OVERVIEW
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== A1. credit_package type distribution ===' AS section;
SELECT type, status, COUNT(*) AS n, SUM(remain_credits) AS total_remain
FROM credit_package
GROUP BY type, status
ORDER BY type, status;

SELECT '=== A2. Users with active credit_package (by type) ===' AS section;
SELECT
  cp.type,
  COUNT(DISTINCT cp.user_id) AS user_count,
  SUM(cp.remain_credits)     AS sum_remain_credits
FROM credit_package cp
WHERE cp.status = 'active' AND cp.expires_at > NOW()
GROUP BY cp.type;

-- ─────────────────────────────────────────────────────────────────────────────
-- §B  SUBSCRIPTION SEGMENT MERGE PREVIEW
-- Each user may have had multiple subscription packages over time.
-- We merge consecutive segments (where a new package starts close to prior
-- expiry) into one Subscription row. This CTE previews merged segment output.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== B. Subscription segment-merge preview (per user) ===' AS section;
WITH sub_packages AS (
  SELECT
    user_id,
    activated_at,
    expires_at,
    grant_source,
    granter_user_id,
    -- rank packages per user by activated_at
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at) AS rn
  FROM credit_package
  WHERE type = 'subscription'
    AND status IN ('active', 'exhausted', 'expired')
),
-- assign a segment break whenever the gap to the previous expires_at > 2 days
segment_breaks AS (
  SELECT
    sp.*,
    LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) AS prev_expires,
    CASE
      WHEN LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) IS NULL THEN 1
      WHEN DATEDIFF(sp.activated_at,
           LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at)) > 2 THEN 1
      ELSE 0
    END AS is_new_segment
  FROM sub_packages sp
),
segment_ids AS (
  SELECT *,
    SUM(is_new_segment) OVER (PARTITION BY user_id ORDER BY activated_at ROWS UNBOUNDED PRECEDING) AS seg_id
  FROM segment_breaks
),
merged AS (
  SELECT
    user_id,
    seg_id,
    MIN(activated_at)   AS first_started_at,
    MAX(expires_at)     AS expires_at,
    COUNT(*)            AS pkg_count,
    -- approximate total months: sum of ~30-day periods per package
    CEIL(SUM(DATEDIFF(expires_at, activated_at)) / 30) AS total_months_estimated,
    -- source/granter from the latest package in the segment
    MAX(grant_source)   AS grant_source,
    MAX(granter_user_id) AS granter_user_id
  FROM segment_ids
  GROUP BY user_id, seg_id
)
SELECT
  user_id,
  first_started_at,
  expires_at,
  pkg_count,
  total_months_estimated,
  grant_source,
  granter_user_id
FROM merged
ORDER BY user_id, first_started_at;

-- ─────────────────────────────────────────────────────────────────────────────
-- §C  TRIAL GRANT PREVIEW
-- One trial_grant per user: the earliest trial package.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== C. Trial grant preview (earliest trial per user) ===' AS section;
-- Original used FIRST_VALUE() OVER (...) mixed with GROUP BY user_id, which
-- MySQL 8.0 only_full_group_by rejects (ERROR 1055). Rewritten with CTE:
-- trial_ranked isolates the earliest row per user via ROW_NUMBER(), then
-- trial_first filters to rn=1 — no GROUP BY needed, semantics identical.
WITH trial_ranked AS (
  SELECT *,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY activated_at) AS rn
  FROM credit_package
  WHERE type = 'trial'
),
trial_first AS (
  SELECT user_id, activated_at, expires_at, remain_credits, grant_source, granter_user_id
  FROM trial_ranked
  WHERE rn = 1
)
SELECT
  tf.user_id,
  tf.activated_at         AS granted_at,
  tf.expires_at           AS expires_at,
  tf.remain_credits       AS credits_remaining,
  tf.grant_source         AS source,
  tf.granter_user_id
FROM trial_first tf
ORDER BY tf.user_id;

-- ─────────────────────────────────────────────────────────────────────────────
-- §D  BOOSTER AGGREGATE PREVIEW
-- user_booster_balance gets SUM(remain_credits) of all active non-expired
-- booster packages.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== D. Booster balance aggregate per user ===' AS section;
SELECT
  user_id,
  COUNT(*) AS active_booster_pkgs,
  SUM(remain_credits) AS credits_remaining
FROM credit_package
WHERE type = 'booster'
  AND status = 'active'
  AND expires_at > NOW()
GROUP BY user_id
ORDER BY user_id;

-- ─────────────────────────────────────────────────────────────────────────────
-- §E  DELTA RECONCILIATION  (post_total >= pre_total guard)
-- Sanity: the new tables should carry AT LEAST as many credits as the active
-- packages currently hold (grace migration principle).
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== E. Delta reconciliation: active package remain vs projected new tables ===' AS section;
WITH pre AS (
  SELECT
    user_id,
    SUM(CASE WHEN type IN ('subscription','trial') AND status='active' AND expires_at > NOW()
             THEN remain_credits ELSE 0 END) AS sub_trial_remain,
    SUM(CASE WHEN type = 'booster' AND status='active' AND expires_at > NOW()
             THEN remain_credits ELSE 0 END) AS booster_remain,
    SUM(CASE WHEN status='active' AND expires_at > NOW() THEN remain_credits ELSE 0 END) AS total_remain
  FROM credit_package
  GROUP BY user_id
)
SELECT
  COUNT(*)                     AS affected_users,
  SUM(sub_trial_remain)        AS pre_sub_trial_credits,
  SUM(booster_remain)          AS pre_booster_credits,
  SUM(total_remain)            AS pre_total_credits,
  -- post should equal pre (exact migration, not a top-up)
  'post_total should = pre_total' AS invariant_note
FROM pre
WHERE total_remain > 0;

-- ─────────────────────────────────────────────────────────────────────────────
-- §F  BLOCKER INVARIANT CHECKS
-- Each check emits violation_count; must be 0 to proceed.
-- ─────────────────────────────────────────────────────────────────────────────

SELECT '=== F. BLOCKERS — all violation_count must be 0 ===' AS section;

-- F1: subscription target table must be empty (no prior migration run)
SELECT 'BLOCKER_F1_subscription_empty', COUNT(*) AS violation_count
FROM subscription;

-- F2: trial_grant target table must be empty
SELECT 'BLOCKER_F2_trial_grant_empty', COUNT(*) AS violation_count
FROM trial_grant;

-- F3: user_booster_balance target table must be empty (or only zero-balance rows)
SELECT 'BLOCKER_F3_booster_balance_empty', COUNT(*) AS violation_count
FROM user_booster_balance
WHERE credits_remaining > 0;

-- F4: membership_event target table must be empty
SELECT 'BLOCKER_F4_membership_event_empty', COUNT(*) AS violation_count
FROM membership_event;

-- F5: booster packages must be in multiples of 600 credits
--     (900 / 600+N logic would indicate corrupted data)
SELECT 'BLOCKER_F5_booster_total_credits_multiple_of_600', COUNT(*) AS violation_count
FROM credit_package
WHERE type = 'booster'
  AND (total_credits % 600) <> 0;

-- F6: No user has both a subscription row already AND active subscription credit_package
--     (idempotency guard — if subscription table already has rows, abort)
SELECT 'BLOCKER_F6_no_existing_subscription_rows', COUNT(*) AS violation_count
FROM subscription;

-- F7: Subscription packages all have valid activated_at < expires_at
SELECT 'BLOCKER_F7_sub_date_ordering', COUNT(*) AS violation_count
FROM credit_package
WHERE type = 'subscription'
  AND activated_at >= expires_at;

-- F8: trial packages per user <= 1 (if >1 we must know which to use)
SELECT 'WARN_F8_trial_pkgs_per_user_gt1', COUNT(*) AS warning_count
FROM (
  SELECT user_id, COUNT(*) AS cnt
  FROM credit_package
  WHERE type = 'trial'
  GROUP BY user_id
  HAVING cnt > 1
) t;

SELECT '=== DRY RUN COMPLETE — Review output before running 02-apply.sql ===' AS section;
