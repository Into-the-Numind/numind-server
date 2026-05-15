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
-- Step 1: Recreate credit_package table from archive schema
-- ============================================================
-- Use CREATE TABLE ... LIKE to guarantee zero type drift vs the archive
-- (source of truth). Then strip the archive-only audit columns and indexes,
-- and add back the AUTO_INCREMENT + original credit_package indexes.
--
-- This approach eliminates the type-mismatch class of bug (user_id width,
-- total_credits/remain_credits width, grant_source nullability, etc.) that
-- inevitably occurs if the explicit CREATE TABLE diverges from archive DDL.
CREATE TABLE IF NOT EXISTS `credit_package` LIKE `legacy_credit_package_archive_20260515`;

-- Drop archive-only audit columns + index (these don't belong on the live table).
ALTER TABLE `credit_package` DROP COLUMN `archived_at`;
ALTER TABLE `credit_package` DROP COLUMN `archive_reason`;
ALTER TABLE `credit_package` DROP INDEX `idx_archive_user_type`;
ALTER TABLE `credit_package` DROP INDEX `idx_archive_grant_source`;
ALTER TABLE `credit_package` DROP INDEX `idx_archive_created_at`;

-- Restore AUTO_INCREMENT on the primary key (archive table does not auto-increment).
ALTER TABLE `credit_package` MODIFY COLUMN `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT;

-- Restore the original credit_package indexes (matching the pre-T11 DDL from
-- migrations/add_credits_system.sql + migrations/20260420_100000_add_grant_fields_to_credit_package.sql).
ALTER TABLE `credit_package`
  ADD KEY `idx_cp_user_status_expires` (`user_id`, `status`, `expires_at`),
  ADD KEY `idx_cp_order` (`order_id`),
  ADD KEY `idx_grant_source_granter` (`grant_source`, `granter_user_id`, `activated_at`);

-- ============================================================
-- Step 2: Restore data from archive table
-- ============================================================
-- Recreated table schema matches the archive's column types exactly (via
-- CREATE TABLE LIKE above), so a column-aligned INSERT ... SELECT is safe.
INSERT INTO `credit_package` (
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  `grant_source`, `granter_user_id`, `order_id`,
  `activated_at`, `expires_at`, `created_at`, `updated_at`
)
SELECT
  `id`, `user_id`, `type`, `total_credits`, `remain_credits`, `status`,
  `grant_source`, `granter_user_id`, `order_id`,
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
