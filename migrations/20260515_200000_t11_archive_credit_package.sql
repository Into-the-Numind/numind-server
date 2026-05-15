-- T11 — Archive + DROP credit_package + DROP credit_account.balance
-- Plan ref: docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md §T11
--
-- Prerequisites (must verify before executing):
--   1. T9 complete: credit_package has 0 readers in production code
--   2. T10 complete: usage_record.credits_deducted dropped
--   3. 7-day monitoring confirmed: SELECT COUNT(*) FROM credit_package WHERE created_at > '<T9_deploy_time>' = 0
--   4. mysqldump backup taken: mysqldump --single-transaction numind-dev credit_package credit_account > /tmp/t11_backup.sql
--
-- CAUTION: Steps 4-5 (DROP TABLE / ALTER TABLE DROP COLUMN) are IRREVERSIBLE.
-- Do NOT run on prod without explicit user authorization.
-- Dev-only run per option D ("dev T7-T8 logic validation only, prod deferred").
--
-- user_booster_balance is NOT touched by this migration (per spec §2.4 and plan preservation note).

-- ============================================================
-- Step 1: Create archive table mirroring credit_package schema
-- ============================================================
CREATE TABLE IF NOT EXISTS `legacy_credit_package_archive_20260515` (
  `id`              BIGINT UNSIGNED NOT NULL,
  `user_id`         BIGINT UNSIGNED NOT NULL,
  `type`            VARCHAR(20)     NOT NULL,
  `total_credits`   INT             NOT NULL,
  `remain_credits`  INT             NOT NULL,
  `status`          VARCHAR(20)     NOT NULL,
  `grant_source`    VARCHAR(50)     DEFAULT NULL,
  `granter_user_id` BIGINT UNSIGNED DEFAULT NULL,
  `order_id`        BIGINT UNSIGNED DEFAULT NULL,
  `activated_at`    DATETIME(3)     DEFAULT NULL,
  `expires_at`      DATETIME(3)     DEFAULT NULL,
  `created_at`      DATETIME(3)     DEFAULT NULL,
  `updated_at`      DATETIME(3)     DEFAULT NULL,
  -- Archive metadata columns
  `archived_at`     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `archive_reason`  VARCHAR(200)    DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_archive_user_type` (`user_id`, `type`),
  KEY `idx_archive_grant_source` (`grant_source`),
  KEY `idx_archive_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='credit_package 归档表 (T11 2026-05-15)。保留 7 年与会计凭证同期。T9 getLegacyEvents stub 在 T11 后切到此表读 cutover_date 前历史。查询说明见 docs/legacy_credit_package_archive_README.md';

-- ============================================================
-- Step 2: Copy all rows from credit_package to archive table
-- ============================================================
-- INSERT IGNORE → idempotent: re-running the migration after partial completion
-- (e.g. archive table already populated) skips duplicate primary keys instead of
-- aborting. Safe because (id) is PK and rows are immutable once archived.
INSERT IGNORE INTO `legacy_credit_package_archive_20260515` (
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  `grant_source`, `granter_user_id`, `order_id`,
  `activated_at`, `expires_at`, `created_at`, `updated_at`,
  `archive_reason`
)
SELECT
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  `grant_source`, `granter_user_id`, `order_id`,
  `activated_at`, `expires_at`, `created_at`, `updated_at`,
  'T11 cleanup 2026-05-15: credit_package DROP, see docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md'
FROM `credit_package`;

-- ============================================================
-- Step 3: Verify archive row count matches original
-- (Run this SELECT manually before proceeding to Step 4)
-- Expected: source_count == archive_count
-- ============================================================
SELECT
  (SELECT COUNT(*) FROM `credit_package`)                                AS source_count,
  (SELECT COUNT(*) FROM `legacy_credit_package_archive_20260515`)        AS archive_count,
  (SELECT COUNT(*) FROM `credit_package`) =
  (SELECT COUNT(*) FROM `legacy_credit_package_archive_20260515`)        AS counts_match;

-- ============================================================
-- Step 4: DROP credit_package table (IRREVERSIBLE)
-- Only execute after verifying archive_count == source_count above.
-- IF EXISTS → idempotent: re-running the migration after the table is already
-- dropped is a no-op instead of an error.
-- ============================================================
DROP TABLE IF EXISTS `credit_package`;

-- ============================================================
-- Step 5: DROP credit_account.balance column (IRREVERSIBLE)
-- GetBalance now reads from credit_cycle + user_booster_balance + trial_grant (three-pool SOT).
-- NOTE: user_booster_balance is NOT dropped (it remains the booster SOT per spec §2.4).
-- IF EXISTS → idempotent (MySQL 8.0.4+): safe to re-run after column is gone.
-- ============================================================
ALTER TABLE `credit_account` DROP COLUMN IF EXISTS `balance`;

-- ============================================================
-- Post-run verification
-- ============================================================
-- Verify credit_package table no longer exists:
-- SHOW TABLES LIKE 'credit_package';                       -- expect: empty result
--
-- Verify credit_account.balance column no longer exists:
-- DESCRIBE credit_account;                                 -- expect: no 'balance' column
--
-- Verify archive has all rows:
-- SELECT COUNT(*) FROM legacy_credit_package_archive_20260515;  -- expect: original row count
--
-- Verify user_booster_balance is untouched:
-- DESCRIBE user_booster_balance;                           -- expect: user_id, credits_remaining, updated_at
