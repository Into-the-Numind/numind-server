-- 20260420_200000_seed_aihubmix_base_pricing_rollback.sql
--
-- 回滚 20260420_200000_seed_aihubmix_base_pricing.sql 注入的：
--   - 4 条 pricing_rule 行（aihubmix base 模型）
--   - 8 条 pricing_rule_tier 行（FK ON DELETE CASCADE 自动清理）
--
-- 注意：假设这 4 条规则除本 migration 外无其他来源。如果 dev DB 在本 migration
-- 之前已通过管理端人工创建，回滚会一并删除人工数据。执行前检查 created_at。

DELETE FROM pricing_rule
WHERE provider = 'aihubmix' AND model IN (
  'claude-sonnet-4-6',
  'deepseek-v3.2',
  'gemini-3.1-pro-preview',
  'gpt-5.4'
);

-- 验证回滚（应返回 0 行）
-- SELECT COUNT(*) AS should_be_zero FROM pricing_rule
-- WHERE provider = 'aihubmix' AND model IN (
--   'claude-sonnet-4-6', 'deepseek-v3.2',
--   'gemini-3.1-pro-preview', 'gpt-5.4');
