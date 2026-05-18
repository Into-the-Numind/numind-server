-- Rollback for T4 schema DROP.
-- IMPORTANT: DROP loses data. This rollback only restores schema. To restore
-- data, run mysqldump restore from /root/backups/numind-prod-pre-t4-*.sql.

ALTER TABLE `user`
  ADD COLUMN `billing_mode` ENUM('legacy_tier','credits') NOT NULL DEFAULT 'credits',
  ADD COLUMN `monthly_sop_runs` INT DEFAULT 0,
  ADD COLUMN `monthly_reset_at` TIMESTAMP NULL DEFAULT NULL,
  ADD COLUMN `user_tier` VARCHAR(20) DEFAULT 'free',
  ADD COLUMN `tier_expires` TIMESTAMP NULL DEFAULT NULL;

ALTER TABLE `user`
  ADD INDEX `idx_user_billing_mode` (`billing_mode`),
  ADD INDEX `idx_user_tier` (`user_tier`);

RENAME TABLE `legacy_tier_change_log` TO `tier_change_log`;
