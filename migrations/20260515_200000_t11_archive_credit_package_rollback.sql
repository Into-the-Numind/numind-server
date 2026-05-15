-- T11 ROLLBACK — Restore credit_package from archive + restore credit_account.balance
-- Plan ref: docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md §T11 Rollback
--
-- When to execute:
--   - Only if T11 forward migration caused unexpected application errors
--   - Only within the 7-day hot backup window after archive table creation
--   - Also requires: git revert <T11_commit_sha> + server restart
--
-- IMPORTANT: This rollback is a best-effort recovery. After rollback:
--   - credit_package.status will be read-only (no cron updating status — T6 deleted that)
--   - credit_account.balance will be 0 for all users (stale cache; GetBalance will error until
--     RecalculateBalance is re-added or the balance column is populated from mysqldump)
--   - T2/T6 callers (RechargeCredits, legacy DeductCredits) have been deleted — rollback
--     must also git revert T2–T11 to restore full functionality
--
-- Pre-conditions:
--   - legacy_credit_package_archive_20260515 must exist (T11 forward Step 1-2 completed)
--   - mysqldump backup at /tmp/t11_backup.sql should be available for balance recovery

-- ============================================================
-- Step 1: Recreate credit_package table with original schema
-- ============================================================
-- Using the original DDL from migrations/add_credits_system.sql +
-- migrations/20260420_100000_add_grant_fields_to_credit_package.sql
CREATE TABLE IF NOT EXISTS `credit_package` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`         INT UNSIGNED    NOT NULL,
  `type`            VARCHAR(20)     NOT NULL,
  `total_credits`   BIGINT          NOT NULL,
  `remain_credits`  BIGINT          NOT NULL,
  `activated_at`    DATETIME(3)     NOT NULL,
  `expires_at`      DATETIME(3)     NOT NULL,
  `order_id`        BIGINT UNSIGNED DEFAULT NULL,
  `status`          VARCHAR(20)     NOT NULL,
  -- Q1 B2B2C grant fields (from migration 20260420_100000)
  `grant_source`    VARCHAR(20)     NOT NULL DEFAULT 'self_purchase',
  `granter_user_id` INT UNSIGNED    DEFAULT NULL,
  `created_at`      DATETIME(3)     DEFAULT NULL,
  `updated_at`      DATETIME(3)     DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_cp_user_status_expires` (`user_id`, `status`, `expires_at`),
  KEY `idx_cp_order` (`order_id`),
  KEY `idx_grant_source_granter` (`grant_source`, `granter_user_id`, `activated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- Step 2: Restore data from archive table
-- ============================================================
INSERT INTO `credit_package` (
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  `grant_source`, `granter_user_id`, `order_id`,
  `activated_at`, `expires_at`, `created_at`, `updated_at`
)
SELECT
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  COALESCE(`grant_source`, 'self_purchase'), `granter_user_id`, `order_id`,
  `activated_at`, `expires_at`, `created_at`, `updated_at`
FROM `legacy_credit_package_archive_20260515`;

-- ============================================================
-- Step 3: Restore credit_account.balance column
-- ============================================================
-- NOTE: balance values will be 0 after this ALTER.
-- To restore accurate balances, run after this step:
--   mysql < /tmp/t11_backup.sql   (partial restore for credit_account only)
-- OR manually UPDATE using:
--   UPDATE credit_account ca
--   SET balance = (
--     SELECT COALESCE(SUM(remain_credits), 0)
--     FROM credit_package
--     WHERE user_id = ca.user_id AND status = 'active'
--   );
ALTER TABLE `credit_account`
  ADD COLUMN `balance` BIGINT NOT NULL DEFAULT 0 AFTER `user_id`;

-- ============================================================
-- Step 4: Verify restoration
-- ============================================================
SELECT
  (SELECT COUNT(*) FROM `credit_package`)                             AS restored_count,
  (SELECT COUNT(*) FROM `legacy_credit_package_archive_20260515`)     AS archive_count;
-- Expected: restored_count == archive_count

-- ============================================================
-- Step 5: Manual steps required after SQL rollback
-- ============================================================
-- 1. git revert <T11_commit_sha> (restores Go model + helper.go)
-- 2. Restart numind-server
-- 3. Verify: server starts without "Table 'credit_package' doesn't exist" errors
-- 4. Verify: GetBalance returns correct values (may need balance recalculation)
-- 5. Consider: If rollback needed beyond T11, must also revert T2-T10 commits
