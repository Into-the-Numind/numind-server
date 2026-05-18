-- T4 of legacy-system-deprecation feature.
-- Pre-conditions verified by parent session:
--   1. server v2.1.23 (T1), v2.1.24 (T2), v2.1.25 (T3) prod deployed and healthy
--   2. Prod DB full backup at /root/backups/numind-prod-pre-t4-*.sql
--   3. SELECT COUNT(*) FROM user WHERE user_tier!='free' OR billing_mode!='credits' = 0

ALTER TABLE `user`
  DROP COLUMN `user_tier`,
  DROP COLUMN `tier_expires`,
  DROP COLUMN `monthly_sop_runs`,
  DROP COLUMN `monthly_reset_at`,
  DROP COLUMN `billing_mode`;

-- Rename audit table (1-year retention per spec §12 deferred decision (b))
RENAME TABLE `tier_change_log` TO `legacy_tier_change_log`;
