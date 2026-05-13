-- =============================================================================
-- 02-apply.sql  —  membership-credits-redesign migration APPLY
-- Migrates credit_package rows → subscription / trial_grant /
-- user_booster_balance / membership_event new tables.
--
-- Run with:
--   docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < 02-apply.sql
--
-- Prerequisites:
--   01-dry-run.sql must show all BLOCKER_Fxx violation_count = 0
--
-- Guarantees:
--   • All writes are inside one transaction (auto-rollback on error)
--   • Backup table created OUTSIDE transaction so it survives tx abort
--   • apply_log table records inserted row counts for rollback targeting
-- =============================================================================

-- ─────────────────────────────────────────────────────────────────────────────
-- PRE-TX: Create backup snapshot (outside transaction — survives abort)
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS migration_20260430_credit_pkg_backup (
  id             BIGINT UNSIGNED NOT NULL,
  user_id        INT UNSIGNED    NOT NULL,
  type           VARCHAR(20)     NOT NULL,
  total_credits  BIGINT          NOT NULL,
  remain_credits BIGINT          NOT NULL,
  activated_at   DATETIME(0)     NOT NULL,
  expires_at     DATETIME(0)     NOT NULL,
  status         VARCHAR(20)     NOT NULL,
  grant_source   VARCHAR(20)     NOT NULL,
  granter_user_id INT UNSIGNED   NULL,
  order_id       BIGINT UNSIGNED NULL,
  created_at     DATETIME(0)     NOT NULL,
  updated_at     DATETIME(0)     NOT NULL,
  backed_up_at   DATETIME(0)     NOT NULL DEFAULT (NOW()),
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO migration_20260430_credit_pkg_backup
  (id, user_id, type, total_credits, remain_credits, activated_at,
   expires_at, status, grant_source, granter_user_id, order_id, created_at, updated_at)
SELECT
  id, user_id, type, total_credits, remain_credits, activated_at,
  expires_at, status, grant_source, granter_user_id, order_id, created_at, updated_at
FROM credit_package;

-- Sanity: row counts must match
SELECT
  (SELECT COUNT(*) FROM credit_package) AS source_rows,
  (SELECT COUNT(*) FROM migration_20260430_credit_pkg_backup) AS backup_rows,
  CASE
    WHEN (SELECT COUNT(*) FROM credit_package) =
         (SELECT COUNT(*) FROM migration_20260430_credit_pkg_backup)
    THEN 'BACKUP_OK'
    ELSE 'BACKUP_MISMATCH — STOP'
  END AS backup_check;

-- ─────────────────────────────────────────────────────────────────────────────
-- TRANSACTION BEGIN
-- ─────────────────────────────────────────────────────────────────────────────

START TRANSACTION;

-- apply_log: tracks what we inserted so rollback can DELETE by exact key range
CREATE TABLE IF NOT EXISTS migration_20260430_apply_log (
  step        VARCHAR(60)  NOT NULL,
  table_name  VARCHAR(60)  NOT NULL,
  rows_inserted INT        NOT NULL DEFAULT 0,
  applied_at  DATETIME(0)  NOT NULL DEFAULT (NOW()),
  PRIMARY KEY (step)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @cutover_ts = NOW();

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 1: INSERT subscription
-- Merge consecutive subscription packages per user into one row.
-- Gap threshold: <= 2 days between prior expires_at and new activated_at.
-- total_months_purchased = CEIL(total calendar days in segment / 30).
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO subscription
  (user_id, first_started_at, current_started_at, expires_at,
   total_months_purchased, source, granter_user_id, created_at, updated_at)
WITH ordered AS (
  SELECT
    user_id,
    activated_at,
    expires_at,
    grant_source,
    granter_user_id,
    LAG(expires_at) OVER (PARTITION BY user_id ORDER BY activated_at) AS prev_expires
  FROM credit_package
  WHERE type = 'subscription'
    AND status IN ('active', 'exhausted', 'expired')
),
with_breaks AS (
  SELECT *,
    CASE
      WHEN prev_expires IS NULL THEN 1
      WHEN DATEDIFF(activated_at, prev_expires) > 2 THEN 1
      ELSE 0
    END AS is_new_segment
  FROM ordered
),
with_seg_id AS (
  SELECT *,
    SUM(is_new_segment) OVER (
      PARTITION BY user_id ORDER BY activated_at ROWS UNBOUNDED PRECEDING
    ) AS seg_id
  FROM with_breaks
),
merged AS (
  SELECT
    user_id,
    MIN(activated_at)                                       AS first_started_at,
    MIN(activated_at)                                       AS current_started_at,
    MAX(expires_at)                                         AS expires_at,
    GREATEST(1, CEIL(DATEDIFF(MAX(expires_at), MIN(activated_at)) / 30)) AS total_months_purchased,
    -- take source/granter from the most recent package in segment
    SUBSTRING_INDEX(GROUP_CONCAT(grant_source ORDER BY activated_at DESC SEPARATOR '|'), '|', 1)    AS source,
    -- IFNULL replaces SQL NULL with literal 0 (not string 'NULL' which would fail
    -- CAST AS UNSIGNED with "Truncated incorrect INTEGER value: 'NULL'"); the outer
    -- SELECT below uses NULLIF(granter_user_id, 0) to restore SQL NULL semantics.
    CAST(SUBSTRING_INDEX(GROUP_CONCAT(IFNULL(granter_user_id, 0) ORDER BY activated_at DESC SEPARATOR '|'), '|', 1) AS UNSIGNED) AS granter_user_id
  FROM with_seg_id
  GROUP BY user_id, seg_id
)
SELECT
  user_id,
  first_started_at,
  current_started_at,
  expires_at,
  total_months_purchased,
  source,
  NULLIF(granter_user_id, 0) AS granter_user_id,
  @cutover_ts,  -- created_at
  @cutover_ts   -- updated_at
FROM merged;

INSERT INTO migration_20260430_apply_log (step, table_name, rows_inserted)
VALUES ('step1_subscription', 'subscription', ROW_COUNT());

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 2: INSERT trial_grant
-- One row per user: pick the earliest trial credit_package row.
-- credits_remaining = remain_credits from that earliest row.
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO trial_grant
  (user_id, granted_at, expires_at, credits_remaining, source, granter_user_id, created_at)
SELECT
  cp.user_id,
  cp.activated_at                                 AS granted_at,
  cp.expires_at,
  cp.remain_credits                               AS credits_remaining,
  cp.grant_source                                 AS source,
  cp.granter_user_id,
  @cutover_ts                                     AS created_at
FROM credit_package cp
INNER JOIN (
  SELECT user_id, MIN(activated_at) AS min_at
  FROM credit_package
  WHERE type = 'trial'
  GROUP BY user_id
) earliest ON earliest.user_id = cp.user_id
           AND cp.activated_at = earliest.min_at
           AND cp.type = 'trial';

INSERT INTO migration_20260430_apply_log (step, table_name, rows_inserted)
VALUES ('step2_trial_grant', 'trial_grant', ROW_COUNT());

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 3: INSERT user_booster_balance
-- One row per user with any booster package (active or not — carry full balance).
-- credits_remaining = SUM of remain_credits from currently active booster pkgs.
-- Users with only exhausted/expired boosters get a 0-balance row (no harm).
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO user_booster_balance (user_id, credits_remaining, updated_at)
SELECT
  user_id,
  SUM(CASE WHEN status = 'active' AND expires_at > @cutover_ts
           THEN remain_credits ELSE 0 END) AS credits_remaining,
  @cutover_ts AS updated_at
FROM credit_package
WHERE type = 'booster'
GROUP BY user_id;

INSERT INTO migration_20260430_apply_log (step, table_name, rows_inserted)
VALUES ('step3_user_booster_balance', 'user_booster_balance', ROW_COUNT());

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 4: INSERT membership_event
-- Reconstruct audit events from credit_package rows.
-- event_type mapping:
--   trial       → 'trial_granted'
--   subscription, first row per user → 'sub_granted'
--   subscription, subsequent rows   → 'sub_renewed'
--   booster     → 'booster_granted'
-- product_type mapping:
--   trial       → 'trial'
--   subscription → 'monthly'
--   booster     → 'booster'
-- months: subscription → GREATEST(1, CEIL(DATEDIFF(expires_at, activated_at)/30)), others NULL
-- quantity: booster → total_credits/600, others NULL
-- amount_cents: 0 (not stored in credit_package)
-- idempotency_key: 'migration-20260430-cp-{credit_package.id}'
-- occurred_at: credit_package.activated_at
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO membership_event
  (user_id, event_type, product_type, months, quantity, amount_cents,
   source, granter_user_id, idempotency_key, subscription_id, occurred_at)
-- trial events
SELECT
  cp.user_id,
  'trial_granted'                                  AS event_type,
  'trial'                                          AS product_type,
  NULL                                             AS months,
  NULL                                             AS quantity,
  0                                                AS amount_cents,
  cp.grant_source                                  AS source,
  cp.granter_user_id,
  CONCAT('migration-20260430-cp-', cp.id)          AS idempotency_key,
  NULL                                             AS subscription_id,
  cp.activated_at                                  AS occurred_at
FROM credit_package cp
WHERE cp.type = 'trial'

UNION ALL

-- subscription events (sub_granted = first pkg per user, sub_renewed = subsequent)
SELECT
  cp.user_id,
  CASE
    WHEN ROW_NUMBER() OVER (PARTITION BY cp.user_id ORDER BY cp.activated_at) = 1
    THEN 'sub_granted'
    ELSE 'sub_renewed'
  END                                              AS event_type,
  'monthly'                                        AS product_type,
  CAST(GREATEST(1, CEIL(DATEDIFF(cp.expires_at, cp.activated_at) / 30)) AS UNSIGNED) AS months,
  NULL                                             AS quantity,
  0                                                AS amount_cents,
  cp.grant_source                                  AS source,
  cp.granter_user_id,
  CONCAT('migration-20260430-cp-', cp.id)          AS idempotency_key,
  s.id                                             AS subscription_id,
  cp.activated_at                                  AS occurred_at
FROM credit_package cp
LEFT JOIN subscription s ON s.user_id = cp.user_id
WHERE cp.type = 'subscription'

UNION ALL

-- booster events
SELECT
  cp.user_id,
  'booster_granted'                                AS event_type,
  'booster'                                        AS product_type,
  NULL                                             AS months,
  CAST(cp.total_credits / 600 AS UNSIGNED)         AS quantity,
  0                                                AS amount_cents,
  cp.grant_source                                  AS source,
  cp.granter_user_id,
  CONCAT('migration-20260430-cp-', cp.id)          AS idempotency_key,
  NULL                                             AS subscription_id,
  cp.activated_at                                  AS occurred_at
FROM credit_package cp
WHERE cp.type = 'booster';

INSERT INTO migration_20260430_apply_log (step, table_name, rows_inserted)
VALUES ('step4_membership_event', 'membership_event', ROW_COUNT());

-- ─────────────────────────────────────────────────────────────────────────────
-- Step 5: Summary log
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO migration_20260430_apply_log (step, table_name, rows_inserted)
VALUES ('step5_done', 'migration_20260430_apply_log', 1);

SELECT step, table_name, rows_inserted, applied_at
FROM migration_20260430_apply_log
ORDER BY applied_at;

-- ─────────────────────────────────────────────────────────────────────────────
-- COMMIT
-- ─────────────────────────────────────────────────────────────────────────────

COMMIT;

SELECT 'APPLY COMPLETE — run 03-verify.sql to audit post-migration state' AS status;
