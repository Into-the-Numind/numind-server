-- Rollback for b2b-billing-rules-rewrite hotfix index migration.
-- Drops the three indexes added in 20260520_180000_add_b2b_billing_indexes.sql.
-- Safe to apply online (DROP INDEX is non-blocking in MySQL 8.0+ for InnoDB).

DROP INDEX IF EXISTS `idx_sub_source_first_started_at` ON `subscription`;
DROP INDEX IF EXISTS `idx_sub_source_updated_at` ON `subscription`;
DROP INDEX IF EXISTS `idx_tg_source_granted_at` ON `trial_grant`;
