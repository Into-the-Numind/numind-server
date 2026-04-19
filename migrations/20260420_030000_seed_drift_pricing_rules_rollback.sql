-- 20260420_030000_seed_drift_pricing_rules_rollback.sql
--
-- 回滚 20260420_030000_seed_drift_pricing_rules.sql 注入的：
--   - 6 条 pricing_rule 行
--   - 8 条 pricing_rule_tier 行（FK ON DELETE CASCADE 自动清理）
--
-- 注意：本回滚假设这 6 条规则除本 migration 外无其他来源。如果 dev DB 在
-- 本 migration 之前已通过管理端人工创建，回滚会一并删除人工数据。
-- 执行前先确认这些行确实来自本 migration（检查 created_at）。

DELETE FROM pricing_rule
WHERE
  (provider = 'aihubmix' AND model IN (
    'claude-sonnet-4-6-thinking',
    'deepseek-v3.2-thinking',
    'gemini-3.1-pro-preview-thinking',
    'gpt-5.4-thinking'
  ))
  OR (provider = 'ali-dashscope' AND model = 'qwen3-vl-flash' AND service_type = 'llm_vision')
  OR (provider = '' AND model = '' AND service_type = 'llm_chat');

-- 验证回滚（应返回 0 行）
-- SELECT COUNT(*) AS should_be_zero FROM pricing_rule
-- WHERE (provider='aihubmix' AND model LIKE '%-thinking')
--    OR (provider='ali-dashscope' AND model='qwen3-vl-flash')
--    OR (provider='' AND model='' AND service_type='llm_chat');
