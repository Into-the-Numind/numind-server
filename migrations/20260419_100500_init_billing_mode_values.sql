-- Credits System: One-time billing_mode initialization (Grandfathering Option E)
-- Part of credits-system feature (Phase 0 契约冻结, Track A will execute with envsubst)
-- See spec §2.7
--
-- DEPLOYMENT: This file contains ${MIGRATION_CUTOFF} placeholder.
-- Before applying, use envsubst with whitelist mode to avoid clobbering other $ characters:
--
--   export MIGRATION_CUTOFF=$(date -u '+%Y-%m-%d %H:%M:%S')
--   envsubst '${MIGRATION_CUTOFF}' < 20260419_100500_init_billing_mode_values.sql | mysql ...
--
-- 记录部署时间，supply 迁移 snapshot 依据。
--
-- NOTE: 列名是 `user_tier`（GORM 无 explicit column tag，从 struct field UserTier 自动
-- snake_case 映射）。不是 `tier`。Phase 0 hotfix 2026-04-19 修正。

-- Step 1: 迁移前分布统计（审计）
SELECT user_tier,
       CASE
           WHEN tier_expires IS NULL THEN 'no_expires'
           WHEN tier_expires > '${MIGRATION_CUTOFF}' THEN 'in_period'
           ELSE 'expired'
       END AS period_status,
       COUNT(*) AS user_count
FROM `user`
GROUP BY user_tier, period_status;

-- Step 2: 在期 standard/premium/trial → legacy_tier（Option E Grandfathering，含 trial）
-- 幂等：billing_mode='credits' 守卫避免覆盖人工调整
-- NULL tier_expires 不算在期，保留 credits 默认（脏数据/永久账号另行标记）
UPDATE `user`
SET billing_mode = 'legacy_tier'
WHERE user_tier IN ('standard', 'premium', 'trial')
  AND tier_expires IS NOT NULL
  AND tier_expires > '${MIGRATION_CUTOFF}'
  AND billing_mode = 'credits';

-- Step 3: 迁移后分布 sanity check
SELECT billing_mode, user_tier, COUNT(*) AS user_count
FROM `user`
GROUP BY billing_mode, user_tier
ORDER BY billing_mode, user_tier;
